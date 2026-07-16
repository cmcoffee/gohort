package orchestrate

import (
	"fmt"
	"strings"
	"testing"
)

// The per-turn dispatch cap must break a genuine LOOP (same target + same
// message re-fired) without false-positiving on legitimate verification,
// where the Builder drives ONE agent with a DIFFERENT message per tool.

func TestDispatchCap_IdenticalMessageLoopBlocks(t *testing.T) {
	counts := map[string]int{}
	const msg = "run your profile tool"
	// First maxSameTargetDispatch identical calls are allowed.
	for i := 0; i < maxSameTargetDispatch; i++ {
		if block := dispatchCapDecision(counts, "tid", "Target", msg, false); block != "" {
			t.Fatalf("identical call %d/%d should be allowed, got block: %s", i+1, maxSameTargetDispatch, block)
		}
	}
	// The next identical call trips the loop cap.
	block := dispatchCapDecision(counts, "tid", "Target", msg, false)
	if block == "" {
		t.Fatal("identical message past the cap should be blocked")
	}
	if !strings.Contains(block, "SAME message") {
		t.Errorf("block should cite the identical-message loop; got: %s", block)
	}
}

func TestDispatchCap_DistinctMessagesDoNotCollide(t *testing.T) {
	counts := map[string]int{}
	// Exercising many DIFFERENT tools on one agent (profile, post, feed…) is
	// real progress — none of these should trip the loop cap even though
	// they all target the same agent, well past maxSameTargetDispatch calls.
	for i := 0; i < maxSameTargetDispatch+3; i++ {
		msg := fmt.Sprintf("run tool number %d", i)
		if block := dispatchCapDecision(counts, "tid", "Target", msg, false); block != "" {
			t.Fatalf("distinct message %d should be allowed (not a loop); got block: %s", i, block)
		}
	}
}

func TestDispatchCap_ThrashCeilingBuilderVsOrdinary(t *testing.T) {
	// An ordinary agent hits the lower total ceiling; Builder gets headroom.
	ordinary := map[string]int{}
	blockedAt := 0
	for i := 1; i <= maxBuilderTargetDispatch+1; i++ {
		msg := fmt.Sprintf("distinct %d", i)
		if dispatchCapDecision(ordinary, "tid", "Target", msg, false) != "" {
			blockedAt = i
			break
		}
	}
	if blockedAt != maxTotalTargetDispatch+1 {
		t.Errorf("ordinary agent should thrash-block at %d, blocked at %d", maxTotalTargetDispatch+1, blockedAt)
	}

	builder := map[string]int{}
	for i := 1; i <= maxTotalTargetDispatch+2; i++ {
		msg := fmt.Sprintf("distinct %d", i)
		if block := dispatchCapDecision(builder, "tid", "Target", msg, true); block != "" {
			t.Fatalf("Builder should not thrash-block at ordinary ceiling; blocked at %d: %s", i, block)
		}
	}
}
