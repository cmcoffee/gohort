package orchestrate

// A delegation the owner approves runs the way it would have run had it been
// pre-authorized: under the asking conversation's privacy, and with its result
// delivered back into that conversation so the agent that asked picks it up.
//
// It used to run on a bare context and report into the TARGET agent's own home
// thread. Two things went wrong at once. The asking agent never heard back, so
// it went on telling the user the work was still waiting for approval after it
// had run, and queued it again. And a conversation running Private, whose
// pre-authorized delegations had their network tools removed, had its approved
// ones search the web: approving the work quietly lifted the privacy it was
// asked under.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// delegationCapture collects an approved delegation's full report instead of
// letting the standing reporter post it into the target's thread. The ledger
// record RunDelegation returns carries only a short summary; the reporter is
// the one place the whole output passes through.
type delegationCapture struct {
	raw string
}

type delegationCaptureKey struct{}

func withDelegationCapture(ctx context.Context) (context.Context, *delegationCapture) {
	c := &delegationCapture{}
	return context.WithValue(ctx, delegationCaptureKey{}, c), c
}

// capturedDelegation reports whether this run's report is being delivered
// elsewhere, recording the output when it is.
func capturedDelegation(ctx context.Context, raw string) bool {
	c, ok := ctx.Value(delegationCaptureKey{}).(*delegationCapture)
	if !ok || c == nil {
		return false
	}
	c.raw = raw
	return true
}

// privateDelegationNote tells the asking agent, in the delegate tool's result,
// that the delegation runs (or ran) without network tools because this
// conversation is Private. Without it the agent offers delegation as a way to
// reach the internet, and the delegate flails for its whole round budget
// looking for tools it was never given.
func privateDelegationNote(ctx context.Context, ran bool) string {
	if NetworkAllowedFromContext(ctx) {
		return ""
	}
	if ran {
		return " This conversation is Private, so it ran with its network tools removed and could not reach the internet."
	}
	return " This conversation is Private, so it will run with its network tools removed: it cannot reach the internet either."
}

// stampRequestOrigin records on a queued request the conversation that asked,
// so the approved run reports back there, and that conversation's privacy, so
// the run keeps it. Returns the asking turn's context with its network state,
// for a note that has to say whether the run will be Private.
//
// Shared by every request that waits on the owner's approval and then runs an
// agent: delegate and request_build both queued the same way, and only one of
// them remembered where it came from, which is how an approved build finished
// with nobody told.
func stampRequestOrigin(a *Authorization, sess *ToolSession) context.Context {
	if sess == nil {
		return context.Background()
	}
	qctx := sess.ContextWithNetworkConnector(sess.Context())
	a.FromSession = sess.DeliverySession()
	a.FromChatID = strings.TrimSpace(sess.ChannelChatID)
	a.FromHandle = strings.TrimSpace(sess.ChannelHandle)
	a.FromPrivate = !NetworkAllowedFromContext(qctx)
	if id, _ := liveRunRegistry().threadAgent(sess); id != "" && id != a.FromAgent {
		a.FromThreadAgent = id
	}
	return qctx
}

// liveRunRegistry is the running app's run registry, for code that records a
// request with no app in hand. Empty (never nil) before the app is up.
func liveRunRegistry() *RunRegistry {
	orchRefMu.Lock()
	o := orchRef
	orchRefMu.Unlock()
	if o == nil {
		return NewRunRegistry()
	}
	return o.runsRegistry()
}

// approvedOrigin is where a queued request's result goes: the conversation it
// was asked from, woken as the agent whose thread that is.
func approvedOrigin(a Authorization) taskOrigin {
	return taskOrigin{
		SessionID: a.FromSession, User: a.Owner, AgentID: chFirst(a.FromThreadAgent, a.FromAgent),
		ChatID: a.FromChatID, Handle: a.FromHandle,
	}
}

// requestOriginNote tells the asking agent where the result of its queued
// request will turn up.
func requestOriginNote(a Authorization) string {
	if a.FromSession == "" {
		return "Its result lands in the run ledger, not here."
	}
	return "Its result comes back to you here once it runs."
}

// runApprovedDelegation runs an approved delegation to target and, when the
// request recorded where it came from, wakes that conversation with the result.
func (T *OrchestrateApp) runApprovedDelegation(a Authorization, target string) {
	// Builder's request was approved with its brief in front of the owner:
	// that is their say-so for the edits it needs, which an unwatched run
	// would otherwise queue one by one and drop (withOwnerApprovedRun).
	ownerApproved := ""
	if udb := UserDB(T.DB, a.Owner); udb != nil {
		if rec, ok := findAgentByNameOrID(udb, a.Owner, target); ok && isBuilderAgent(rec.ID) {
			ownerApproved = rec.ID
		}
	}
	name := T.delegationTargetName(a.Owner, target)
	T.runApproved(approvedRun{
		a: a, target: target, brief: a.Brief, ownerApproved: ownerApproved,
		label: fmt.Sprintf("the delegation to %s you queued for the user's approval, which they approved (%s)", name, truncateObs(a.Brief, 160)),
	})
}

