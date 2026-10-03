package prompts

import (
	"strings"
	"sync"
	"testing"
)

var registerTierTestBlocks sync.Once

const (
	tierPlainKey = "test.tier_apply.plain"
	tierFillKey  = "test.tier_apply.fill"
	tierPlain    = "[Plain rule: say the thing once and plainly.]"
	tierFill     = "[Budget rule: this turn has up to {rounds} rounds; use the tools {{tool_list}} before answering.]"
)

func tierTestBlocks(t *testing.T) {
	t.Helper()
	registerTierTestBlocks.Do(func() {
		RegisterPromptBlock(PromptBlock{Key: tierPlainKey, Title: "plain", Text: tierPlain})
		RegisterPromptBlock(PromptBlock{Key: tierFillKey, Title: "fill", Text: tierFill})
	})
	SetPromptOverrideDB(jsonStore{})
	t.Cleanup(func() { SetPromptOverrideDB(nil) })
}

// A tier's own text replaces the shared text in a prompt that tier is sent,
// and only that tier's; the other tier keeps the shared text. The swap is
// recorded so the page can say a tier text is in use.
func TestTierTextSwapsForTheServingTier(t *testing.T) {
	tierTestBlocks(t)
	system := "You are helpful.\n\n" + tierPlain + "\n\nEnd."
	if got := ApplyTierText(TierLead, system); got != system {
		t.Fatalf("no tier text, yet the prompt changed: %q", got)
	}
	SetPromptTierOverride(TierWorker, tierPlainKey, "[Plain rule: SAY IT ONCE. Plainly.]", "small-model")
	if got := ApplyTierText(TierWorker, system); got != "You are helpful.\n\n[Plain rule: SAY IT ONCE. Plainly.]\n\nEnd." {
		t.Fatalf("worker prompt = %q", got)
	}
	if got := ApplyTierText(TierLead, system); got != system {
		t.Fatalf("the lead got the worker's words: %q", got)
	}
	if TierTextApplied(TierWorker, tierPlainKey).IsZero() || !TierTextApplied(TierLead, tierPlainKey).IsZero() {
		t.Fatal("the applied record is wrong")
	}
	// The shared text moved (an all-tiers edit): the swap follows it.
	SetPromptOverride(tierPlainKey, "[Plain rule, edited.]")
	if got := ApplyTierText(TierWorker, "x [Plain rule, edited.] y"); got != "x [Plain rule: SAY IT ONCE. Plainly.] y" {
		t.Fatalf("after the shared edit: %q", got)
	}
	if got := ApplyTierText(TierWorker, system); got != system {
		t.Fatalf("the old shared text was swapped after it stopped being the block's text: %q", got)
	}
	// Cleared: back to the shared text.
	ClearPromptTierOverride(TierWorker, tierPlainKey)
	if got := ApplyTierText(TierWorker, "x [Plain rule, edited.] y"); got != "x [Plain rule, edited.] y" {
		t.Fatalf("after clearing: %q", got)
	}
}

// A block with placeholders is found by its plain text, whatever the
// placeholders were filled with, and the tier's text gets the same values.
func TestTierTextCarriesPlaceholderValues(t *testing.T) {
	tierTestBlocks(t)
	filled := strings.NewReplacer("{rounds}", "24", "{{tool_list}}", "web_search, fetch_url").Replace(tierFill)
	system := "Persona.\n" + filled + "\nMore."
	SetPromptTierOverride(TierLead, tierFillKey, "[Budget: {rounds} rounds at most. Tools: {{tool_list}}.]", "")
	if got := ApplyTierText(TierLead, system); got != "Persona.\n[Budget: 24 rounds at most. Tools: web_search, fetch_url.]\nMore." {
		t.Fatalf("lead prompt = %q", got)
	}
	// A tier text naming a placeholder the block does not have would leave a
	// raw {name} in the prompt: refused, and the shared text stays.
	if p := TierPlaceholderProblem(tierFill, "[Budget: {limit}.]"); !strings.Contains(p, "{limit}") {
		t.Fatalf("problem = %q", p)
	}
	SetPromptTierOverride(TierLead, tierFillKey, "[Budget: {limit}.]", "")
	if got := ApplyTierText(TierLead, system); got != system {
		t.Fatalf("an unfillable tier text went out: %q", got)
	}
}

// Swaps that cannot be made exactly are not made.
func TestTierSwapRefusesWhatItCannotPlace(t *testing.T) {
	for _, shared := range []string{
		"{x} starts with a placeholder and goes on for a while",
		"ends with a placeholder after plenty of text {x}",
		"two placeholders side by side here {a}{b} and then plenty more text",
	} {
		if _, ok := newTierSwap("k", shared, "something else entirely", nil); ok {
			t.Errorf("a swap was built for %q", shared)
		}
	}
	if _, ok := newTierSwap("k", "same", "same", nil); ok {
		t.Error("a swap was built for identical text")
	}
}

// A block that registered a renderer is swapped as rendered, so one ending in
// a placeholder (Builder's does) is still found.
func TestTierTextUsesTheBlocksRenderer(t *testing.T) {
	tierTestBlocks(t)
	const key = "test.tier_apply.rendered"
	const shared = "Build carefully.{{note}}"
	registerTierRenderedBlock.Do(func() {
		RegisterPromptBlock(PromptBlock{Key: key, Title: "rendered", Text: shared})
		RegisterTierRender(key, func(s string) string { return strings.ReplaceAll(s, "{{note}}", " (python is old)") })
	})
	if p := TierTextPlaceable(key, shared); p != "" {
		t.Fatalf("a rendered block was called unplaceable: %s", p)
	}
	if p := TierTextPlaceable("test.unrendered", shared); p == "" {
		t.Fatal("a trailing placeholder without a renderer was called placeable")
	}
	SetPromptTierOverride(TierWorker, key, "BUILD CAREFULLY.{{note}}", "")
	if got := ApplyTierText(TierWorker, "P. Build carefully. (python is old) Q."); got != "P. BUILD CAREFULLY. (python is old) Q." {
		t.Fatalf("worker prompt = %q", got)
	}
}

var registerTierRenderedBlock sync.Once
