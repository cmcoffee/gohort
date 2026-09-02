package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func msgsOf(sizes ...int) []ChatMessage {
	out := make([]ChatMessage, 0, len(sizes))
	for i, n := range sizes {
		out = append(out, ChatMessage{Role: "user", Content: strings.Repeat("x ", n) + "#" + string(rune('a'+i))})
	}
	return out
}

// The gap this closes: every other bound here counts MESSAGES, and on a
// persistent thread a message is a standing report or a daily brief, not a chat
// turn. A thread sitting exactly at its configured depth of 12, with the fold
// cursor advancing and nothing broken, still arrived having eaten most of the
// window — while every message-shaped diagnostic said it was fine.
func TestTailIsBoundedByTokensNotJustMessages(t *testing.T) {
	// Twelve messages — a healthy count — that are enormous individually.
	// Roughly 4000 tokens each: one standing report, not one chat turn.
	big := make([]int, 12)
	for i := range big {
		big[i] = 8000
	}
	tail := msgsOf(big...)

	got := capTailTokens(tail, 1_000_000, "ag", "channel:ag")
	if len(got) >= len(tail) {
		t.Fatalf("a message-shaped bound let all %d through; the window is measured in tokens", len(tail))
	}
	total := 0
	for _, m := range got {
		total += EstimateTokens(m.Content)
	}
	if budget := TuneInt("tune_persistent_tail_tokens"); total > budget {
		t.Errorf("kept ~%d tokens against a budget of %d", total, budget)
	}
	// Trimmed from the FRONT: the newest messages are what the turn is about.
	if got[len(got)-1].Content != tail[len(tail)-1].Content {
		t.Error("the newest message must survive — it is the turn being answered")
	}
}

// A bound that can drop the user's just-typed message produces a model
// answering from a summary about a question it was never shown.
func TestTheNewestMessageIsAlwaysKept(t *testing.T) {
	// One message that blows the entire budget on its own.
	tail := msgsOf(100, 200000)
	got := capTailTokens(tail, 8000, "ag", "channel:ag")
	if len(got) != 1 {
		t.Fatalf("expected only the newest, got %d", len(got))
	}
	if got[0].Content != tail[1].Content {
		t.Error("the kept message must be the newest one, however expensive")
	}
}

// The cases where doing nothing is right.
func TestTailBudgetLeavesWellEnoughAlone(t *testing.T) {
	tail := msgsOf(10, 10, 10)

	// Comfortably inside the budget.
	if got := capTailTokens(tail, 32000, "ag", "s"); len(got) != 3 {
		t.Errorf("a small tail must pass through untouched, got %d", len(got))
	}
	// An undeclared window is not a reason to skip the budget: the budget is
	// an absolute number of tokens and means the same thing without one. (The
	// first version took a percent of the window and did nothing here, which
	// is exactly the silence this test now forbids.)
	if got := capTailTokens(msgsOf(90000, 90000, 90000), 0, "ag", "s"); len(got) >= 3 {
		t.Errorf("with no declared window the token budget still governs, got %d", len(got))
	}
	// A single message is already the minimum.
	if got := capTailTokens(msgsOf(500000), 8000, "ag", "s"); len(got) != 1 {
		t.Errorf("one message stays one message, got %d", len(got))
	}
	if got := capTailTokens(nil, 8000, "ag", "s"); got != nil {
		t.Error("an empty tail stays empty")
	}
}

// The budget is an absolute number of tokens, because that is what a turn
// COSTS. A share of the window meant 70k on a 200k model and 350k on a 1M one —
// so on the big-window deployment the bound was never reached, the thread
// stayed enormous, and the setting looked applied. (Reported live: a cortex turn
// still arriving at 205k with the percent budget in force.)
func TestTheBudgetDoesNotGrowWithTheWindow(t *testing.T) {
	tail := msgsOf(9000, 9000, 9000, 9000, 9000, 9000)
	small := capTailTokens(tail, 200_000, "ag", "s")
	huge := capTailTokens(tail, 1_000_000, "ag", "s")
	if len(small) != len(huge) {
		t.Errorf("a bigger window bought a bigger tail: %d vs %d — the budget is tokens, not a share",
			len(small), len(huge))
	}
}

// The share survives as a CEILING only: a deployment that raised the budget for
// a 200k model must not hand the same tail to a 32k local one and lose the
// system prompt to it.
func TestASmallWindowCapsTheBudget(t *testing.T) {
	tail := msgsOf(2000, 2000, 2000, 2000, 2000, 2000)
	got := capTailTokens(tail, 16000, "ag", "s")
	total := 0
	for _, m := range got {
		total += EstimateTokens(m.Content)
	}
	if ceiling := 16000 * tailWindowShareCapPct / 100; total > ceiling {
		t.Errorf("kept ~%d tokens of a 16000 window; the cap is %d", total, ceiling)
	}
	if len(got) == 0 {
		t.Error("the cap must still leave the turn itself")
	}
}
