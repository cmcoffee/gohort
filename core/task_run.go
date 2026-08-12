// Detached tool calls — work that outlives the turn that started it.
//
// A batch of tool calls already runs concurrently (see the agent loop's
// fan-out), so parallelism WITHIN a round is not the gap. The gap is duration:
// the batch still joins before the round ends, so one slow call holds the whole
// answer. A ComfyUI edit can take fifteen minutes, and for those fifteen minutes
// the assistant cannot say anything at all.
//
// Detaching turns that call into a RUN rather than an agent. There is nothing to
// decide while it waits — fire, wait, report — so it costs no LLM rounds; what
// it borrows from the agent machinery is the lifecycle: a registry entry, a
// cancel function, a row in the live surface nested under the turn that started
// it, and a way to deliver the result afterwards.
//
// The framework decides, not the model. A tool reports how long it expects to
// take (data it already has — an image backend knows its own poll deadline) and
// anything past the threshold detaches. Asking the model to choose would make
// detaching a judgement call it has no way to evaluate, on top of a delivery
// contract it already gets wrong.
package core

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func init() {
	RegisterTunable(TunableSpec{
		Key: "tune_task_detach_threshold", Category: "Timeouts",
		Label: "Detach long tool calls after",
		Help:  "A tool call expected to take longer than this runs detached: it returns straight away, keeps working in the background, and delivers its result into the conversation when it finishes. Set high to keep everything inline.",
		// 300s clears the image-generate deadline (180s) and sits under the edit
		// one (900s), so today only edits detach.
		//
		// It is a workaround, and worth replacing. A tool's estimate is its
		// DEADLINE — how long before we give up — not how long the work takes,
		// so it over-reports by however much headroom the operator left. At 120s
		// that made every generate detach, including ones that finish in twenty
		// seconds. Tuning the threshold around a bad estimate only holds while
		// the two backends stay this far apart; measuring what a backend
		// actually takes is the fix.
		//
		// Detaching is not free either: the conversation pauses between rounds,
		// and a long enough gap lets other traffic evict its prefix from the
		// llama slot, so the wake turn re-prefills. An unnecessary detach buys a
		// cold prefill nobody asked for.
		Kind: KindSeconds, Default: 300, Min: 15, Max: 3600,
	})
	// The same call, judged by where the conversation is happening.
	//
	// A threshold is really a question about how long the assistant may go
	// quiet, and the honest answer differs by surface. In a web chat the person
	// watches a live pill and a progress tree, so a minute of work looks like a
	// minute of work. On a messaging channel there is nothing to look at: the
	// contact sent a text and the next thing they see is a reply, so the same
	// minute reads as an assistant that stopped answering.
	//
	// Low by default, because on that surface the cost of detaching (an extra
	// message) is small and the cost of waiting (looking gone) is what people
	// actually complain about. Raise it to make a channel behave like a chat.
	RegisterTunable(TunableSpec{
		Key: "tune_task_detach_threshold_unattended", Category: "Timeouts",
		Label: "Detach long tool calls after (messaging)",
		Help:  "Detach threshold for turns answering a messaging conversation, where the person sees nothing at all until a reply arrives — no progress indicator, no live status. Set lower than the main threshold so an agent on a channel answers straight away and reports back, instead of going quiet mid-conversation. Raise it to the main value to treat both surfaces alike.",
		Kind:  KindSeconds, Default: 20, Min: 5, Max: 3600,
	})
}

// DetachableTool is an optional interface for tools whose calls can outlive the
// turn. ExpectedDuration reports how long THIS call is likely to take, given
// its arguments — an image tool answers with the chosen backend's own deadline.
// Zero means "no idea, keep it inline", which is the safe default for any tool
// that doesn't implement this.
//
// It is asked per CALL, not per tool: the same tool can be a two-second fetch
// and a fifteen-minute render depending on what it was handed.
type DetachableTool interface {
	ChatTool
	ExpectedDuration(args map[string]any, sess *ToolSession) time.Duration
}

// EstimatingTool is an optional companion to DetachableTool: what the call
// USUALLY takes, as opposed to how long the framework is willing to wait for
// it. Only implement it when the number is measured — the point of the split is
// that a ceiling makes a terrible estimate, and a second guess dressed as an
// observation is no better than the first.
//
// Zero means "not known", and the notice then tells the model to put no time on
// it at all rather than invent one.
type EstimatingTool interface {
	ChatTool
	TypicalDuration(args map[string]any, sess *ToolSession) time.Duration
}

