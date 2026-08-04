// One background job per tool per turn.
//
// Observed: an image edit detached in round 3, the model got back "STARTED, NOT
// FINISHED", saw no picture, called the tool again in round 4 and again in round
// 6 — and the user received three images, minutes apart, for one request they
// made once. The detach notice already said "do NOT call this tool again for the
// same request — a second call starts a second job", in those words. Nothing was
// enforcing it.
package core

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOnlyOneBackgroundJobPerToolPerTurn(t *testing.T) {
	sess := &ToolSession{Username: "craig"}

	if _, free := sess.ClaimDetachSlot("image"); !free {
		t.Fatal("the first call must get the slot")
	}
	sess.RecordDetachSlot("image", TaskRun{ID: "task-1", Label: "editing image#1"})

	prior, free := sess.ClaimDetachSlot("image")
	if free {
		t.Fatal("a second detach in the same turn must be refused")
	}
	if prior.ID != "task-1" {
		t.Errorf("the refusal must name the job holding the slot, got %+v", prior)
	}

	// Per tool, not per turn: a different capability is a different delivery,
	// and one has nothing to say about the other.
	if _, free := sess.ClaimDetachSlot("video"); !free {
		t.Error("a different tool has its own slot")
	}
}

func TestParallelCallsInOneRoundCannotBothClaim(t *testing.T) {
	// A round can dispatch several calls at once, in their own goroutines. Two
	// renders started together are two separate deliveries just as surely as two
	// started a round apart, so the claim has to be atomic — the gap between
	// taking the slot and knowing the run id is where a race would slip through.
	sess := &ToolSession{Username: "craig"}
	const callers = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, free := sess.ClaimDetachSlot("image"); free {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if won != 1 {
		t.Errorf("%d callers claimed the slot, want exactly 1", won)
	}
}

func TestAFailedDetachGivesTheSlotBack(t *testing.T) {
	// The run never started, so nothing is holding it. Keeping the slot would
	// refuse the next call over a job that does not exist.
	sess := &ToolSession{Username: "craig"}
	if _, free := sess.ClaimDetachSlot("image"); !free {
		t.Fatal("first claim")
	}
	sess.ReleaseDetachSlot("image")
	if _, free := sess.ClaimDetachSlot("image"); !free {
		t.Error("a released slot must be claimable again")
	}
}

func TestASlotIsPerTurnNotPerSession(t *testing.T) {
	// The hole this closes: the ledger used to live on the SESSION, and a plan
	// runs each step on its own session (runWorkerStep mints one per step). So a
	// three-step plan got three allowances and the user got three deliveries for
	// one request — the cap counting the wrong thing entirely.
	turn := NewDetachLedger()
	step1 := &ToolSession{Username: "craig", Detach: turn}
	step2 := &ToolSession{Username: "craig", Detach: turn}
	step3 := &ToolSession{Username: "craig", Detach: turn}

	if _, free := step1.ClaimDetachSlot("image"); !free {
		t.Fatal("the first step must get the slot")
	}
	step1.RecordDetachSlot("image", TaskRun{ID: "task-1"})
	for i, later := range []*ToolSession{step2, step3} {
		prior, free := later.ClaimDetachSlot("image")
		if free {
			t.Errorf("step %d started a second job for one request", i+2)
		}
		if prior.ID != "task-1" {
			t.Errorf("step %d must be told which job holds the slot, got %+v", i+2, prior)
		}
	}

	// A NEW turn starts clean. If this leaked, an agent that rendered something
	// once could never render again.
	next := &ToolSession{Username: "craig", Detach: NewDetachLedger()}
	if _, free := next.ClaimDetachSlot("image"); !free {
		t.Error("a new turn must start with its slot free")
	}
	// A session nobody shared a ledger with mints its own, so a standalone
	// caller behaves exactly as it did before any of this existed.
	lone := &ToolSession{Username: "craig"}
	if _, free := lone.ClaimDetachSlot("image"); !free {
		t.Error("a lone session must still get a slot")
	}
	if _, free := lone.ClaimDetachSlot("image"); free {
		t.Error("and must still be capped by it")
	}
	// And a nil session keeps the behaviour every caller had before this existed.
	if _, free := (*ToolSession)(nil).ClaimDetachSlot("image"); !free {
		t.Error("a nil session claims nothing and blocks nothing")
	}
}

func TestTheRefusalDoesNotReadAsAFailure(t *testing.T) {
	notice := secondDetachNotice("image", TaskRun{ID: "task-1", Label: "editing image#1"})

	// It has to name the job, or "you already have one running" is unfalsifiable
	// from where the model sits.
	if !strings.Contains(notice, "task-1") || !strings.Contains(notice, "editing image#1") {
		t.Errorf("the notice must name the running job:\n%s", notice)
	}
	// The dangerous reading is "that failed, try something else" — which is the
	// exact conclusion that produced the second call.
	if !strings.Contains(notice, "Nothing was wrong with this call") {
		t.Errorf("the notice must say the call was not the problem:\n%s", notice)
	}
	// And it has to close the obvious workaround, because the model reaching for
	// a different route to the same picture is the same second delivery.
	if !strings.Contains(notice, "another way to do the same thing") {
		t.Errorf("the notice must rule out routing around it:\n%s", notice)
	}
	// Finally: what to do instead. A refusal with no next move is where turns
	// stall on a promise.
	if !strings.Contains(notice, "report back") {
		t.Errorf("the notice must say how to end the turn:\n%s", notice)
	}
}

func TestTheSecondCallOfATurnStartsNoSecondJob(t *testing.T) {
	// End to end through the wrapper, which is where the turn actually lives:
	// same session, same tool, two calls a round apart. The first detaches; the
	// second must come back with the refusal and leave the runner alone.
	started := withTaskRunner(t)
	tool := &slowTool{dur: taskDetachThreshold() + time.Hour, result: "rendered"}
	sess := &ToolSession{Username: "craig"}
	def := ChatToolToAgentToolDefWithSession(tool, sess)

	first, err := def.Handler(map[string]any{"prompt": "blend Rory onto the garage photo"})
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if !strings.Contains(first, "STARTED, NOT FINISHED") {
		t.Fatalf("the first call must detach:\n%s", first)
	}

	// The model saw no picture and tried a different phrasing — the exact shape
	// from the log, where the args differed every time, so anything keyed on
	// duplicate ARGUMENTS would have waved all of these through.
	second, err := def.Handler(map[string]any{"prompt": "put Rory on the hood instead"})
	if err != nil {
		// Deliberately not an error: an error reads as "adjust and retry", and
		// it would feed the give-up-with-errors guard, which pushes exactly the
		// retry this is refusing.
		t.Fatalf("the refusal must not be an error: %v", err)
	}
	if !strings.Contains(second, "NOT STARTED") {
		t.Errorf("the second call must be refused:\n%s", second)
	}
	if len(*started) != 1 {
		t.Errorf("runner started %d jobs, want 1 — the user asked once", len(*started))
	}
}

func TestNeitherNoticeHandsOverTheVocabularyItBans(t *testing.T) {
	// Observed in front of a user: "The edit from earlier is still running in
	// the background — same request, will deliver the result as soon as it's
	// done." detachedNotice bans exactly that as machinery nobody asked about.
	// secondDetachNotice explained the situation with "that is what running in
	// the background means" and then asked for a one-line reply, so the model
	// wrote the phrase back. A notice that supplies the words it does not want
	// repeated is the one at fault.
	second := secondDetachNotice("image", TaskRun{ID: "task-1", Label: "editing image#1"})
	if strings.Contains(strings.ToLower(second), "running in the background") {
		t.Errorf("the refusal must not hand over the phrase it wants suppressed:\n%s", second)
	}
	// And it has to carry the ban itself — the model reads THIS notice, not the
	// one from the round before.
	for _, must := range []string{"machinery", "do NOT put a time on it"} {
		if !strings.Contains(second, must) {
			t.Errorf("the refusal must repeat the no-machinery rule (%q):\n%s", must, second)
		}
	}
}

func TestADetachedCallClosesTheWorkspaceRouteToo(t *testing.T) {
	// Told only not to re-call the tool, a model goes looking by hand: observed
	// as workspace(ls) one round after a detach, reasoning "let me check the
	// workspace for the result from the earlier successful edit". Nothing is
	// there, so the round buys nothing — and the older files it does find are
	// exactly what it can mistake for this one.
	notice := detachedNotice(TaskRun{ID: "task-1", Label: "editing image#1"}, 0)
	if !strings.Contains(notice, "NOT in your workspace") {
		t.Errorf("the notice must say the result is not on disk yet:\n%s", notice)
	}
	if !strings.Contains(notice, "do not go looking") {
		t.Errorf("the notice must close the hunt, not just the re-call:\n%s", notice)
	}
	// The rule it already had stays.
	if !strings.Contains(notice, "a second call starts a second job") {
		t.Errorf("the original ban must survive:\n%s", notice)
	}
}
