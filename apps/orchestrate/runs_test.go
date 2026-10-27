package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The cancel endpoint looked a turn up by the session id alone, which comes
// from the request, so any signed-in user could stop another user's turn by
// naming its id. It is looked up under (user, id) now.
func TestCancelOnlyReachesOwnTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inflightCancels.Store(runSessKey("user1", "sess-x"), context.CancelFunc(cancel))
	defer inflightCancels.Delete(runSessKey("user1", "sess-x"))

	h := routed(&OrchestrateApp{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}})
	h(httptest.NewRecorder(), as(t, httptest.NewRequest(http.MethodPost, "/api/cancel?id=sess-x", nil), "user2"))
	if ctx.Err() != nil {
		t.Fatal("another user's cancel stopped the turn")
	}
	h(httptest.NewRecorder(), as(t, httptest.NewRequest(http.MethodPost, "/api/cancel?id=sess-x", nil), "user1"))
	if ctx.Err() == nil {
		t.Fatal("the owner's cancel did not stop the turn")
	}
}

// "A fresh send cancels the old run" was keyed by session id too, so another
// user's send on the same id cancelled the turn, and the running flag and
// stream resume reported the wrong user's run.
func TestRunRegistrySessionsArePerUser(t *testing.T) {
	rr := NewRunRegistry()
	ctx1, run1 := rr.CreateCancellable(context.Background(), "user1", "", "sess-x")
	_, run2 := rr.CreateCancellable(context.Background(), "user2", "", "sess-x")
	time.Sleep(20 * time.Millisecond) // the replace cancels asynchronously
	if ctx1.Err() != nil {
		t.Fatal("another user's run on the same session id cancelled this one")
	}
	if got := rr.BySession("user1", "sess-x"); got != run1 {
		t.Fatalf("user1's session resolved to %v, want its own run", got)
	}
	if got := rr.BySession("user2", "sess-x"); got != run2 {
		t.Fatalf("user2's session resolved to %v, want its own run", got)
	}
	run1.Cancel()
	run2.Cancel()
}

// The turn's context has to reach the session, or two features that look wired
// are quietly off.

func TestASessionWithoutTheTurnContextCannotDetach(t *testing.T) {
	// What the logs showed, four times: "[task] image stayed inline: no owning
	// agent to deliver as". A tool finds the run that owns it by reading the
	// parent-run tag off sess.Context(); a session built before the tag was
	// applied — and never given the context at all — falls back to
	// context.Background(), where there is no tag and no owner. Detaching then
	// fails every time and a fifteen-minute render holds the turn open.
	untagged := &ToolSession{}
	if got := parentRunFromCtx(untagged.Context()); got != "" {
		t.Fatalf("a session with no context should carry no parent run, got %q", got)
	}

	tagged := &ToolSession{Ctx: withParentRun(context.Background(), "run_42")}
	if got := parentRunFromCtx(tagged.Context()); got != "run_42" {
		t.Errorf("parent run = %q, want run_42 — this is what the detach path reads", got)
	}
}

func TestCancellationOnlyReachesASessionHoldingTheTurnContext(t *testing.T) {
	// The same omission with the other consequence: the image poll loop selects
	// on sess.Context(), so a background context means Stop never lands.
	ctx, cancel := context.WithCancel(context.Background())
	sess := &ToolSession{Ctx: ctx}
	cancel()
	if sess.Context().Err() == nil {
		t.Error("cancelling the turn must be visible through the session")
	}
	if (&ToolSession{}).Context().Err() != nil {
		t.Error("a session with no context should not look cancelled")
	}
}

// A card is drawn early in a run and updated in place, so a long run pushes
// its frame out of the event ring. A page that rejoined the run (reopened on a
// phone, mid-build) got every frame after that and never drew the card:
// Builder's plan came back as a blank bar. The ring now keeps each card's
// latest frame and hands back the ones it has dropped, first.

func runFrame(t *testing.T, payload map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return append(append([]byte("data: "), b...), '\n', '\n')
}

func TestARejoinedRunStillDrawsItsCards(t *testing.T) {
	r := NewRunRegistry().Create("u", "a", "s", nil)
	r.Append(runFrame(t, map[string]any{"kind": "block", "type": "orchestrate_plan", "id": "plan-1", "plan": []string{"step one"}}))
	r.Append(runFrame(t, map[string]any{"kind": "block", "type": "orchestrate_plan", "id": "plan-1", "plan": []string{"step one", "step two"}}))
	r.Append(runFrame(t, map[string]any{"kind": "block", "type": "orchestrate_intent", "id": "intent-1", "text": "looking"}))
	// A build's worth of streaming: far more than the ring holds.
	for i := 0; i < runMaxEvents()+200; i++ {
		r.Append(runFrame(t, map[string]any{"kind": "chunk", "id": "m1", "text": "x"}))
	}
	sub := r.Subscribe(0)
	defer r.Unsubscribe(sub)
	if len(sub.Backlog) < 2 {
		t.Fatalf("backlog has %d frames", len(sub.Backlog))
	}
	first, second := string(sub.Backlog[0].Frame), string(sub.Backlog[1].Frame)
	if !strings.Contains(first, `"id":"plan-1"`) || !strings.Contains(first, "step two") {
		t.Errorf("the plan's latest frame does not lead the rejoin: %s", first)
	}
	if !strings.Contains(second, `"id":"intent-1"`) {
		t.Errorf("the second card was not handed back: %s", second)
	}
	plans := 0
	for _, ev := range sub.Backlog {
		if strings.Contains(string(ev.Frame), `"id":"plan-1"`) {
			plans++
		}
	}
	if plans != 1 {
		t.Errorf("the plan came back %d times; only its latest frame belongs in a rejoin", plans)
	}

	// A page that already has everything up to the end gets no cards again.
	if again := r.Subscribe(sub.Backlog[len(sub.Backlog)-1].Seq); len(again.Backlog) != 0 {
		t.Errorf("a caught-up page was handed %d frames", len(again.Backlog))
		r.Unsubscribe(again)
	}
}