// approvedRun is one run of an agent the owner approved from the queue.
type approvedRun struct {
	a             Authorization // the request, and where it was asked from
	target        string        // the agent that runs
	brief         string        // what it is asked to do
	label         string        // how the wake names it to the agent that asked
	ownerApproved string        // the agent whose edits the approval covers (withOwnerApprovedRun); "" for none
}

// runApproved runs ar. With a conversation to come back to, it is a background
// task of that conversation, the way a long tool call is: a live row with a
// Stop, nested work under it, and the conversation woken with the result. It
// used to run with nothing on screen, so an approved hand-off looked like one
// that never went out, and its wake went to the agent that asked, which for an
// agent another one sent is not the agent whose thread it was.
func (T *OrchestrateApp) runApproved(ar approvedRun) {
	a := ar.a
	prep := func(ctx context.Context) context.Context {
		if a.FromPrivate {
			ctx = WithNetworkConnector(ctx, NewNetworkConnector(true))
		}
		if ar.ownerApproved != "" {
			ctx = withOwnerApprovedRun(ctx, ar.ownerApproved)
		}
		return ctx
	}
	// Who handed it over, unless that is the agent running: an agent asking
	// for its own change is not on its own call path twice.
	var via []string
	if from := strings.TrimSpace(a.FromAgent); from != "" && from != ar.target {
		via = []string{from}
	}
	if strings.TrimSpace(a.FromSession) == "" || strings.TrimSpace(a.FromAgent) == "" {
		RunDelegation(prep(context.Background()), RootDB, a.Owner, ar.target, ar.brief, via...)
		return
	}
	name := T.delegationTargetName(a.Owner, ar.target)
	origin := approvedOrigin(a)
	T.startTask(backgroundTask{
		origin:    origin,
		agentName: name,
		title:     truncateObs(ar.brief, 100),
		label:     ar.label,
	}, func(ctx context.Context) (TaskProduct, error) {
		ctx, capture := withDelegationCapture(prep(ctx))
		returned := &ToolSession{} // what the run attaches, delivered with its result
		ctx = withMediaReturn(ctx, returned)
		rec := RunDelegation(ctx, RootDB, a.Owner, ar.target, ar.brief, via...)
		out := strings.TrimSpace(capture.raw)
		if out == "" {
			out = strings.TrimSpace(rec.Summary)
		}
		var runErr error
		if rec.Status == RunFailed {
			msg := strings.TrimSpace(rec.Err)
			if msg == "" {
				msg = "the run failed"
			}
			runErr = errors.New(msg)
		}
		if a.FromPrivate && out != "" {
			out += "\n(It ran Private, with its network tools removed, because the conversation that asked is Private.)"
		}
		// What it created goes to the folder of the agent the result wakes,
		// where the turn that delivers it runs (agent_media.go).
		home := &ToolSession{WorkspaceDir: familyWorkspace(UserDB(T.DB, a.Owner), a.Owner, origin.AgentID)}
		out += handBackFiles(name, madeFiles(returned), home)
		return TaskProduct{Text: out, Images: returned.Images, Videos: returned.Videos, Files: returned.Files}, runErr
	})
}

// delegationTargetName is the name to call a delegation's target by in the
// wake note, falling back to what the request said.
func (T *OrchestrateApp) delegationTargetName(owner, target string) string {
	if udb := UserDB(T.DB, owner); udb != nil {
		if rec, ok := findAgentByNameOrID(udb, owner, target); ok && strings.TrimSpace(rec.Name) != "" {
			return rec.Name
		}
	}
	return target
}

// noteDeniedDelegation tells the agent that asked for a delegation that the
// owner said no. Not a new turn: the owner just clicked Deny and knows, and an
// agent answering a click is noise. A live turn takes it between rounds; with
// none, it is kept in the thread as a hidden note the next turn reads, so the
// agent stops describing the request as pending and does not queue it again.
func (T *OrchestrateApp) noteDeniedDelegation(a Authorization) {
	sid, agentID := strings.TrimSpace(a.FromSession), approvedOrigin(a).AgentID
	if sid == "" || strings.TrimSpace(agentID) == "" {
		return
	}
	note := fmt.Sprintf("%sThe user DENIED the delegation to %s you queued for approval (%s). It did not run and nothing came back. Do not queue it again unless they ask for it; if the work still matters to them, ask how they want to go about it.",
		frameworkNoteTag, T.delegationTargetName(a.Owner, a.Agent), truncateObs(a.Brief, 160))
	if q := lookupInjectionQueue(sid); q != nil && q.Owner == a.Owner {
		q.PushFramework(note, "")
		return
	}
	udb := UserDB(T.DB, a.Owner)
	if udb == nil {
		return
	}
	if err := appendToStoredSession(udb, agentID, sid, ChatSession{ID: sid, AgentID: agentID},
		ChatMessage{Role: "user", Content: note, Created: time.Now(), Hidden: true}); err != nil {
		Log("[operator.approval] could not record a denied delegation in session %s: %v", sid, err)
	}
}