// AlwaysDetachTool is a DetachableTool whose work belongs in the background
// because of WHAT IT IS, not how long it takes.
//
// The duration test cannot express this. A render that finishes in twenty
// seconds is fast by any measure and still holds the whole conversation for
// those twenty seconds — and a set of four holds it for eighty, during which
// the assistant cannot say a word. Detaching them turns one blocked turn into
// four messages that arrive as each picture is ready, which is what a person
// asking for variations actually wants.
//
// It is a policy, so it belongs behind a knob rather than in an estimate: a
// deployment that would rather wait keeps the duration rule by turning it off.
type AlwaysDetachTool interface {
	DetachableTool
	// AlwaysDetach reports that THIS call goes to the background whatever its
	// estimate says. Per call, because the same tool has actions that should
	// not — a URL fetch has nothing to wait for.
	AlwaysDetach(args map[string]any, sess *ToolSession) bool
}

// SeriesTool is a DetachableTool that can work through a SET one piece per
// turn: it books each finished piece against the count (AdvanceTaskSeries) and
// leaves the instruction that starts the next (SetTaskContinuation).
//
// Declared rather than assumed, because the refusal a second detach gets now
// PROMISES that the running piece will ask for the next one. A tool that does
// not keep that promise leaves the model waiting for an instruction nothing
// will send — worse than the flat refusal it replaced.
type SeriesTool interface {
	DetachableTool
	// SeriesCapable reports that this tool books its own pieces. A method
	// rather than a marker so it reads as a capability at the call site.
	SeriesCapable() bool
}

// DetachIdentityTool lets several tools that are the SAME ACT under different
// names share one background slot and one set.
//
// The rationing is per identity, not per name, and the distinction is not
// academic: every approved rest_image connector materializes its own
// generate_image_<name> tool beside the grouped `image` tool, and a model
// treats them as interchangeable ways to make a picture. Rationed by name, two
// of them run two jobs for one request and deliver it twice; a set begun on one
// forks when the model reaches for the other, which is how a set of four
// reported four and sent two.
//
// The name still appears in every message the model reads — only the
// bookkeeping is shared.
type DetachIdentityTool interface {
	ChatTool
	DetachIdentity() string
}

// RenderDetachIdentity is the shared identity for every tool whose job is
// producing a picture. One slot, one set, whichever name the model reaches for.
const RenderDetachIdentity = "render"

// PreflightTool is an optional companion to DetachableTool: the part of the
// call that can be checked WITHOUT doing the work. Implement it for anything
// whose arguments can be wrong in a way the model could fix on the spot.
//
// Detaching moves every error the call can raise out of the turn. A bad source
// reference, a backend that can't do what was asked, a missing prompt — all of
// them used to come back as an immediate tool error the model corrected in its
// next round. Detached, the same mistake instead returns "started, will report
// back", the agent tells the user it's running, and the failure surfaces a
// minute later in a wake with no turn left to fix it in. The model then has to
// explain a failure it can't see the cause of, and it invents one.
//
// So the checkable part runs BEFORE the detach: an error here is returned to
// the model inline, exactly as it would have been without detaching. Only work
// that genuinely needs the time goes to the background.
//
// It is asked only on the detach path — an inline call raises its own errors
// at the right moment already — so the check must be cheap and side-effect
// free. A nil return means "nothing I can rule out from here."
type PreflightTool interface {
	ChatTool
	Preflight(args map[string]any, sess *ToolSession) error
}

// TaskRun is a detached call the framework is tracking.
type TaskRun struct {
	ID     string // handle the model can name to ask about or cancel it
	Label  string // human/model-readable summary, e.g. "editing image#1"
	Detach time.Duration
}

// TaskProduct is everything a detached call produced: the text the model reads,
// and the attachments the framework has to deliver on its behalf.
//
// The attachments are the whole reason this is a struct and not a string. An
// inline tool call leaves its picture in the turn's session and the reply it
// rides out on collects it; a detached call has no such reply — the turn that
// started it ended minutes ago. Whatever it attached has to travel WITH the
// result to the wake, or it is produced, stored, announced, and never sent.
type TaskProduct struct {
	Text   string
	Images []string // base64, in the order the tool attached them
	Videos []string
	Files  []FileAttachment

	// Continuation is what the delivering turn should do NEXT — the seam that
	// lets a tool run a declared series one piece per turn (see task_series.go).
	// Unlike Text it is an instruction for that one turn, so the host must keep
	// it out of the thread's record. Empty for the ordinary one-and-done call.
	Continuation string
}