// TestRunRegistryActivity pins the live-activity view: user-scoped, running
// runs first (oldest first), then completed newest-first, with the Describe /
// SetProgress metadata riding the snapshot.
func TestRunRegistryActivity(t *testing.T) {
	rr := NewRunRegistry()

	sched := rr.Create("u", "agent-1", "", nil).Describe("scheduled", "Moltbook", "Run your standing task now.")
	sched.SetProgress(3, []ToolCall{{Name: "get_feed"}, {Name: "reply_to_post"}})
	rr.Create("u", "agent-2", "sess-1", nil).Describe("chat", "Gohort", "what's up")
	other := rr.Create("someone-else", "agent-9", "", nil).Describe("standing", "Theirs", "")
	doneRun := rr.Create("u", "agent-3", "", nil).Describe("standing", "Daily Laughs", "morning meme")
	doneRun.Complete(RunStatusCompleted)

	rows := rr.Activity("u")
	if len(rows) != 3 {
		t.Fatalf("want 3 rows for user u (other user's run excluded), got %d", len(rows))
	}
	// Running first, oldest first: sched was created before chat.
	if rows[0].AgentName != "Moltbook" || rows[0].Status != RunStatusRunning {
		t.Fatalf("row 0 should be the oldest running run; got %+v", rows[0])
	}
	if rows[0].Round != 3 || rows[0].LastTool != "reply_to_post" {
		t.Fatalf("progress should ride the snapshot; got round=%d tool=%q", rows[0].Round, rows[0].LastTool)
	}
	if rows[1].AgentName != "Gohort" || rows[1].Kind != "chat" {
		t.Fatalf("row 1 should be the chat run; got %+v", rows[1])
	}
	if rows[2].Status != RunStatusCompleted || rows[2].AgentName != "Daily Laughs" {
		t.Fatalf("completed run should trail; got %+v", rows[2])
	}
	_ = other

	// Cancel via the registry handle is what the console kill switch uses;
	// on a run with no cancel func it must not panic and the run stays
	// running until the loop actually exits.
	sched.Cancel()
	if sched.Status() != RunStatusRunning {
		t.Fatal("Cancel without a cancel func must not force-complete the run")
	}
}

// TestOrderRunsByTree pins the nested-activity ordering: a run is placed
// directly after its parent and one level deeper, recursively; a run whose
// parent isn't in the set is a root at depth 0.
func TestOrderRunsByTree(t *testing.T) {
	// root -> child -> grandchild, plus a second root and an orphan.
	snaps := []RunSnapshot{
		{ID: "gc", ParentID: "child"},
		{ID: "root2"},
		{ID: "child", ParentID: "root"},
		{ID: "root"},
		{ID: "orphan", ParentID: "gone"}, // parent not present -> root at depth 0
	}
	got := orderRunsByTree(snaps)
	depth := map[string]int{}
	pos := map[string]int{}
	for i, s := range got {
		depth[s.ID] = s.Depth
		pos[s.ID] = i
	}
	if len(got) != len(snaps) {
		t.Fatalf("tree order must preserve every run; got %d of %d", len(got), len(snaps))
	}
	if depth["root"] != 0 || depth["child"] != 1 || depth["gc"] != 2 {
		t.Fatalf("depths wrong: root=%d child=%d gc=%d", depth["root"], depth["child"], depth["gc"])
	}
	if depth["orphan"] != 0 {
		t.Fatalf("an orphan (missing parent) must be a depth-0 root; got %d", depth["orphan"])
	}
	// Child follows its root; grandchild follows the child.
	if !(pos["root"] < pos["child"] && pos["child"] < pos["gc"]) {
		t.Fatalf("child/grandchild must follow the parent in order: %v", got)
	}
}

