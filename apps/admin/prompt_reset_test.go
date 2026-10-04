package admin

import (
	"context"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/prompts"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The reset clears what was changed by hand, the shared text and a model's
// hand-written wording, and leaves the wording Optimize gave a model.
func TestResetEditedPromptsKeepsOptimizesWording(t *testing.T) {
	SetPromptOverrideDB(&DBase{Store: kvlite.MemStore()})
	t.Cleanup(func() { SetPromptOverrideDB(nil) })
	blocks := AllPromptBlocks()
	if len(blocks) < 2 {
		t.Skip("fewer than two registered blocks")
	}
	a, b := blocks[0], blocks[1]
	SetPromptOverride(a.Key, "edited by hand")
	prompts.SetPromptTierOverrideBy(prompts.TierWorker, a.Key, "worker words by hand", "local/qwen", "edit")
	prompts.SetPromptTierOverrideBy(prompts.TierLead, b.Key, "lead words from Optimize", "cloud/flash", "tuned")

	if n := resetEditedPrompts(context.Background()); n != 2 {
		t.Fatalf("reset %d texts, want 2", n)
	}
	if _, ok := PromptOverride(a.Key); ok {
		t.Fatal("the shared edit stayed")
	}
	if _, ok := prompts.PromptTierOverride(prompts.TierWorker, a.Key); ok {
		t.Fatal("the hand-written worker wording stayed")
	}
	if o, ok := prompts.PromptTierOverride(prompts.TierLead, b.Key); !ok || o.Text != "lead words from Optimize" {
		t.Fatal("Optimize's wording was cleared")
	}
	var found bool
	for _, m := range ListMaintenanceFuncs() {
		found = found || m.Key == "reset_edited_prompts"
	}
	if !found {
		t.Fatal("the reset is not registered as maintenance")
	}
}
