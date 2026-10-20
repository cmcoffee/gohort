package orchestrate

// An approved request comes back to the conversation it was asked from, as a
// background task of that conversation. Seen live: a channel agent sent
// another agent, which queued a hand-off for approval; the owner approved it,
// it ran, and nothing came back. The wake went to the agent that ASKED, the
// sent one, and so into a thread of that agent's under the right session id
// that nobody was reading. And approving a queued change only granted it: the
// run that wanted it had ended, so it was never made.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// sentAgentSession is a session of agent "music", sent by "wiwee" from the
// conversation sid: a dispatch run under wiwee's turn, delivering into sid.
func sentAgentSession(t *testing.T, app *OrchestrateApp, sid string) *ToolSession {
	t.Helper()
	rr := app.runsRegistry()
	top := rr.Create("u", "wiwee", sid, func() {}).Describe("session", "WiWee", "make me a song")
	sub := rr.Create("u", "music", "", func() {}).Describe("dispatch", "Music", "make the song").Parent(top.ID)
	t.Cleanup(func() { sub.Complete(RunStatusCompleted); top.Complete(RunStatusCompleted) })
	return &ToolSession{Username: "u", ChatSessionID: "dispatch:" + sid + ":music", DeliverySessionID: sid,
		AgentID: "music", Ctx: withParentRun(context.Background(), sub.ID)}
}

func TestAWakeGoesToTheAgentWhoseThreadItIs(t *testing.T) {
	depStores(t, "u")
	sess := sentAgentSession(t, orchRef, "room-"+UUIDv4())
	if id, name := orchRef.runsRegistry().threadAgent(sess); id != "wiwee" || name != "WiWee" {
		t.Errorf("a sent agent's work should come home as the thread's agent, got %q %q", id, name)
	}
	// A session that is its own conversation is its own agent's.
	own := &ToolSession{Username: "u", ChatSessionID: sess.ChatSessionID, AgentID: "music", Ctx: sess.Ctx}
	if id, _ := orchRef.runsRegistry().threadAgent(own); id != "music" {
		t.Errorf("a session delivering into itself is the nearest run's agent, got %q", id)
	}
}

func TestAHandOffASentAgentQueuedComesBackToTheThread(t *testing.T) {
	root := depStores(t, "u")
	app := orchRef
	stubDelegationRunner(t, app)
	sid := "room-" + UUIDv4()
	sess := sentAgentSession(t, app, sid)

	a := Authorization{Owner: "u", Agent: "helper", Brief: "find the news", FromAgent: "music"}
	stampRequestOrigin(&a, sess)
	if a.FromSession != sid || a.FromThreadAgent != "wiwee" || a.FromAgent != "music" {
		t.Fatalf("the request should record the thread and whose it is, and keep the asker: %+v", a)
	}

	q := registerInjectionQueue(sid, "u", "wiwee")
	app.runApprovedDelegation(a, "helper")
	var task *RunSnapshot
	for _, s := range app.runsRegistry().Activity("u") {
		if s.Kind == "task" {
			s := s
			task = &s
		}
	}
	if task == nil || task.AgentID != "wiwee" || task.AgentName != "helper" {
		t.Fatalf("the approved run should show as a background task of the thread's agent: %+v", task)
	}
	waitBackgroundJobs(t, sid)
	releaseInjectionQueue(sid)
	if notes := q.Drain(); len(notes) != 1 || !strings.Contains(notes[0].Text, "FULL RESULT") {
		t.Fatalf("the conversation should be woken with the result: %+v", notes)
	}

	// A no reaches the same thread.
	app.noteDeniedDelegation(a)
	s, ok := loadChatSession(UserDB(root, "u"), "wiwee", sid)
	if !ok || len(s.Messages) == 0 || !strings.Contains(s.Messages[len(s.Messages)-1].Content, "DENIED") {
		t.Errorf("the denial should be in the thread's agent's conversation: %+v", s)
	}
}

