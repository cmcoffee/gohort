// The turn's context has to reach the session, or two features that look wired
// are quietly off.
package orchestrate

import (
	"context"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestASessionWithoutTheTurnContextCannotDetach(t *testing.T) {
	// What the logs showed, four times: "[task] image stayed inline: no owning
	// agent to deliver as". A tool finds the run that owns it by reading the
	// parent-run tag off sess.Context(); a session built before the tag was
	// applied — and never given the context at all — falls back to
	// context.Background(), where there is no tag and no owner. Detaching then
	// fails every time and a fifteen-minute render holds the turn open.
	untagged := &ToolSession{}
	if got := parentRunFromCtx(untagged.Context()); got != "" {
		t.Fatalf("a session with no context should carry no parent run, got %q", got)
	}

	tagged := &ToolSession{Ctx: withParentRun(context.Background(), "run_42")}
	if got := parentRunFromCtx(tagged.Context()); got != "run_42" {
		t.Errorf("parent run = %q, want run_42 — this is what the detach path reads", got)
	}
}

func TestCancellationOnlyReachesASessionHoldingTheTurnContext(t *testing.T) {
	// The same omission with the other consequence: the image poll loop selects
	// on sess.Context(), so a background context means Stop never lands.
	ctx, cancel := context.WithCancel(context.Background())
	sess := &ToolSession{Ctx: ctx}
	cancel()
	if sess.Context().Err() == nil {
		t.Error("cancelling the turn must be visible through the session")
	}
	if (&ToolSession{}).Context().Err() != nil {
		t.Error("a session with no context should not look cancelled")
	}
}
