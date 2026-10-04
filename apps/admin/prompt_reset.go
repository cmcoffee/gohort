package admin

// A maintenance pass that puts prompt blocks back on what ships.
//
// The hand editor for prompt blocks is gone, and what it saved would
// otherwise go on applying with nothing left to show it, freezing each block
// it touched against every later change to the shipped text. This clears a
// block's shared text wherever it differs from what ships, and each model's
// own wording where it was written by hand. Optimize's per-model wording
// stays: its own Undo and Reset look after that. Each cleared text is written
// to the log first, so nothing is lost that cannot be read back.

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/prompts"
)

func init() {
	RegisterMaintenanceFunc("Housekeeping", "reset_edited_prompts", "Reset edited prompt blocks",
		"Puts every prompt block whose text was changed by hand back on the shipped text, so it follows "+
			"future releases again. Wording Optimize gave a model stays. Each cleared text is written to the log first.",
		resetEditedPrompts)
}

// resetEditedPrompts clears the hand-made overrides and says which blocks it
// cleared. It returns how many texts it cleared.
func resetEditedPrompts(ctx context.Context) int {
	n := 0
	var titles []string
	blocks := AllPromptBlocks()
	for i, b := range blocks {
		if ctx.Err() != nil {
			break
		}
		ReportMaintenanceProgress(ctx, fmt.Sprintf("%d of %d blocks - %d reset", i+1, len(blocks), n))
		cleared := false
		if text, ok := PromptOverride(b.Key); ok {
			Log("[admin] prompt block %s reset to shipped; its text was:\n%s", b.Key, text)
			ClearPromptOverride(b.Key)
			n++
			cleared = true
		}
		for _, tier := range prompts.Tiers() {
			o, ok := prompts.PromptTierOverride(tier, b.Key)
			if !ok || o.Via != "edit" {
				continue
			}
			Log("[admin] prompt block %s: the %s's own wording, written by hand, cleared; it was:\n%s", b.Key, tier, o.Text)
			prompts.ClearPromptTierOverride(tier, b.Key)
			n++
			cleared = true
		}
		if cleared {
			titles = append(titles, b.Title)
		}
	}
	switch {
	case ctx.Err() != nil:
		ReportMaintenanceOutcome(ctx, fmt.Sprintf("Stopped after resetting %d text(s).", n))
	case n == 0:
		ReportMaintenanceOutcome(ctx, "No prompt block had text changed by hand.")
	default:
		ReportMaintenanceOutcome(ctx, fmt.Sprintf("Reset %d text(s) to shipped: %s.", n, strings.Join(titles, ", ")))
	}
	return n
}
