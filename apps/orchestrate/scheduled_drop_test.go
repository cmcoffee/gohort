package orchestrate

import (
	"strings"
	"testing"

	core "github.com/cmcoffee/gohort/core"
)

// A schedule that dies before its agent runs used to Log and return, which is
// invisible from the only surface anyone opens. Combined with FireCount
// incrementing at PRE-ARM, an owner could see twelve fires and zero runs and
// have no way to tell "it ran and did nothing" from "it never got started".
func TestADroppedFireLeavesALedgerRow(t *testing.T) {
	db := pinRootDB(t)
	p := orchUpdatePayload{
		Username:  "craig",
		AgentID:   "agent-123",
		SessionID: "sess-1",
		Name:      "Snuglab blog post",
		Prompt:    "post today's snuglab entry",
	}

	recordScheduledDrop(p, core.RunFailed, "Did not run: its agent (id agent-123) no longer exists.")

	runs := core.ListRuns(db, "craig", core.RunFilter{})
	if len(runs) != 1 {
		t.Fatalf("expected the drop to be recorded, got %d runs", len(runs))
	}
	r := runs[0]
	if r.Status != core.RunFailed {
		t.Errorf("status: got %q", r.Status)
	}
	// Findable by the name the owner gave it — the whole point of Task.
	if r.Task != "Snuglab blog post" {
		t.Errorf("the row must name the task: %q", r.Task)
	}
	if !strings.Contains(r.Summary, "no longer exists") {
		t.Errorf("the row must say why it did not run: %q", r.Summary)
	}
	// And reachable by that name through the filter the tools use.
	if got := core.ListRuns(db, "craig", core.RunFilter{Task: "Snuglab blog post"}); len(got) != 1 {
		t.Errorf("a dropped fire must be findable by task name, got %d", len(got))
	}
}

// The agent may be the very thing that went missing, so the label falls back to
// its id rather than leaving the row anonymous.
func TestADroppedFireIdentifiesItselfWithoutTheAgent(t *testing.T) {
	db := pinRootDB(t)
	recordScheduledDrop(orchUpdatePayload{
		Username: "craig", AgentID: "deleted-agent", SessionID: "s", Name: "nightly digest",
	}, core.RunAttention, "Auto-cancelled: reached its cap of 50 fires.")

	runs := core.ListRuns(db, "craig", core.RunFilter{})
	if len(runs) != 1 {
		t.Fatalf("expected one run, got %d", len(runs))
	}
	if !strings.Contains(runs[0].Agent, "deleted-agent") {
		t.Errorf("label should fall back to the agent id, got %q", runs[0].Agent)
	}
	if runs[0].Subject != "agent:deleted-agent" {
		t.Errorf("identity should still be the agent id, got %q", runs[0].Subject)
	}
}

// The ledger is keyed by owner. A payload with no owner cannot be filed, and
// must not panic trying — it is the one drop that stays log-only.
func TestADropWithNoOwnerIsNotRecorded(t *testing.T) {
	db := pinRootDB(t)
	recordScheduledDrop(orchUpdatePayload{AgentID: "a", SessionID: "s"}, core.RunFailed, "nope")
	if got := core.ListRuns(db, "", core.RunFilter{}); len(got) != 0 {
		t.Errorf("an ownerless drop has nowhere to go, got %d rows", len(got))
	}
}

// An unnamed task still identifies itself: recurringName falls back to the
// first line of the prompt, which is what the owner would recognize.
func TestAnUnnamedTaskStillNamesItself(t *testing.T) {
	db := pinRootDB(t)
	recordScheduledDrop(orchUpdatePayload{
		Username: "craig", AgentID: "a", SessionID: "s",
		Prompt: "check the deploy queue\nand report anything stuck",
	}, core.RunAttention, "Auto-cancelled after 90 idle days.")

	runs := core.ListRuns(db, "craig", core.RunFilter{})
	if len(runs) != 1 || runs[0].Task == "" {
		t.Fatalf("an unnamed task still needs a label: %+v", runs)
	}
	if !strings.Contains(runs[0].Task, "check the deploy queue") {
		t.Errorf("fallback should be the prompt's first line, got %q", runs[0].Task)
	}
}