// TestRunOutcomeStatus pins the fix for "marked failed when it didn't": a
// context cancellation (superseded turn / shutdown) is CANCELED, not FAILED;
// hitting the round cap (nil error, has a reply) is COMPLETED; a real error or
// no result is FAILED.
func TestRunOutcomeStatus(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		hasResp bool
		want    string
	}{
		{"clean success", nil, true, RunStatusCompleted},
		{"round cap (nil err, has reply)", nil, true, RunStatusCompleted},
		{"superseded", context.Canceled, false, RunStatusCanceled},
		{"deadline", context.DeadlineExceeded, true, RunStatusCanceled},
		{"real error", errors.New("llm exploded"), false, RunStatusFailed},
		{"no result, no error", nil, false, RunStatusFailed},
		{"wrapped cancel", fmt.Errorf("dispatch: %w", context.Canceled), false, RunStatusCanceled},
	}
	for _, c := range cases {
		if got := runOutcomeStatus(c.err, c.hasResp); got != c.want {
			t.Errorf("%s: runOutcomeStatus(%v, %v) = %q, want %q", c.name, c.err, c.hasResp, got, c.want)
		}
	}
}

// Cancel has to say whether it did anything, because for most of this
// product's life it did not and said nothing. Eleven of the twelve run-creation
// sites passed no cancel func, so the Monitor row's Cancel and the live pill's
// Stop appeared on scheduled fires, standing fires, dispatches, pipelines and
// machines, answered success, and left the work running.
func TestCancelSaysWhetherItDidAnything(t *testing.T) {
	rr := NewRunRegistry()

	deaf := rr.Create("u", "a", "", nil).Describe("scheduled", "Nightly", "digest")
	if deaf.Cancel() {
		t.Error("a run with no cancel func reported that it stopped something")
	}
	if deaf.Cancellable() || deaf.Snapshot().Cancellable {
		t.Error("a run with no cancel func must not advertise itself as cancellable")
	}
	if deaf.Status() != RunStatusRunning {
		t.Error("Cancel must not force-complete a run it could not reach")
	}

	stopped := false
	live := rr.Create("u", "a", "", func() { stopped = true })
	if !live.Cancellable() || !live.Snapshot().Cancellable {
		t.Error("a run with a cancel func should advertise it")
	}
	if !live.Cancel() {
		t.Error("cancelling a stoppable run reported that nothing happened")
	}
	if !stopped {
		t.Error("the cancel func was not called")
	}
}

// The whole point of CreateCancellable: the context it hands back is the one
// the work runs under, so pressing the button reaches the work.
func TestCreateCancellableStopsTheWorkItHandsBack(t *testing.T) {
	rr := NewRunRegistry()
	ctx, run := rr.CreateCancellable(context.Background(), "u", "a", "")
	if ctx.Err() != nil {
		t.Fatalf("the context arrived already cancelled: %v", ctx.Err())
	}
	if !run.Cancel() {
		t.Fatal("a run from CreateCancellable reported nothing to cancel")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("Cancel did not reach the context the work runs under")
	}
}

// The guard that keeps this from coming back at the thirteenth site. Every live
// run has to be stoppable: a row that offers Cancel and cannot deliver it is
// worse than one that never offered.
func TestNoRunIsCreatedWithoutAWayToStopIt(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("reading the package: %v", err)
	}
	call := regexp.MustCompile(`runsRegistry\(\)\.Create\(([^)]*)\)`)
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, rerr := os.ReadFile(f)
		if rerr != nil {
			t.Fatalf("reading %s: %v", f, rerr)
		}
		for _, m := range call.FindAllStringSubmatch(string(src), -1) {
			checked++
			args := strings.Split(m[1], ",")
			last := strings.TrimSpace(args[len(args)-1])
			if last == "nil" {
				t.Errorf("%s: %s passes no cancel func, so its Cancel button would do nothing. "+
					"Use runsRegistry().CreateCancellable(ctx, …) and run the work under the ctx it returns.", f, m[0])
			}
		}
	}
	if checked == 0 {
		t.Fatal("found no run-creation sites to check — did the call shape change?")
	}
}

// A synthetic session id shared by concurrent dispatches must not put them in
// the registry's one-run-per-session slot: with a real cancel behind it, a
// pipeline fanning several branches at the same agent would have each branch
// cancel the one before it.
func TestParallelDispatchesDoNotCancelEachOther(t *testing.T) {
	rr := NewRunRegistry()
	firstStopped := false
	ctx1, first := rr.CreateCancellable(context.Background(), "u", "agent-1", "")
	go func() { <-ctx1.Done(); firstStopped = true }()
	_, second := rr.CreateCancellable(context.Background(), "u", "agent-1", "")

	time.Sleep(20 * time.Millisecond)
	if firstStopped || ctx1.Err() != nil {
		t.Fatal("starting a second dispatch to the same agent cancelled the first")
	}
	if first.Status() != RunStatusRunning || second.Status() != RunStatusRunning {
		t.Fatal("both dispatches should still be running")
	}

	// A real conversation keeps the replace-the-previous rule, because there a
	// fresh send IS meant to abandon the turn before it.
	ctx3, _ := rr.CreateCancellable(context.Background(), "u", "agent-1", "sess-real")
	rr.CreateCancellable(context.Background(), "u", "agent-1", "sess-real")
	select {
	case <-ctx3.Done():
	case <-time.After(time.Second):
		t.Fatal("a new run on the same conversation did not replace the previous one")
	}
}
