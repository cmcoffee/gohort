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

// runApprovedDelegation runs an approved delegation to target and, when the
// request recorded where it came from, wakes that conversation with the result.
func (T *OrchestrateApp) runApprovedDelegation(a Authorization, target string) {
	ctx := context.Background()
	if a.FromPrivate {
		ctx = WithNetworkConnector(ctx, NewNetworkConnector(true))
	}
	origin := strings.TrimSpace(a.FromSession) != "" && strings.TrimSpace(a.FromAgent) != ""
	var capture *delegationCapture
	if origin {
		ctx, capture = withDelegationCapture(ctx)
	}
	rec := RunDelegation(ctx, RootDB, a.Owner, target, a.Brief, a.FromAgent)
	if !origin {
		return
	}
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
	label := fmt.Sprintf("the delegation to %s you queued for the user's approval, which they approved (%s)",
		T.delegationTargetName(a.Owner, target), truncateObs(a.Brief, 160))
	T.deliverTaskResult(taskOrigin{
		SessionID: a.FromSession, User: a.Owner, AgentID: a.FromAgent,
		ChatID: a.FromChatID, Handle: a.FromHandle,
	}, label, TaskProduct{Text: out}, runErr)
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
	sid, agentID := strings.TrimSpace(a.FromSession), strings.TrimSpace(a.FromAgent)
	if sid == "" || agentID == "" {
		return
	}
	note := fmt.Sprintf("%sThe user DENIED the delegation to %s you queued for approval (%s). It did not run and nothing came back. Do not queue it again unless they ask for it; if the work still matters to them, ask how they want to go about it.",
		frameworkNoteTag, T.delegationTargetName(a.Owner, a.Agent), truncateObs(a.Brief, 160))
	if q := lookupInjectionQueue(sid); q != nil && q.Owner == a.Owner {
		q.Push(note)
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
