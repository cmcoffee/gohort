package orchestrate

// The pill's whole job is to say "the machine did something while you were
// reading". It can only do that if it can tell that apart from your own turn
// running — which is decided at the ROOT of a run tree, not per run.

import "testing"

func TestSubAgentsInheritTheirRootsKind(t *testing.T) {
	// A chat turn that dispatches a sub-agent, which dispatches another. All
	// three are foreground: you are sitting there waiting on the lot.
	snaps := []RunSnapshot{
		{ID: "root", Kind: "chat"},
		{ID: "kid", Kind: "dispatch", ParentID: "root"},
		{ID: "grandkid", Kind: "dispatch", ParentID: "kid"},
	}
	bg := markBackground(snaps)
	for _, id := range []string{"root", "kid", "grandkid"} {
		if bg[id] {
			t.Errorf("%s belongs to a chat turn and is not background", id)
		}
	}

	// The same dispatch under a scheduled fire IS background — nobody asked
	// for it, which is the only question this answers.
	snaps = []RunSnapshot{
		{ID: "cron", Kind: "scheduled"},
		{ID: "kid", Kind: "dispatch", ParentID: "cron"},
	}
	bg = markBackground(snaps)
	if !bg["cron"] || !bg["kid"] {
		t.Errorf("a scheduled fire and its children are background, got %v", bg)
	}
}

func TestUnknownKindsAreBackground(t *testing.T) {
	// The direction where being wrong is visible. A kind added later that
	// defaulted to foreground would stop announcing itself — silently, and on
	// the one surface built to announce things.
	bg := markBackground([]RunSnapshot{{ID: "x", Kind: "some_new_surface"}})
	if !bg["x"] {
		t.Error("an unrecognized kind must announce itself rather than hide")
	}
	if !isBackgroundKind("channel") || !isBackgroundKind("standing") || !isBackgroundKind("task") {
		t.Error("channel, standing and task all start without anyone watching")
	}
	if isBackgroundKind("chat") {
		t.Error("chat is the one kind a person started on purpose")
	}
}

func TestAnOrphanedRunIsJudgedOnItsOwnKind(t *testing.T) {
	// The parent completed and was swept while the child still runs. The walk
	// stops where it can see, rather than guessing at the vanished parent.
	bg := markBackground([]RunSnapshot{{ID: "kid", Kind: "dispatch", ParentID: "gone"}})
	if !bg["kid"] {
		t.Error("with no visible parent, a dispatch is judged as a dispatch")
	}
}

func TestParentCycleTerminates(t *testing.T) {
	// Nothing here can assume the links are acyclic. A hang would freeze the
	// poll that feeds the pill, which is worse than any wrong color.
	done := make(chan map[string]bool, 1)
	go func() {
		done <- markBackground([]RunSnapshot{
			{ID: "a", Kind: "chat", ParentID: "b"},
			{ID: "b", Kind: "chat", ParentID: "a"},
		})
	}()
	select {
	case <-done:
	default:
		// markBackground is synchronous and fast; if the goroutine hasn't
		// finished by the time we look, give it one blocking chance and let
		// the test framework's own timeout catch a real hang.
		<-done
	}
}

func TestFocusingOneRunTakesItsChildrenAndNothingElse(t *testing.T) {
	snaps := []RunSnapshot{
		{ID: "other", Kind: "chat"},
		{ID: "cron", Kind: "scheduled"},
		{ID: "kid", Kind: "dispatch", ParentID: "cron"},
		{ID: "grandkid", Kind: "dispatch", ParentID: "kid"},
		{ID: "unrelated", Kind: "standing"},
	}
	got := descendantsOf(snaps, "cron")
	if len(got) != 3 {
		t.Fatalf("a focused view is the run and its descendants, got %d: %+v", len(got), got)
	}
	for _, s := range got {
		if s.ID == "other" || s.ID == "unrelated" {
			t.Errorf("%s is not part of that task", s.ID)
		}
	}
	// Tree order survives, so the focused table reads the way the full one did.
	if got[0].ID != "cron" || got[1].ID != "kid" || got[2].ID != "grandkid" {
		t.Errorf("order should be parent then children, got %+v", got)
	}
	// An id naming nothing returns NOTHING. Falling back to everything would
	// show a viewer the whole deployment under a URL claiming one task.
	if n := len(descendantsOf(snaps, "swept-long-ago")); n != 0 {
		t.Errorf("a stale link shows nothing, got %d rows", n)
	}
	// No filter is still no filter.
	if n := len(descendantsOf(snaps, "")); n != len(snaps) {
		t.Errorf("an empty filter passes everything, got %d", n)
	}
}
