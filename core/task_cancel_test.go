package core

// "Actually, forget the other three." Before this the agent could only
// apologize to that sentence: the pictures kept arriving, because what knew
// they were coming was a ledger it could not see and a run it could not name.

import (
	"strings"
	"testing"
)

func TestAnAgentCanSeeAndStopWhatItStarted(t *testing.T) {
	const sess = "sess-bg-1"
	t.Cleanup(func() { CancelBackgroundJobs(sess, "") })
	stoppedA, stoppedB := false, false
	RegisterBackgroundJob(sess, TaskRun{ID: "task-a", Label: "image: a red bicycle"}, func() { stoppedA = true })
	RegisterBackgroundJob(sess, TaskRun{ID: "task-b", Label: "image: a blue one"}, func() { stoppedB = true })
	// A set is running alongside them, and stopping the work must stop it too.
	AdvanceTaskSeries(sess, RenderDetachIdentity, 4)

	tool := &backgroundWorkTool{}
	s := &ToolSession{ChatSessionID: sess}

	out, err := tool.RunWithSession(map[string]any{"action": "list"}, s)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, want := range []string{"2 still running", "task-a", "a red bicycle"} {
		if !strings.Contains(out, want) {
			t.Errorf("list missing %q:\n%s", want, out)
		}
	}

	// One by id.
	if out, err = tool.RunWithSession(map[string]any{"action": "stop", "task": "task-a"}, s); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !stoppedA || stoppedB {
		t.Errorf("stopped the wrong one: a=%v b=%v", stoppedA, stoppedB)
	}
	if !strings.Contains(out, "Stopped 1") {
		t.Errorf("stop must say what it stopped:\n%s", out)
	}
	// Stopping ANY of it closes the set — leaving it open is how the next wake
	// starts the piece after the one just cancelled.
	if TaskSeriesOpen(sess, RenderDetachIdentity) {
		t.Error("a cancelled conversation must have no set still counting")
	}

	// The rest, by omitting the id — which is what "forget it" means.
	if _, err = tool.RunWithSession(map[string]any{"action": "stop"}, s); err != nil {
		t.Fatalf("stop all: %v", err)
	}
	if !stoppedB {
		t.Error("omitting the id must stop everything still running")
	}
	if len(ListBackgroundJobs(sess)) != 0 {
		t.Error("nothing should still be listed after stopping everything")
	}
}

// The model's next move on an ambiguous "stop that" is otherwise to claim it
// stopped something.
func TestStoppingNothingSaysSoPlainly(t *testing.T) {
	tool := &backgroundWorkTool{}
	s := &ToolSession{ChatSessionID: "sess-bg-empty"}
	for _, action := range []string{"list", "stop"} {
		out, err := tool.RunWithSession(map[string]any{"action": action}, s)
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if !strings.Contains(out, "Nothing") {
			t.Errorf("%s must say nothing is running:\n%s", action, out)
		}
	}
	out, _ := tool.RunWithSession(map[string]any{"action": "stop"}, s)
	if !strings.Contains(out, "Do not tell the user you cancelled something") {
		t.Errorf("an empty stop must not read as a successful cancel:\n%s", out)
	}
}

// A finished job must leave the list, or the agent is told to stop work that is
// already over.
func TestFinishedWorkLeavesTheList(t *testing.T) {
	const sess = "sess-bg-2"
	RegisterBackgroundJob(sess, TaskRun{ID: "task-x"}, nil)
	CompleteBackgroundJob(sess, "task-x")
	if len(ListBackgroundJobs(sess)) != 0 {
		t.Error("a delivered job must not still be listed as running")
	}
}

// Work is scoped to its conversation: stopping here must not stop there.
func TestStoppingOneConversationLeavesAnother(t *testing.T) {
	t.Cleanup(func() { CancelBackgroundJobs("sess-bg-other", "") })
	RegisterBackgroundJob("sess-bg-3", TaskRun{ID: "t1"}, nil)
	RegisterBackgroundJob("sess-bg-other", TaskRun{ID: "t2"}, nil)
	CancelBackgroundJobs("sess-bg-3", "")
	if len(ListBackgroundJobs("sess-bg-other")) != 1 {
		t.Error("cancelling one conversation must not touch another's work")
	}
}

// The delivery session is the conversation, so a wake turn asking "what is
// running here" gets the answer for the thread, not for its own sub-session.
func TestTheToolLooksAtTheConversationNotTheSubSession(t *testing.T) {
	const sess = "sess-bg-4"
	t.Cleanup(func() { CancelBackgroundJobs(sess, "") })
	RegisterBackgroundJob(sess, TaskRun{ID: "task-w", Label: "still going"}, nil)
	wake := &ToolSession{ChatSessionID: "scheduled:" + sess, DeliverySessionID: sess}
	out, err := (&backgroundWorkTool{}).RunWithSession(map[string]any{"action": "list"}, wake)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "task-w") {
		t.Errorf("a wake turn must see the conversation's work:\n%s", out)
	}
}
