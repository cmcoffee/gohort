package admin

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/prompts"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Settling clears hand edits, makes the worker's tuned wording the shared
// text, drops the lead's, and then never runs again: after it, the shared
// text is Optimize's, and a second run would undo it.
func TestSettlingRunsOnceInTheRightOrder(t *testing.T) {
	SetPromptOverrideDB(&DBase{Store: kvlite.MemStore()})
	t.Cleanup(func() { SetPromptOverrideDB(nil) })
	var plain []PromptBlock
	for _, x := range AllPromptBlocks() {
		if x.Text != "" && !strings.Contains(x.Text, "{") {
			plain = append(plain, x)
		}
	}
	if len(plain) < 2 {
		t.Skip("fewer than two plain registered blocks")
	}
	a, b := plain[0], plain[1]
	SetPromptOverride(a.Key, "hand edit")
	prompts.SetPromptTierOverrideBy(prompts.TierWorker, a.Key, "worker tuned", "local/qwen", "tuned")
	prompts.SetPromptTierOverrideBy(prompts.TierLead, a.Key, "lead tuned", "cloud/flash", "tuned")
	prompts.SetPromptTierOverrideBy(prompts.TierLead, b.Key, "lead by hand", "cloud/flash", "edit")
	db := &DBase{Store: kvlite.MemStore()}

	if n := settlePromptWording(context.Background(), db); n != 4 {
		t.Fatalf("changed %d, want 4", n)
	}
	if got, _ := PromptOverride(a.Key); got != "worker tuned" {
		t.Fatalf("a's shared text = %q, want the worker's tuned wording", got)
	}
	for _, k := range []string{a.Key, b.Key} {
		for _, tier := range prompts.Tiers() {
			if _, own := prompts.PromptTierOverride(tier, k); own {
				t.Fatalf("%s kept the %s's own wording", k, tier)
			}
		}
	}
	// Optimize writes the shared text afterwards; a second run leaves it.
	SetPromptOverride(b.Key, "optimize wrote this")
	if n := settlePromptWording(context.Background(), db); n != 0 {
		t.Fatalf("a second run changed %d", n)
	}
	if got, _ := PromptOverride(b.Key); got != "optimize wrote this" {
		t.Fatal("a second run cleared Optimize's wording")
	}
}