// TaskRunnerFunc starts fn in the background and returns a handle. The host
// owns the lifecycle: registering the run so it appears in the live surface
// under the turn that spawned it, wiring cancellation, and delivering the
// result — text AND attachments — into the conversation when fn returns.
//
// A function VARIABLE because the run registry and the conversation live in the
// app layer, which imports core rather than the other way round. Same seam as
// media.GovernedUploadFunc.
//
// Nil means detaching is unavailable and every call stays inline — the exact
// behaviour before this existed, so a host that doesn't wire it is no worse off.
// fn receives the TASK's context, not the turn's. Everything the detached call
// touches must hang off that instead — see ForDetachedTask.
var TaskRunnerFunc func(sess *ToolSession, label string, fn func(ctx context.Context) (TaskProduct, error)) (TaskRun, error)

// taskDetachThreshold is the duration past which a call is detached, for the
// surface this turn is happening on. See ToolSession.Unattended.
func taskDetachThreshold(sess *ToolSession) time.Duration {
	if sess.Unattended() {
		if d := TuneDuration("tune_task_detach_threshold_unattended"); d > 0 {
			return d
		}
	}
	return TuneDuration("tune_task_detach_threshold")
}

// ShouldDetach reports whether this call should run detached, and how long it
// is expected to take. All three conditions have to hold: a host that can run
// tasks, a tool that estimates its own duration, and an estimate past the
// threshold.
func ShouldDetach(ct ChatTool, args map[string]any, sess *ToolSession) (time.Duration, bool) {
	if TaskRunnerFunc == nil || sess == nil {
		return 0, false
	}
	d, ok := ct.(DetachableTool)
	if !ok {
		return 0, false
	}
	expected := d.ExpectedDuration(args, sess)
	// A tool that says this work is background work outranks the clock. See
	// AlwaysDetachTool — the estimate can be twenty seconds and the answer
	// still be yes, because the cost being avoided is a blocked conversation,
	// not a long one.
	if a, ok := ct.(AlwaysDetachTool); ok && a.AlwaysDetach(args, sess) {
		return expected, true
	}
	if expected <= 0 {
		return 0, false
	}
	return expected, expected >= taskDetachThreshold(sess)
}

// frameworkResultMark tags a tool result the FRAMEWORK wrote rather than one the
// tool returned — today, the notice a detached call hands back in place of its
// result. It is stripped before the model ever sees it (safeInvoke, and the
// app's own wrapper), so it exists only for the length of one return.
//
// It buys one thing: a result carrying it is not wrapped in the untrusted-
// content fence. That fence tells the model to treat everything below it as
// data and to obey no instruction inside it — correct for a fetched page, and
// exactly wrong for the framework's own "do NOT claim this finished, do NOT
// call this tool again". Fencing our own control text teaches the model to
// discount the instructions we most need followed.
//
// Randomized per process, because the alternative — recognizing the notice by
// its opening words — is forgeable: a fetched page that begins with the right
// sentence would slip its payload past the fence.
var frameworkResultMark = "\x00gohort-framework:" + UUIDv4() + "\x00"

func markFrameworkResult(s string) string { return frameworkResultMark + s }

// TakeFrameworkResultMark strips the mark and reports whether it was there.
// Callers that fence untrusted tool output check this first: a marked result is
// the framework speaking, not the outside world.
func TakeFrameworkResultMark(s string) (string, bool) {
	if strings.HasPrefix(s, frameworkResultMark) {
		return strings.TrimPrefix(s, frameworkResultMark), true
	}
	return s, false
}

