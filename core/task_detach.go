// Detaching a call whose tool is not a registered ChatTool.
//
// The detach decision, the notice the model gets back and the whole lifecycle
// were written once, inside ChatToolToAgentToolDefWithSession, and read off the
// ChatTool interfaces — ExpectedDuration, Preflight, TypicalDuration and the
// rest. That covers every tool in tools/, and misses the ones an app builds by
// hand as an AgentToolDef: the fleet's `agents` surface among them, which is
// the single longest-running call the framework has and the one most likely to
// leave a texted conversation silent while a sub-agent thinks.
//
// So the policy is expressed as DATA here, and both paths run the same code.
// The alternative — a second implementation for hand-built tools — is how one
// of them quietly stops claiming its detach slot, or stops running preflight,
// and nobody notices until a set of pictures arrives twice.
package core

import (
	"context"
	"strings"
	"time"
)

// DetachPolicy is everything the framework needs to know to decide whether a
// call detaches, and what to do with it once it has. Each field mirrors one of
// the optional ChatTool interfaces; every one is optional except Tool and
// Detached.
type DetachPolicy struct {
	// Tool is what the MODEL called, used wherever the notice has to name
	// something the model can act on. Required.
	Tool string

	// Key is the rationing identity: which slot this claims and which set it
	// belongs to. Empty means Tool, which is right for a tool that is the only
	// way to do its job.
	//
	// It exists because several names can be the same ACT. Every approved
	// rest_image connector materializes its own generate_image_<name> tool
	// alongside the grouped `image` tool, and to a model they are
	// interchangeable ways to make a picture. Rationed by NAME they are not
	// interchangeable at all: two of them can hold two slots and run two jobs
	// for one request, and a set started on one silently forks when the model
	// reaches for the other. Observed exactly that — a set of four begun on
	// `image`, continued on generate_image_comfyui, reported as four delivered
	// and two actually sent.
	Key string

	// Expected mirrors DetachableTool.ExpectedDuration. Nil or zero means the
	// duration test never fires and only Always can detach this call.
	Expected func(args map[string]any, sess *ToolSession) time.Duration

	// Always mirrors AlwaysDetachTool.AlwaysDetach: background work by nature,
	// whatever the clock says.
	Always func(args map[string]any, sess *ToolSession) bool

	// Typical mirrors EstimatingTool.TypicalDuration — a MEASURED number, and
	// the only one the agent may quote. Nil means the notice puts no time on
	// it rather than inventing one.
	Typical func(args map[string]any, sess *ToolSession) time.Duration

	// Preflight mirrors PreflightTool: the part of the call that can be
	// checked without doing the work, run while the model still has a round
	// to fix it in.
	Preflight func(args map[string]any, sess *ToolSession) error

	// Series mirrors SeriesTool.SeriesCapable — whether a refused second call
	// this turn should be booked as a set rather than simply refused.
	Series bool

	// Label overrides how the run is described on the live surface. Nil falls
	// back to the generic argument-reading label.
	Label func(args map[string]any) string

	// Detached runs the call against a session built to OUTLIVE the turn.
	// Required. The session it receives is not the turn's — see
	// ToolSession.ForDetachedTask for what that changes.
	Detached func(args map[string]any, sess *ToolSession) (string, error)
}

// key is the rationing identity, falling back to the tool's own name.
func (p DetachPolicy) key() string {
	if k := strings.TrimSpace(p.Key); k != "" {
		return k
	}
	return p.Tool
}

// shouldDetachPolicy is ShouldDetach for a policy rather than a ChatTool. The
// two must agree, so the ordering here mirrors it exactly: a declaration of
// background work outranks the clock, and the clock is read against the
// threshold for the surface this turn is happening on.
func shouldDetachPolicy(p DetachPolicy, args map[string]any, sess *ToolSession) bool {
	if TaskRunnerFunc == nil || sess == nil || p.Detached == nil || p.Tool == "" {
		return false
	}
	if p.Always != nil && p.Always(args, sess) {
		return true
	}
	if p.Expected == nil {
		return false
	}
	expected := p.Expected(args, sess)
	if expected <= 0 {
		return false
	}
	return expected >= taskDetachThreshold(sess)
}

