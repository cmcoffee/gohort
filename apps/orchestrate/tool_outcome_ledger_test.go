// Noticing that a tool action has never once worked.
//
// The verify gate proves a tool works at authoring time and stops watching, so
// a definition that is wrong in a way the author's single test didn't reach
// goes on failing forever. Observed: one toolbox action was called 987 times
// over two days and failed 371; 260 of those were a single action that had
// never returned a result. The model kept calling it because nothing told it
// not to, and the user never found out because nothing told them either.
package orchestrate

import (
	"errors"
	"strings"
	"sync"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func outcomeSession(t *testing.T) *ToolSession {
	t.Helper()
	return &ToolSession{Username: "craig", AgentID: "wiwee", DB: &DBase{Store: kvlite.MemStore()}}
}

func TestAnActionIsCalledBrokenOnlyAfterItNeverWorks(t *testing.T) {
	sess := outcomeSession(t)
	boom := errors.New(`action "get_feed" requires param(s) "cursor"`)

	for i := 1; i < neverWorkedThreshold; i++ {
		if advice := recordToolOutcome(sess, "moltbook", "get_feed", boom); advice != "" {
			t.Fatalf("failure %d must not yet call the tool broken: %s", i, advice)
		}
	}
	advice := recordToolOutcome(sess, "moltbook", "get_feed", boom)
	if advice == "" {
		t.Fatal("the threshold failure must hand the model an advisory")
	}
	if !strings.Contains(advice, "STOP RETRYING") || !strings.Contains(advice, "get_feed") {
		t.Errorf("advisory must name the action and say to stop:\n%s", advice)
	}
	// The one distinction the model cannot make on its own, and the reason it
	// kept guessing: its call is not what's wrong.
	if !strings.Contains(advice, "not a mistake in your call") {
		t.Errorf("advisory must place the fault in the definition:\n%s", advice)
	}

	broken := brokenToolActions(sess.DB, sess.Username)
	if len(broken) != 1 || broken[0].key() != "moltbook.get_feed" {
		t.Fatalf("broken list = %v, want the one action", broken)
	}
	if broken[0].LastError == "" {
		t.Error("the record must carry what keeps going wrong, so a human need not go to the log")
	}
}

func TestTheModelIsToldEveryTimeAndTheHumanOnlyOnce(t *testing.T) {
	// A person needs telling once. A model reads only the error in front of it
	// and has no memory of the nineteen before, so it needs telling every time.
	sess := outcomeSession(t)
	boom := errors.New("nope")
	for i := 0; i < neverWorkedThreshold; i++ {
		recordToolOutcome(sess, "moltbook", "get_feed", boom)
	}
	for i := 0; i < 3; i++ {
		if advice := recordToolOutcome(sess, "moltbook", "get_feed", boom); advice == "" {
			t.Fatalf("call %d after the threshold must still advise the model", i)
		}
	}
	// Flagged is what keeps the log and the ⚠ from repeating.
	if !brokenToolActions(sess.DB, sess.Username)[0].Flagged {
		t.Error("the human-facing warning must be marked as already given")
	}
}

func TestOneSuccessRetiresTheVerdict(t *testing.T) {
	// The definition got fixed, or the call finally found the shape it wanted.
	// Either way the action is not broken any more and must stop saying it is —
	// the same shape as the failure-streak collapse retiring its residue.
	sess := outcomeSession(t)
	for i := 0; i < neverWorkedThreshold+2; i++ {
		recordToolOutcome(sess, "moltbook", "get_feed", errors.New("nope"))
	}
	if len(brokenToolActions(sess.DB, sess.Username)) != 1 {
		t.Fatal("precondition: the action is broken")
	}
	recordToolOutcome(sess, "moltbook", "get_feed", nil)
	if got := brokenToolActions(sess.DB, sess.Username); len(got) != 0 {
		t.Errorf("a success must retire the verdict, still broken: %v", got)
	}
	if advice := recordToolOutcome(sess, "moltbook", "get_feed", errors.New("nope")); advice != "" {
		t.Errorf("the count restarts after a success, got advice:\n%s", advice)
	}
}

func TestActionsAreCountedApart(t *testing.T) {
	// "Has this ever worked" is a question about one action. A toolbox with a
	// broken feed and a working post is not a broken toolbox.
	sess := outcomeSession(t)
	for i := 0; i < neverWorkedThreshold; i++ {
		recordToolOutcome(sess, "moltbook", "get_feed", errors.New("nope"))
		recordToolOutcome(sess, "moltbook", "create_post", nil)
	}
	broken := brokenToolActions(sess.DB, sess.Username)
	if len(broken) != 1 || broken[0].Action != "get_feed" {
		t.Errorf("broken = %v, want only get_feed", broken)
	}
}

func TestParallelFailuresAllCount(t *testing.T) {
	// A round dispatches its tool calls in parallel goroutines. Without the
	// lock, two failures landing at once read the same tally and the second
	// writes back a stale count — and a counter whose whole job is to reach a
	// threshold then never reaches it.
	sess := outcomeSession(t)
	var wg sync.WaitGroup
	for i := 0; i < neverWorkedThreshold*4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recordToolOutcome(sess, "moltbook", "get_feed", errors.New("nope"))
		}()
	}
	wg.Wait()
	got := brokenToolActions(sess.DB, sess.Username)
	if len(got) != 1 {
		t.Fatalf("broken = %v, want the action flagged", got)
	}
	if got[0].Fail != neverWorkedThreshold*4 {
		t.Errorf("counted %d failures, want %d — a lost update means the threshold may never arrive", got[0].Fail, neverWorkedThreshold*4)
	}
}
