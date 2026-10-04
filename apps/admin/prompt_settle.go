package admin

// A one-time maintenance pass that settles prompt wording for the move to one
// shared wording.
//
// Two things written before that move would otherwise go on applying unseen.
// The hand editor for prompt blocks is gone, and what it saved froze each
// block it touched against later releases. And Optimize used to apply what it
// kept as one model's own wording; it is now one optimization aimed at the
// worker, which both models read, because the lead is the stronger model and
// works with wording that helps the worker.
//
// So, in this order: hand edits are cleared (a block's shared text, and any
// model's wording written by hand); then the worker's tuned wording becomes
// the shared text (it is what was measured) and the lead's is dropped. Each
// text goes to the log first.
//
// Once only. Before any one-wording Optimize run, every shared override is a
// hand edit; after one, the shared text is Optimize's, and clearing it would
// undo the run. So the pass records that it ran and does nothing again. A
// later reset is Optimize's own Reset to shipped, which knows what it applied.

import (
	"context"
	"fmt"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/prompts"
)

const (
	settleTable = "maintenance_once"
	settleKey   = "settle_prompt_wording"
)

func init() {
	RegisterMaintenanceFunc("Housekeeping", settleKey, "Settle prompt wording (once)",
		"Run once after upgrading: clears prompt block text changed by hand in the old editor, makes the worker's "+
			"tuned wording the text both models read, and drops the lead's. Each text is logged first. It does "+
			"nothing on later runs: after Optimize writes its wording, Optimize's Reset to shipped is the reset.",
		func(ctx context.Context) int { return settlePromptWording(ctx, RootDB) })
}

// settlePromptWording returns how many texts it changed.
func settlePromptWording(ctx context.Context, db Database) int {
	if db != nil {
		var at time.Time
		if db.Get(settleTable, settleKey, &at) {
			ReportMaintenanceOutcome(ctx, "Already settled on "+at.Format("Jan 2 15:04")+"; nothing to do.")
			return 0
		}
	}
	n, cleared, folded, dropped := 0, 0, 0, 0
	blocks := AllPromptBlocks()
	for i, b := range blocks {
		if ctx.Err() != nil {
			break
		}
		ReportMaintenanceProgress(ctx, fmt.Sprintf("%d of %d blocks - %d changed", i+1, len(blocks), n))
		// Hand edits first, so the fold below writes over nothing of them.
		if text, ok := PromptOverride(b.Key); ok {
			Log("[admin] prompt block %s: hand-edited text cleared; it was:\n%s", b.Key, text)
			ClearPromptOverride(b.Key)
			cleared++
		}
		for _, tier := range prompts.Tiers() {
			if o, ok := prompts.PromptTierOverride(tier, b.Key); ok && o.Via == "edit" {
				Log("[admin] prompt block %s: the %s's wording, written by hand, cleared; it was:\n%s", b.Key, tier, o.Text)
				prompts.ClearPromptTierOverride(tier, b.Key)
				cleared++
			}
		}
		if o, ok := prompts.PromptTierOverride(prompts.TierWorker, b.Key); ok {
			// A text naming a placeholder the block does not fill would reach
			// every model as a raw {name}: dropped, not folded.
			if why := prompts.TierPlaceholderProblem(b.Text, o.Text); why != "" {
				Log("[admin] prompt block %s: the worker's tuned wording dropped, not folded (it %s); it was:\n%s", b.Key, why, o.Text)
				dropped++
			} else {
				SetPromptOverride(b.Key, o.Text)
				folded++
			}
			prompts.ClearPromptTierOverride(prompts.TierWorker, b.Key)
		}
		if o, ok := prompts.PromptTierOverride(prompts.TierLead, b.Key); ok {
			Log("[admin] prompt block %s: the lead's tuned wording dropped; it was:\n%s", b.Key, o.Text)
			prompts.ClearPromptTierOverride(prompts.TierLead, b.Key)
			dropped++
		}
		n = cleared + folded + dropped
	}
	if ctx.Err() != nil {
		ReportMaintenanceOutcome(ctx, fmt.Sprintf("Stopped after changing %d text(s); run it again to finish.", n))
		return n
	}
	if db != nil {
		db.Set(settleTable, settleKey, time.Now())
	}
	ReportMaintenanceOutcome(ctx, fmt.Sprintf("Settled: %d hand edit(s) cleared, %d worker wording(s) made shared, %d dropped.", cleared, folded, dropped))
	return n
}