func TestAnApprovedChangeIsMadeAndComesBack(t *testing.T) {
	root := depStores(t, "u")
	app := orchRef
	registerStandingRunner(app)
	var ran []StandingAgent
	var under []string
	RegisterStandingRunner(func(ctx context.Context, sa StandingAgent) StandingRunResult {
		ran = append(ran, sa)
		under = append(under, parentRunFromCtx(ctx))
		return StandingRunResult{Status: RunOK, Summary: "done", Raw: "CHANGED: the calendar tool now does all three."}
	})
	t.Cleanup(func() { RegisterStandingRunner(nil); RegisterStandingReporter(nil) })
	sid := "room-" + UUIDv4()
	sess := sentAgentSession(t, app, sid)
	sess.IntentText = "fold the calendar tools into one"
	udb := UserDB(root, "u")

	// Builder, sent by wiwee, with nobody watching: queued, and told it comes back.
	c := changeAsker{owner: "u", agentID: "seed-builder", agentName: "Builder", session: sess.ChatSessionID,
		db: udb, sentBy: "wiwee", sess: sess}
	msg := c.approve("tool", "calendar", "Replace its definition.", ChangeEdit)
	if !strings.Contains(msg, "approval queue") || !strings.Contains(msg, "comes back to this conversation") {
		t.Fatalf("the refusal should say approval brings the result back: %q", msg)
	}
	var a Authorization
	for _, q := range ListAuthorizations(RootDB, "u") {
		if q.Action == changeApprovalAction {
			a = q
		}
	}
	if a.Text != "fold the calendar tools into one" || a.FromSession != sid {
		t.Fatalf("the queued change should keep the work it was part of and where it was asked: %+v", a)
	}

	q := registerInjectionQueue(sid, "u", "wiwee")
	r := httptest.NewRequest(http.MethodPost, "/api/approvals/approve?id="+a.ID, nil)
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(AuthDB(), "u")})
	w := httptest.NewRecorder()
	app.resolveApproval(w, r, false)
	if w.Code != http.StatusNoContent || !changeGranted(udb, a.Agent, a.Handle) {
		t.Fatalf("approve answered %d %s, granted=%v", w.Code, w.Body.String(), changeGranted(udb, a.Agent, a.Handle))
	}
	waitBackgroundJobs(t, sid)
	releaseInjectionQueue(sid)
	if len(ran) != 1 || ran[0].AgentID != "seed-builder" || !strings.Contains(ran[0].Mission, "calendar") ||
		!strings.Contains(ran[0].Mission, "fold the calendar tools into one") {
		t.Fatalf("approving should run the agent that wanted the change, told what it is and what it was for: %+v", ran)
	}
	if len(ran[0].DispatchedBy) != 1 || ran[0].DispatchedBy[0] != "wiwee" {
		t.Errorf("the run should be wiwee's hand-off, as the original was: %v", ran[0].DispatchedBy)
	}
	if notes := q.Drain(); len(notes) != 1 || !strings.Contains(notes[0].Text, "CHANGED") {
		t.Fatalf("the conversation should be woken with what the change did: %+v", notes)
	}
	if r := app.runsRegistry().Get(under[0]); r == nil || r.Snapshot().Kind != "task" || r.Snapshot().AgentID != "wiwee" {
		t.Errorf("what the approved run does should nest under its task row, ran under %q", under[0])
	}

	// An agent asking for its own change is not handed over by itself, and a
	// change with nowhere to come back to only grants, as before.
	ran = nil
	self := a
	self.FromAgent = "seed-builder"
	self.FromThreadAgent = ""
	self.FromSession = "room-" + UUIDv4()
	app.runApprovedChange(self)
	waitBackgroundJobs(t, self.FromSession)
	if len(ran) != 1 || len(ran[0].DispatchedBy) != 0 {
		t.Errorf("an agent's own change ran as a hand-off from itself: %+v", ran)
	}
	ran = nil
	self.FromSession = ""
	app.runApprovedChange(self)
	if len(ran) != 0 {
		t.Errorf("a change with no conversation to come back to ran: %+v", ran)
	}
}
