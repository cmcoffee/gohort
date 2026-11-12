package orchestrate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func ownerJobs(owner string) []Authorization {
	var out []Authorization
	for _, a := range ListAuthorizations(RootDB, owner) {
		if a.Action == builderAsOwnerAction {
			out = append(out, a)
		}
	}
	return out
}

// A Builder job an agent sent that touches a tool the agent may not use is
// queued for the owner, once however many of its calls are refused, and shown
// in the chat it came from; one refused for an API the agent may not use is
// not; and a job the owner said no to is not put to them again.
func TestABuilderJobRefusedForAToolGoesToTheOwner(t *testing.T) {
	udb := delegationFixture(t)
	const job = "Make allowed_kit retry twice on a timeout, and say which host failed."
	shown := 0
	sess := builderSession(udb, "a-other")
	applyBuilderDelegation(sess, udb, ownerJob{brief: job, live: func(Authorization) { shown++ }})

	msg := sess.ToolEditRefusal("allowed_kit", "")
	if !strings.Contains(msg, "NOT DONE") || !strings.Contains(msg, "queued for the owner") {
		t.Fatalf("the refusal should say the job went to the owner: %q", msg)
	}
	again := sess.ToolEditRefusal("builder_scratch", "")
	queued := ownerJobs("alice")
	if len(queued) != 1 || shown != 1 || !strings.Contains(again, queued[0].ID) {
		t.Fatalf("one job should queue once and show once: %d queued, shown %d, second refusal %q", len(queued), shown, again)
	}
	if a := queued[0]; a.FromAgent != "a-other" || a.Brief != job || !strings.Contains(a.Text, "allowed_kit") {
		t.Errorf("the request should carry who asked, the whole job and the tool: %+v", a)
	}
	if !approvalBelongsToAgent(udb, queued[0], "a-other") {
		t.Error("the request should show in the chat of the agent that sent the job")
	}

	// An API the sender may not use stays a plain no.
	api := builderSession(udb, "a-other")
	applyBuilderDelegation(api, udb, ownerJob{brief: "Point forecast at the new endpoint."})
	if msg := api.ToolEditRefusal("forecast", ""); strings.Contains(msg, "queued") || !strings.Contains(msg, "NOT DONE") {
		t.Errorf("a tool refused for its API should not be queued: %q", msg)
	}
	if n := len(ownerJobs("alice")); n != 1 {
		t.Errorf("the API refusal queued a job: %d", n)
	}

	// The owner's no holds: the same job is not queued again.
	DeleteAuthorization(RootDB, "alice", queued[0].ID)
	recordProposalDecline(udb, "a-other", proposalKindBuilderJob, job, "", declineNo)
	next := builderSession(udb, "a-other")
	applyBuilderDelegation(next, udb, ownerJob{brief: job})
	if msg := next.ToolEditRefusal("allowed_kit", ""); !strings.Contains(msg, "already declined") {
		t.Errorf("a declined job should be refused as declined: %q", msg)
	}
	if n := len(ownerJobs("alice")); n != 0 {
		t.Errorf("a declined job was queued again: %d", n)
	}
}

// Approving runs the job in Builder as the owner, with no agent handing it
// over (which is what carries the agent's limits), and the result back in the
// conversation; denying keeps the no and tells that conversation.
func TestAnApprovedBuilderJobRunsAsTheOwner(t *testing.T) {
	root := depStores(t, "u")
	app := orchRef
	registerStandingRunner(app)
	var mu sync.Mutex
	var ran []StandingAgent
	RegisterStandingRunner(func(ctx context.Context, sa StandingAgent) StandingRunResult {
		mu.Lock()
		ran = append(ran, sa)
		mu.Unlock()
		return StandingRunResult{Status: RunOK, Summary: "done", Raw: "CHANGED: the tool retries twice now."}
	})
	t.Cleanup(func() { RegisterStandingRunner(nil); RegisterStandingReporter(nil) })
	sid := "room-" + UUIDv4()
	sess := sentAgentSession(t, app, sid)
	cookie := &http.Cookie{Name: "gohort_session", Value: AuthCreateSession(AuthDB(), "u")}
	post := func(path, id string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path+"?id="+id, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		routed(app)(w, r)
		if w.Code != http.StatusNoContent {
			t.Fatalf("%s answered %d %s", path, w.Code, w.Body.String())
		}
	}
	queue := func(brief string) Authorization {
		a := Authorization{Owner: "u", Action: builderAsOwnerAction, Agent: "builder", FromAgent: "music", Brief: brief}
		stampRequestOrigin(&a, sess)
		return SaveAuthorization(RootDB, a)
	}

	a := queue("Make the song tool retry twice.")
	q := registerInjectionQueue(sid, "u", "wiwee")
	post("/api/console/approvals/approve", a.ID)
	// Approving starts the run in the background, as an approved build does.
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		mu.Lock()
		n := len(ran)
		mu.Unlock()
		if n > 0 {
			break
		}
	}
	waitBackgroundJobs(t, sid)
	mu.Lock()
	defer mu.Unlock()
	releaseInjectionQueue(sid)
	if len(ran) != 1 || ran[0].AgentID != "builder" || !strings.Contains(ran[0].Mission, "retry twice") {
		t.Fatalf("approving should run Builder on the job: %+v", ran)
	}
	if len(ran[0].DispatchedBy) != 0 {
		t.Errorf("the job should run as the owner, not handed over by %v", ran[0].DispatchedBy)
	}
	if notes := q.Drain(); len(notes) != 1 || !strings.Contains(notes[0].Text, "CHANGED") {
		t.Fatalf("the conversation should be woken with the result: %+v", notes)
	}

	b := queue("Rename the song tool.")
	post("/api/console/approvals/deny", b.ID)
	if m := checkProposalDeclines(UserDB(root, "u"), "music", proposalKindBuilderJob, b.Brief); m.Same == nil {
		t.Error("the owner's no to the job should be kept")
	}
	s, ok := loadChatSession(UserDB(root, "u"), "wiwee", sid)
	if !ok || len(s.Messages) == 0 || !strings.Contains(s.Messages[len(s.Messages)-1].Content, "DENIED") {
		t.Errorf("the no should reach the conversation that asked: %+v", s.Messages)
	}
}
