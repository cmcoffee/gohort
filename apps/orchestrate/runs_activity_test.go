package orchestrate

import (
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