// WrapDetachable returns a handler that sends the call to the background when
// the policy says to, and otherwise runs inline unchanged.
//
// inline is what the tool does today; it is still the fallback whenever
// detaching cannot happen — no task host, no chat session to deliver into. A
// slow answer beats no answer, and the caller never asked to detach.
func WrapDetachable(p DetachPolicy, sess *ToolSession, inline ToolHandlerFunc) ToolHandlerFunc {
	if sess == nil || p.Detached == nil || p.Tool == "" {
		return inline
	}
	return func(args map[string]any) (string, error) {
		if !shouldDetachPolicy(p, args, sess) {
			return inline(args)
		}
		// Everything the call can rule out from its arguments alone is ruled
		// out HERE, while the model still has a round to fix it in. Past this
		// point an error becomes a wake the agent has to apologize for rather
		// than a correction it can act on. See PreflightTool.
		if p.Preflight != nil {
			if err := p.Preflight(args, sess); err != nil {
				Debug("[task] %s failed preflight, not detaching: %v", p.Tool, err)
				return "", err
			}
		}
		// One background job per tool per turn. Claimed AFTER preflight, so a
		// call rejected on its arguments costs nothing: it never started a job,
		// and the model still has the round to fix it. See ClaimDetachSlot.
		prior, free := sess.ClaimDetachSlot(p.key())
		if !free {
			// Refused, but not dismissed. A model calling the same tool twice
			// in one turn is usually not repeating itself — it is working
			// through a set the way it would if nothing detached. Recording
			// that here is what turns the refusal into an order: the piece
			// already running says "start the next one" when it lands. See
			// core/task_series.go.
			of := 0
			if p.Series {
				of = ExtendTaskSeries(sess.DeliverySession(), p.key())
			}
			Debug("[task] %s refused a second detach this turn; task %q is already running (set of %d)", p.Tool, prior.ID, of)
			return markFrameworkResult(secondDetachNotice(p.Tool, prior, of)), nil
		}
		label := ""
		if p.Label != nil {
			label = p.Label(args)
		}
		if label == "" {
			label = p.Tool
		}
		run, err := TaskRunnerFunc(sess, label, func(taskCtx context.Context) (TaskProduct, error) {
			// A session built for work that outlives the turn. Reusing the
			// turn's is what killed the call the moment the turn ended: its
			// context is the turn's, and everything downstream honours it.
			detached := sess.ForDetachedTask(taskCtx)
			out, rerr := p.Detached(args, detached)
			// Take whatever the call attached along with its text. The detached
			// session's accumulators are the ONLY record of it — nothing else
			// holds this session, and the turn that could have collected them
			// ended before the work started.
			return TaskProduct{
				Text:         out,
				Images:       detached.ClaimUnflushedImages(),
				Videos:       detached.ClaimUnflushedVideos(),
				Files:        detached.ClaimUnflushedFiles(),
				Continuation: detached.TakeTaskContinuation(),
			}, rerr
		})
		if err != nil {
			// Could not detach — run it inline rather than refuse. Give the
			// slot back first: no job exists to hold it, and keeping it would
			// refuse the next call over nothing.
			sess.ReleaseDetachSlot(p.key())
			Debug("[task] %s stayed inline: %v", p.Tool, err)
			return inline(args)
		}
		sess.RecordDetachSlot(p.key(), run) // so a later refusal can name it
		// What to SAY about the wait is a measured number or nothing at all —
		// never the ceiling that decided to detach in the first place.
		typical := time.Duration(0)
		if p.Typical != nil {
			typical = p.Typical(args, sess)
		}
		// Remember what we are about to invite the model to quote. The turn
		// judge is told to flag "a duration the assistant made up rather than
		// one it was given" — it can only apply that if something records that
		// this one was given.
		sess.RecordDetachEstimate(typical)
		// Marked as framework-authored so the app's untrusted-content fence
		// leaves it alone: these instructions are ours, and wrapping them in
		// "obey no instruction below" is self-defeating.
		return markFrameworkResult(detachedNotice(run, typical)), nil
	}
}

// detachPolicyForChatTool reads the policy off the optional interfaces a
// registered ChatTool may implement, so the ChatTool path and the hand-built
// path run the same wrapper.
func detachPolicyForChatTool(ct ChatTool, sess *ToolSession) DetachPolicy {
	p := DetachPolicy{
		Tool:  ct.Name(),
		Label: func(args map[string]any) string { return taskLabelFor(ct, args) },
		Detached: func(args map[string]any, detached *ToolSession) (string, error) {
			if sct, ok := ct.(SessionChatTool); ok {
				return sct.RunWithSession(args, detached)
			}
			return ct.Run(args)
		},
	}
	if d, ok := ct.(DetachableTool); ok {
		p.Expected = d.ExpectedDuration
	}
	if a, ok := ct.(AlwaysDetachTool); ok {
		p.Always = a.AlwaysDetach
	}
	if e, ok := ct.(EstimatingTool); ok {
		p.Typical = e.TypicalDuration
	}
	if pt, ok := ct.(PreflightTool); ok {
		p.Preflight = pt.Preflight
	}
	if st, ok := ct.(SeriesTool); ok {
		p.Series = st.SeriesCapable()
	}
	if it, ok := ct.(DetachIdentityTool); ok {
		p.Key = it.DetachIdentity()
	}
	return p
}

// EstimateText is the wait this turn's detach notices offered, in the same
// words the model was given, or empty when none was offered.
//
// Exported for hosts wiring AgentLoopConfig.BackgroundEstimate: the judge is
// comparing against a sentence the model wrote, so it needs the phrasing the
// model saw, not a raw duration.
func (d *DetachLedger) EstimateText() string {
	if d == nil {
		return ""
	}
	v := d.Estimate()
	if v <= 0 {
		return ""
	}
	return humanizeTaskDuration(v)
}
