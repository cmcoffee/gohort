package orchestrate

import (
	"context"
	"testing"
)

// A note that only asks the run to stop stops it, as the Stop button does;
// one that says more is an instruction for the turn and joins it. Typed as a
// note, "Stop" was one more message: the model answered "Stopped" and made
// another tool call in the same reply, five times over.
func TestANoteThatSaysStopStopsTheRun(t *testing.T) {
	for _, s := range []string{"Stop", "stop.", "Stop now", "Stop now do not continue", "STOP!", "please stop", "halt", "Cancel that", "give up", "stop, don't continue", "Stop it right now"} {
		if !isStopRequest(s) {
			t.Errorf("%q is not taken as a stop", s)
		}
	}
	for _, s := range []string{"stop using the cache and rebuild", "don't stop, keep going", "can you stop the server first?", "Stop the build and show me the log", "cancel my subscription to the newsletter", ""} {
		if isStopRequest(s) {
			t.Errorf("%q is taken as a stop", s)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := runSessKey("alice", "s-stop")
	inflightCancels.Store(key, context.CancelFunc(cancel))
	defer inflightCancels.Delete(key)
	stopOnRequest("alice", "s-stop", "use the other API instead")
	if ctx.Err() != nil {
		t.Fatal("an instruction stopped the run")
	}
	stopOnRequest("bob", "s-stop", "stop")
	if ctx.Err() != nil {
		t.Fatal("someone else's stop stopped the run")
	}
	stopOnRequest("alice", "s-stop", "Stop now do not continue")
	if ctx.Err() == nil {
		t.Error("the person's stop did not stop the run")
	}
}
