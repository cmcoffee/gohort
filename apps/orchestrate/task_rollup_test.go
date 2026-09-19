package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func seedGoalAgent(t *testing.T, db Database, owner, name, parent, until string, rollUp, met bool) {
	t.Helper()
	sa := StandingAgent{Owner: owner, Name: name, AgentID: "a1", Parent: parent, Until: until, RollUp: rollUp}
	if met {
		sa.Attempts = appendObjectiveAttempt(nil, true, "done")
		sa.Paused, sa.StopCause, sa.StopNote = true, StoppedByMet, "done"
	}
	SaveStandingAgent(db, sa)
}

// Rollup is opt-in because a parent is one of two different things. A REAL
// CHECK is not met because its pieces are, and finishing it on their evidence
// declares something true that nobody verified.
func TestRollupDoesNotFireUnlessAsked(t *testing.T) {
	root := pinRootDB(t)
	seedGoalAgent(t, root, "craig", "verify", "", "the newsletter went out", false, false)
	seedGoalAgent(t, root, "craig", "gather", taskParentRef(schedKindStanding, "verify"), "links collected", false, true)

	rollUpFrom("craig", schedKindStanding, "gather")
	parent, _ := GetStandingAgent(root, "craig", "verify")
	if parent.Paused {
		t.Error("a parent with its own check was finished on its children's evidence")
	}
}

// And with it on, the last child finishing finishes the parent, in the same
// stopped state any other met objective produces.
func TestTheLastChildFinishesTheParent(t *testing.T) {
	root := pinRootDB(t)
	ref := taskParentRef(schedKindStanding, "launch")
	seedGoalAgent(t, root, "craig", "launch", "", "", true, false)
	seedGoalAgent(t, root, "craig", "draft", ref, "drafted", false, true)
	seedGoalAgent(t, root, "craig", "send", ref, "sent", false, false)

	// One still running: nothing happens, and the reason NAMES it.
	rollUpFrom("craig", schedKindStanding, "draft")
	if parent, _ := GetStandingAgent(root, "craig", "launch"); parent.Paused {
		t.Fatal("the parent finished while a child was still running")
	}
	if got := rollUpStateLabel("craig", schedKindStanding, "launch"); !strings.Contains(got, "send") {
		t.Errorf("the row does not say what it is waiting for: %q", got)
	}

	// Now the other one finishes.
	seedGoalAgent(t, root, "craig", "send", ref, "sent", false, true)
	rollUpFrom("craig", schedKindStanding, "send")
	parent, _ := GetStandingAgent(root, "craig", "launch")
	if !parent.Paused || parent.StopCause != StoppedByMet {
		t.Fatalf("the parent did not finish: paused=%v cause=%q", parent.Paused, parent.StopCause)
	}
	if !strings.Contains(parent.StopNote, "rolled up") {
		t.Errorf("the stop does not say how it finished: %q", parent.StopNote)
	}
	if !objectiveMet(parent.Attempts) {
		t.Error("a rolled-up finish left no attempt saying it was met")
	}
}

// A child that can never report finishing BLOCKS its parent, named, rather than
// being skipped. Skipping it would mark work finished while it is still
// running, which is the failure nobody goes looking for.
func TestAChildWithNoCheckBlocksTheParent(t *testing.T) {
	root := pinRootDB(t)
	ref := taskParentRef(schedKindStanding, "launch")
	seedGoalAgent(t, root, "craig", "launch", "", "", true, false)
	seedGoalAgent(t, root, "craig", "done-piece", ref, "finished", false, true)
	seedGoalAgent(t, root, "craig", "forever", ref, "", false, false) // no completion check

	rollUpFrom("craig", schedKindStanding, "done-piece")
	if parent, _ := GetStandingAgent(root, "craig", "launch"); parent.Paused {
		t.Fatal("a parent finished over a child that can never say it is done")
	}
	got := rollUpStateLabel("craig", schedKindStanding, "launch")
	if !strings.Contains(got, "forever") || !strings.Contains(got, "cannot report") {
		t.Errorf("the reason does not name the child holding it up: %q", got)
	}
}

// "Everything under it is done" is vacuously true of nothing, and a heading
// that finishes the moment it is created is the most confusing possible
// behaviour.
func TestAParentWithNoChildrenDoesNotFinish(t *testing.T) {
	root := pinRootDB(t)
	seedGoalAgent(t, root, "craig", "empty", "", "", true, false)
	if done, why := rollUpVerdict(rollUpChildren("craig", taskParentRef(schedKindStanding, "empty"))); done {
		t.Errorf("a childless parent rolled up: %q", why)
	}
}

// Finishing one parent can be the last thing ITS parent was waiting for, so the
// walk continues up. It stops at the first one that is not ready, since nothing
// above that can be either.
func TestRollupClimbs(t *testing.T) {
	root := pinRootDB(t)
	top := taskParentRef(schedKindStanding, "top")
	mid := taskParentRef(schedKindStanding, "mid")
	seedGoalAgent(t, root, "craig", "top", "", "", true, false)
	seedGoalAgent(t, root, "craig", "mid", top, "", true, false)
	seedGoalAgent(t, root, "craig", "leaf", mid, "leaf done", false, true)

	rollUpFrom("craig", schedKindStanding, "leaf")
	for _, name := range []string{"mid", "top"} {
		rec, _ := GetStandingAgent(root, "craig", name)
		if !rec.Paused {
			t.Errorf("%s did not finish when everything under it had", name)
		}
	}
}

// A stalled child is not a finished one. A parent does not get to complete
// because a piece of it gave up.
func TestAStalledChildDoesNotFinishTheParent(t *testing.T) {
	root := pinRootDB(t)
	ref := taskParentRef(schedKindStanding, "launch")
	seedGoalAgent(t, root, "craig", "launch", "", "", true, false)
	stalled := StandingAgent{
		Owner: "craig", Name: "gave-up", AgentID: "a1", Parent: ref, Until: "never happens",
		Attempts: appendObjectiveAttempt(nil, false, "out of attempts"),
	}
	SaveStandingAgent(root, stalled)

	rollUpFrom("craig", schedKindStanding, "gave-up")
	if parent, _ := GetStandingAgent(root, "craig", "launch"); parent.Paused {
		t.Error("a parent finished because its child stopped trying")
	}
}