// detachedNotice is what the model gets back INSTEAD of the result. It has to
// do two jobs the ordinary result never had to: stop it claiming the work is
// finished, and stop it re-running the call because nothing came back.
//
// The delivery contract is the part that goes wrong. A tool that returned an
// image taught the model to say "here it is"; this one must teach it to say "I
// have started it" — and a model that gets that backwards either promises an
// image that isn't there or silently redoes a fifteen-minute render.
func detachedNotice(run TaskRun, typical time.Duration) string {
	var b strings.Builder
	b.WriteString("STARTED, NOT FINISHED. It is running in the background as task " + run.ID)
	if l := strings.TrimSpace(run.Label); l != "" {
		b.WriteString(" (" + l + ")")
	}
	b.WriteString(".\n")
	b.WriteString("There is NO result yet and nothing has been delivered. Do NOT describe the outcome, do NOT claim anything was sent, and do NOT call this tool again for the same request — a second call starts a second job.\n")
	// Closing the OTHER route out. Told only not to re-call the tool, a model
	// goes looking for the result by hand — observed: workspace(ls) one round
	// after a detach, reasoning "let me check the workspace for the result from
	// the earlier successful edit". Nothing is there to find, so the round is
	// spent to learn nothing, and what it does find is older files it can then
	// mistake for this one.
	b.WriteString("It is NOT in your workspace and will not be until it finishes, so do not go looking for it there or anywhere else — listing files, searching, or trying a different route to the same thing all find nothing or find something older.\n")
	b.WriteString("Say you are doing it and that you will report back, in one line, the way a person would: \"I'll get that going and let you know when it's done.\" ")
	// The estimate is only ever a MEASURED one. It used to be the deadline —
	// the point at which the framework gives up — so a render that finishes in
	// forty seconds was announced as "about 15 minutes", and the user is then
	// holding a promise nothing intended to make.
	if typical > 0 {
		fmt.Fprintf(&b, "This usually takes about %s; say so if it is worth knowing.\n", humanizeTaskDuration(typical))
	} else {
		b.WriteString("Put NO time on it — nothing here knows how long it will take, and a number you invent is one they will hold you to.\n")
	}
	b.WriteString("Do NOT explain that you are running in the background, do NOT tell them they can keep talking to you, and do NOT invite them to check on it — that is machinery, they did not ask about it, and the live indicator already shows the work.\n")
	b.WriteString("The result arrives on its own as a new message when it is done; you will be told then, and that is when you deliver it. Until then answer whatever they say next as normal.")
	return b.String()
}

// secondDetachNotice answers a tool that tries to start a second background job
// in a turn that already has one running.
//
// Returned as a RESULT, not an error, and that is load-bearing twice over. An
// error would read as "that failed, adjust and retry" — the exact conclusion
// that produced the second call. It would also feed the give-up-with-errors
// guard, which counts unaddressed tool errors and pushes the model to try
// again; the framework would be nudging the behaviour it just blocked.
//
// So it says the same thing the original notice said, in the one place the
// model cannot skim past: the answer to its call.
// of is the size of the set this refused call now belongs to, 0 when it is not
// part of one. It changes what the notice is FOR: without a set the answer is
// "you already did this"; with one it is "you are early, and the rest is
// booked" — which is the true statement, and the only one that does not read
// as a failure the model should work around.
func secondDetachNotice(tool string, prior TaskRun, of int) string {
	var b strings.Builder
	b.WriteString("NOT STARTED — you already have one of these running this turn")
	if id := strings.TrimSpace(prior.ID); id != "" {
		b.WriteString(" (task " + id)
		if l := strings.TrimSpace(prior.Label); l != "" {
			b.WriteString(", " + l)
		}
		b.WriteString(")")
	}
	b.WriteString(".\n")
	b.WriteString("Nothing was wrong with this call. It was not run because a second background job delivers a SECOND result to the user, minutes later, as its own message — for one thing they asked for once.\n")
	b.WriteString("The first job is still working. It has not failed, and getting nothing back yet is not a sign that it did. Do NOT call " + tool + " again this turn, and do NOT go looking for another way to do the same thing.\n")
	// The refusal used to end the matter, and for a model that genuinely meant
	// to make several that was the wrong answer to the wrong question: it was
	// not repeating itself, it was working through a set. So say what has
	// actually been arranged on its behalf. See core/task_series.go.
	if of > 1 {
		fmt.Fprintf(&b, "You are not being told no — you are being told EARLY. This is now a set of %d: the one already running is the first, and when it lands you will be told to start the next, and so on until the set is done. Nothing is lost by waiting, and you do not need to remember the count.\n", of)
	} else {
		b.WriteString("If you MEANT to make several, they still happen — one at a time, not all at once. Say how many you intend on the FIRST call (the count parameter, where the tool has one) and you will be told to start the next as each finishes.\n")
	}
	// The wording matters, and this notice used to get it wrong in a way that
	// showed up verbatim in front of a user. It explained the situation with the
	// phrase "that is what running in the background means" and then asked for a
	// one-line reply — so the model wrote "The edit from earlier is still running
	// in the background", which is precisely what detachedNotice bans as
	// machinery nobody asked about. A notice that hands over the vocabulary it
	// does not want repeated is the one at fault, not the model.
	b.WriteString("Finish your turn now: say you are on it and will report back, in one line, the way a person would. Do NOT mention jobs, queues, or anything about HOW the work is being carried out, do NOT invite them to check on it, and do NOT put a time on it — that is machinery, they did not ask about it, and the live indicator already shows the work.\n")
	b.WriteString("The result arrives on its own when it is done, and that is when you deliver it. If they genuinely want another one after that, start it then.")
	return b.String()
}

