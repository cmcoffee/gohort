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
	big := make([]int, 12)
	for i := range big {
		big[i] = 4000
	}
	tail := msgsOf(big...)

	got := capTailTokens(tail, 32000, "ag", "channel:ag")
	if len(got) >= len(tail) {
		t.Fatalf("a message-shaped bound let all %d through; the window is measured in tokens", len(tail))
	}
	total := 0
	for _, m := range got {
		total += EstimateTokens(m.Content)
	}
	if budget := 32000 * 35 / 100; total > budget {
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
	// No declared window to take a percent of: a guess here would be a budget
	// nobody chose, applied to every deployment that never set a size.
	if got := capTailTokens(msgsOf(9000, 9000, 9000), 0, "ag", "s"); len(got) != 3 {
		t.Errorf("with no context size the message bound governs, got %d", len(got))
	}
	// A single message is already the minimum.
	if got := capTailTokens(msgsOf(500000), 8000, "ag", "s"); len(got) != 1 {
		t.Errorf("one message stays one message, got %d", len(got))
	}
	if got := capTailTokens(nil, 8000, "ag", "s"); got != nil {
		t.Error("an empty tail stays empty")
	}
}
