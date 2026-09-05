package orchestrate

import "testing"

// The live pill takes a run's owner to the conversation it runs in — for a
// root run that has one. A nested dispatch names a sub-session the chat page
// cannot open, and a run with no session has nothing to open.
func TestRunOwnerDestination(t *testing.T) {
	root := RunSnapshot{ID: "r1", AgentID: "agent 1", SessionID: "sess/1", Depth: 0}
	if got, want := runOwnerDestination("/orchestrate", root), "/orchestrate/?agent=agent+1&session=sess%2F1"; got != want {
		t.Errorf("root run: got %q, want %q", got, want)
	}
	if got := runOwnerDestination("/orchestrate/", root); got != "/orchestrate/?agent=agent+1&session=sess%2F1" {
		t.Errorf("a trailing slash on the prefix must not double up, got %q", got)
	}
	nested := root
	nested.Depth = 1
	if got := runOwnerDestination("/orchestrate", nested); got != "" {
		t.Errorf("a nested dispatch has no thread of its own to open, got %q", got)
	}
	detached := root
	detached.SessionID = ""
	if got := runOwnerDestination("/orchestrate", detached); got != "" {
		t.Errorf("a run without a session has nothing to open, got %q", got)
	}
}
