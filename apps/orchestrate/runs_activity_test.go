package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

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