// humanizeTaskDuration renders an estimate the way a person would say it, so
// the model repeats something natural rather than "900s".
func humanizeTaskDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "a moment"
	case d < 90*time.Second:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	case d < 90*time.Minute:
		return fmt.Sprintf("%d minutes", int(d.Minutes()+0.5))
	default:
		return fmt.Sprintf("%.1f hours", d.Hours())
	}
}

// taskLabelFor names a detached run for the live surface and the wake message.
// It reads the tool's own arguments rather than taking a label parameter: every
// tool already describes its work in an argument the model wrote, and asking
// for a second, label-shaped restatement is a field authors forget.
func taskLabelFor(ct ChatTool, args map[string]any) string {
	name := ct.Name()
	if a := strings.TrimSpace(StringArg(args, "action")); a != "" {
		name += " " + a
	}
	for _, key := range []string{"prompt", "query", "message", "url"} {
		if v := strings.TrimSpace(StringArg(args, key)); v != "" {
			return name + ": " + truncateTaskLabel(v, 60)
		}
	}
	return name
}

func truncateTaskLabel(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}

// errNoTaskHost is what a host returns when it cannot start a task — no chat
// session to deliver into, most often. The caller runs inline instead.
var errNoTaskHost = fmt.Errorf("no host available to run a detached task")

// ForDetachedTask returns a session for work that OUTLIVES the turn that
// started it.
//
// The turn's session cannot be reused as-is. Its Ctx is the turn's context and
// the turn cancels it on the way out, so a detached call reading sess.Context()
// — which the governed dispatch and the image poll loop both now do, so that
// Stop reaches them — dies the instant the turn ends. It cannot be mutated in
// place either: the turn keeps using the same session for its remaining rounds.
//
// What carries over is identity and authority: who this is, what they may
// reach, where their files live. What does NOT carry over is anything wired to
// a surface that is about to disappear — the status callback writes to an SSE
// stream that will be closed, and the approval/connect prompts need a person
// watching a turn that has ended. Those are left nil, which every one of them
// documents as "fall back to an error", rather than firing into a closed
// writer.
//
// Output accumulators start empty rather than shared. A detached call appending
// to the turn's Images would race the turn's own flush and deliver into a reply
// that has already been sent; its real delivery is the wake message.
func (s *ToolSession) ForDetachedTask(ctx context.Context) *ToolSession {
	if s == nil {
		return &ToolSession{Ctx: ctx}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return &ToolSession{
		Ctx:      ctx,
		Detached: true,

		// Identity + storage.
		Username:          s.Username,
		AgentID:           s.AgentID,
		ChatSessionID:     s.ChatSessionID,
		DeliverySessionID: s.DeliverySessionID,
		DB:                s.DB,
		WorkspaceDir:      s.WorkspaceDir,
		WorkspaceID:       s.WorkspaceID,
		// The read fallback travels too. Without it a detached call resolves
		// paths against its agent's directory ALONE, so a file at the user's
		// own root — reachable from every inline turn — is missing only when
		// the work runs in the background. "Works inline, fails detached" is
		// the worst shape a bug can take here: the inline path is the one
		// anybody tests.
		WorkspaceFallback: s.WorkspaceFallback,

		// Authority. Network in particular: a detached call must stay inside
		// the same egress gate as the turn that spawned it, and the connector
		// is shared deliberately so a Private toggle still reaches it.
		Network:               s.Network,
		DeniedCredentials:     s.DeniedCredentials,
		CanScopeGlobal:        s.CanScopeGlobal,
		DispatchParentAgentID: s.DispatchParentAgentID,
		AuthoringAgentFn:      s.AuthoringAgentFn,

		// What the work needs to run.
		LLM:                s.LLM,
		LeadLLM:            s.LeadLLM,
		SubAgentRunner:     s.SubAgentRunner,
		TempTools:          s.TempTools,
		BundledToolNames:   s.BundledToolNames,
		UnbundleTool:       s.UnbundleTool,
		BundleTool:         s.BundleTool,
		IntentText:         s.IntentText,
		RoutingTarget:      s.RoutingTarget,
		availableTools:     s.availableTools,
		imageBackends:      s.imageBackends,
		imageBackendsSet:   s.imageBackendsSet,
		InboundMedia:       s.InboundMedia,
		ReplyAuthorizedKey: s.ReplyAuthorizedKey,
	}
}
