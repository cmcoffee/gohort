package core

// Thinking is switched OFF for one round after a guardrail block.
//
// A block is the round where deliberation reliably goes wrong: the model is
// handed a refusal it did not expect, about a mechanism it is told not to name,
// and asked to carry on. It reasons at length about the system it is inside
// instead of answering — the failure COLLAPSE-DIAG exists to detect. Trimming
// the block messages helped and did not fix it, because the deliberation is
// caused by the situation rather than only by the wording.
//
// These pin the mechanism at the level it can be tested without a live model:
// the option is produced, it wins over the static options, and it lasts exactly
// one round.

import (
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/textutil"
)

// thinkStateOf resolves a built option slice the way the LLM layer does and
// reports the thinking decision it lands on.
func thinkStateOf(opts []ChatOption) (enabled, set bool) {
	cfg := applyOpts("m", 1024, opts)
	if cfg.Think == nil {
		return false, false
	}
	return *cfg.Think, true
}

// TestQuietRoundOptionWins — the option is appended LAST, so it has to beat a
// per-agent think budget and any static ChatOptions the caller set. An agent
// configured to think would otherwise keep thinking through exactly the round
// this exists for.
func TestQuietRoundOptionWins(t *testing.T) {
	// What the loop builds: static options first, the one-shot last.
	static := []ChatOption{WithThink(true), WithThinkBudget(4096)}
	on, set := thinkStateOf(static)
	if !set || !on {
		t.Fatalf("baseline should have thinking on: on=%v set=%v", on, set)
	}
	quiet := append(append([]ChatOption{}, static...), WithThink(false))
	on, set = thinkStateOf(quiet)
	if !set {
		t.Fatal("the one-shot option did not reach the config")
	}
	if on {
		t.Error("a static WithThink(true) beat the one-shot; it must be appended last and win")
	}
}

// TestQuietRoundIsOneShot — the flag is consumed when applied. A block that
// silenced thinking for the rest of the turn would degrade every later round,
// including ones doing real work after the agent changed course.
func TestQuietRoundIsOneShot(t *testing.T) {
	// Mirrors the loop's consume-on-apply shape.
	quiet := true
	build := func() []ChatOption {
		opts := []ChatOption{WithThink(true)}
		if quiet {
			quiet = false
			opts = append(opts, WithThink(false))
		}
		return opts
	}
	if on, _ := thinkStateOf(build()); on {
		t.Error("the round after a block should not think")
	}
	if quiet {
		t.Error("the flag was not consumed")
	}
	if on, _ := thinkStateOf(build()); !on {
		t.Error("thinking must come back the round after — the agent still has work to do")
	}
}

// TestUnblockedRoundsAreUntouched — an agent that never trips a guardrail must
// build byte-identical options to before this existed. The whole feature is
// meant to cost nothing in the steady state.
func TestUnblockedRoundsAreUntouched(t *testing.T) {
	quiet := false
	opts := []ChatOption{WithThink(true), WithThinkBudget(2048)}
	if quiet {
		opts = append(opts, WithThink(false))
	}
	on, set := thinkStateOf(opts)
	if !set || !on {
		t.Errorf("an unblocked round changed shape: on=%v set=%v", on, set)
	}
	cfg := applyOpts("m", 1024, opts)
	if cfg.ThinkBudget == nil || *cfg.ThinkBudget != 2048 {
		t.Errorf("the agent's think budget was disturbed: %v", cfg.ThinkBudget)
	}
}

// TestClosedNoteIsForTheModelOnly — the note exists to stop the next turn
// answering a request that was refused, and it must never reach the reader.
func TestClosedNoteIsForTheModelOnly(t *testing.T) {
	substituted := "I can't help with that one." + guardrailClosedNote

	// The persisted copy carries it: cleanReply is stored BEFORE stripping, so
	// the next turn's history contains it and the model sees the outcome.
	if !strings.Contains(substituted, "declined and is closed") {
		t.Fatal("the persisted reply lost the note the next turn depends on")
	}
	// Every delivery boundary strips it.
	delivered := textutil.StripMetaTags(substituted)
	if strings.Contains(delivered, "gohort-meta") || strings.Contains(delivered, "declined and is closed") {
		t.Errorf("the note reached the reader: %q", delivered)
	}
	if strings.TrimSpace(delivered) != "I can't help with that one." {
		t.Errorf("stripping disturbed the decline itself: %q", delivered)
	}
	// It must not name the MECHANISM. Doing so invites the model to reason
	// about the system it is inside — the deliberation the one-shot
	// thinking-off exists to stop, and the reason guardrailInputMessage
	// dropped "ENFORCED GUARDRAIL". Nor may it restate the subject, which
	// would re-seed the topic the block just removed.
	low := strings.ToLower(guardrailClosedNote)
	for _, leak := range []string{"guardrail", "rule", "enforced", "check", "policy", "blocked"} {
		if strings.Contains(low, leak) {
			t.Errorf("the note names the mechanism (%q): %s", leak, guardrailClosedNote)
		}
	}
	// And it still reads as CLOSED rather than deferred — the distinction the
	// model got wrong live.
	for _, want := range []string{"closed", "do not answer it later"} {
		if !strings.Contains(low, want) {
			t.Errorf("the note does not say the request is settled (missing %q)", want)
		}
	}
}
