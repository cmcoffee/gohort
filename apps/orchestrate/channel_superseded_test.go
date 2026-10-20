package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A channel run cancelled because a newer message took over says nothing,
// since the newer run answers. A run cancelled with nothing newer behind it,
// and a run that failed some other way, still apologise.
func TestASupersededChannelRunSaysNothing(t *testing.T) {
	T := &OrchestrateApp{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	// A run stopped with nothing newer behind it: by the time the channel
	// looks, the run has ended, and the conversation has no active run.
	_, stopped := T.runsRegistry().CreateCancellable(context.Background(), "u", "wiwee", "chan:room")
	stopped.Complete(RunStatusCanceled)
	if T.supersededInConversation("u", "chan:room", context.Canceled) {
		t.Error("with no newer run, a cancel is not a supersede")
	}
	// The next message's run replaces the one answering the message before.
	_, first := T.runsRegistry().CreateCancellable(context.Background(), "u", "wiwee", "chan:room")
	_, next := T.runsRegistry().CreateCancellable(context.Background(), "u", "wiwee", "chan:room")
	defer next.Complete(RunStatusCompleted)
	first.Complete(RunStatusCanceled)
	if !T.supersededInConversation("u", "chan:room", fmt.Errorf("run: %w", context.Canceled)) {
		t.Error("the run a newer message replaced should stay quiet")
	}
	if T.supersededInConversation("u", "chan:room", errors.New("the model refused")) {
		t.Error("a failure that is not a cancel was read as superseded")
	}
	if T.supersededInConversation("u", "chan:other", context.Canceled) {
		t.Error("a newer run in another conversation superseded this one")
	}
}
