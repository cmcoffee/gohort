package core

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// clearObservedCeilings keeps cases from leaking into one another — the store
// is process-wide by design.
func clearObservedCeilings(t *testing.T) {
	t.Helper()
	reset := func() {
		observedCeilings.Range(func(k, _ any) bool { observedCeilings.Delete(k); return true })
	}
	reset()
	t.Cleanup(reset)
}

// The exact regression, in the numbers the log recorded: a worker advertising
// 262144 refused a prompt estimated at ~130k. Recovery used to take the
// configured number, ask stillTooBig whether the history fit inside it, hear
// "yes", and skip every rung of the ladder below — so the retry re-sent what
// had just been refused.
func TestRecoveryWindowLandsBelowTheRefusedPrompt(t *testing.T) {
	const configured, refused = 262144, 130333

	window := recoveryWindow(configured, fmt.Errorf("llama.cpp api error (500): Context size has been exceeded."), refused)
	if window >= refused {
		t.Fatalf("recovery window %d is not below the refused prompt %d — the ladder will no-op", window, refused)
	}

	// The behavioural consequence, which is the thing that actually matters:
	// a history that size must now read as too big, so summarize and elide run.
	history := historyOfAboutTokens(refused)
	if !stillTooBig(history, "", window) {
		t.Error("history that was just refused does not read as too big in the recovery window")
	}
	// And the other half of the regression, pinned so it can't come back: the
	// SAME history reads as fitting against the configured window. That is
	// precisely why recovering into cfg.ContextSize did nothing at all.
	if stillTooBig(history, "", configured) {
		t.Error("test no longer reproduces the bug: the refused history reads as too big even against the configured window")
	}
}

// A refusal with nothing to do with size must not drive recovery into a window
// too small to hold a turn.
func TestRecoveryWindowIsFloored(t *testing.T) {
	if got := recoveryWindow(262144, nil, 1000); got < observedCeilingFloor {
		t.Errorf("recovery window %d fell below the floor %d", got, observedCeilingFloor)
	}
}

// With nothing configured, the provider's own wording is still the better
// source than the blind fallback.
func TestRecoveryWindowFallsBackWhenNothingIsConfigured(t *testing.T) {
	err := fmt.Errorf("maximum context length is 32768 tokens, however you requested 40000 tokens")
	if got := recoveryWindow(0, err, 0); got != 32768 {
		t.Errorf("window = %d, want the 32768 the refusal named", got)
	}
	if got := recoveryWindow(0, fmt.Errorf("context exceeded"), 0); got != fallbackRecoveryWindow {
		t.Errorf("window = %d, want the %d fallback", got, fallbackRecoveryWindow)
	}
}

// The point of remembering: the NEXT turn has to build smaller, or it spends a
// round rediscovering the same wall.
func TestARefusalShrinksTheWindowTheNextTurnBuildsAgainst(t *testing.T) {
	clearObservedCeilings(t)
	const configured = 262144

	if got := effectiveContextSize(configured); got != configured {
		t.Fatalf("with no refusal seen, effective size = %d, want the configured %d", got, configured)
	}
	noteContextRefusal(configured, 130333)
	got := effectiveContextSize(configured)
	if got >= 130333 {
		t.Errorf("effective size %d is not below the refused %d", got, 130333)
	}
	if got <= 0 {
		t.Errorf("effective size collapsed to %d", got)
	}
}

// Two refusals mean the ceiling is under both.
func TestSmallestRefusalWins(t *testing.T) {
	clearObservedCeilings(t)
	const configured = 262144
	noteContextRefusal(configured, 130000)
	noteContextRefusal(configured, 90000)
	first := effectiveContextSize(configured)
	noteContextRefusal(configured, 200000) // a later, larger refusal teaches nothing new
	if second := effectiveContextSize(configured); second != first {
		t.Errorf("a larger refusal loosened the ceiling: %d → %d", first, second)
	}
}

// The real ceiling moves — on the deployment that prompted this, a 149,267-token
// prompt was accepted and a ~130k one refused later the same day. So the clamp
// has to lift on its own: no restart, no setting.
func TestTheCeilingExpiresSoTheWindowComesBack(t *testing.T) {
	clearObservedCeilings(t)
	const configured = 262144
	noteContextRefusal(configured, 130333)
	if effectiveContextSize(configured) == configured {
		t.Fatal("the refusal was not recorded at all")
	}
	// Age the observation past its TTL.
	v, _ := observedCeilings.Load(configured)
	obs := v.(observedCeiling)
	observedCeilings.Store(configured, observedCeiling{tokens: obs.tokens, at: time.Now().Add(-observedCeilingTTL - time.Minute)})

	if got := effectiveContextSize(configured); got != configured {
		t.Errorf("stale ceiling still clamping: %d, want the full %d back", got, configured)
	}
	if _, still := observedCeilings.Load(configured); still {
		t.Error("expired observation was not dropped, so it keeps being re-checked")
	}
}

// A refusal at or above the configured window teaches nothing, and recording it
// would raise a ceiling a smaller refusal had correctly lowered.
func TestARefusalAboveTheClaimIsNotRecorded(t *testing.T) {
	clearObservedCeilings(t)
	const configured = 262144
	noteContextRefusal(configured, 400000)
	if got := effectiveContextSize(configured); got != configured {
		t.Errorf("effective size = %d, want the configured %d left alone", got, configured)
	}
}

// historyOfAboutTokens builds a history whose estimate is near n tokens, so a
// case can talk in the units the code does.
func historyOfAboutTokens(n int) []Message {
	return []Message{{Role: "user", Content: strings.Repeat("x", n*4)}}
}
