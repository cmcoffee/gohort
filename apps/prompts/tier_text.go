package prompts

// A block's wording per model: the worker's and the lead's.
//
// The editor shows every block in three versions: what both models read,
// and each model's own. A model reads its own wording when it has one, else
// the shared text, so a block is split only where something put a split
// there: Optimize, when it finds wording that builds better on one model,
// or a hand edit to one model's version. Saving a model's version back to
// the shared text, or empty, removes the split.

import (
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/prompts"
	"github.com/cmcoffee/gohort/core/ui"
)

// The editor's versions. "all" is the shared text.
const variantAll = "all"

func editorVariants() []ui.SelectOption {
	return []ui.SelectOption{
		{Value: variantAll, Label: "Both models", Help: "What every model reads, unless it has its own wording."},
		{Value: prompts.TierWorker, Label: "Worker", Help: "The worker's own wording. Saving the shared text here removes it."},
		{Value: prompts.TierLead, Label: "Lead", Help: "The lead's own wording. Saving the shared text here removes it."},
	}
}

// tierModel is what a tier runs now, as configured: the lead falls back to
// the worker when there is no separate one, so its text is read by the worker.
func tierModel(tier string) string {
	worker, lead := LiveLLMs()
	if tier == prompts.TierLead && lead != "" {
		return lead
	}
	return worker
}

// shortModel is a model as people name it, without the address it is served
// from ("local/qwen at http://..." is "local/qwen").
func shortModel(s string) string {
	if i := strings.Index(s, " at "); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// tierTextProblem says why text cannot be a tier's own wording for block b,
// or "" when it can. The swap into a prompt is made on the way out, by
// finding the block's shared text and putting this in its place, so the
// shared text has to be findable and this has to fill only the placeholders
// the shared text fills.
func tierTextProblem(b PromptBlock, text string) string {
	if strings.HasPrefix(b.Key, prompts.ToolBlockPrefix) {
		return "" // a tool's description is replaced whole, by the tool's name
	}
	shared := EffectivePromptText(b.Key, b.Text)
	if why := prompts.TierTextPlaceable(b.Key, shared); why != "" {
		return "This block cannot be worded per model: " + why + "."
	}
	if why := prompts.TierPlaceholderProblem(shared, text); why != "" {
		return "This text " + why + "."
	}
	return ""
}

// ApplyTierEdit sets a tier's own text for a block from outside the editor
// (Optimize), stamped with the model the tier runs now. Empty text removes
// it, so the tier reads the block's text again.
func ApplyTierEdit(tier, key, text string) error {
	if tier != prompts.TierLead && tier != prompts.TierWorker {
		return fmt.Errorf("a tier is lead or worker, not %q", tier)
	}
	b, ok := lookupBlock(key)
	if !ok {
		return fmt.Errorf("no prompt block %q", key)
	}
	if strings.TrimSpace(text) != "" {
		if why := tierTextProblem(b, text); why != "" {
			return fmt.Errorf("%s", why)
		}
	}
	prompts.SetPromptTierOverrideBy(tier, key, text, tierModel(tier), "tuned")
	return nil
}

// variantText is block b's text in one version, and a line saying where it
// came from.
func variantText(b PromptBlock, variant string) (body, note string) {
	shared := EffectivePromptText(b.Key, b.Text)
	tier := variant
	if tier != prompts.TierWorker && tier != prompts.TierLead {
		var own []string
		for _, t := range prompts.Tiers() {
			if _, ok := prompts.PromptTierOverride(t, b.Key); ok {
				own = append(own, "the "+t)
			}
		}
		note = "What every model reads."
		if len(own) > 0 {
			note = "What every model reads, except " + strings.Join(own, " and ") + ", which has its own wording."
		}
		return shared, note
	}
	if tier == prompts.TierLead && !LeadIsDistinct() {
		note = "There is no separate lead model, so the worker serves the lead's calls and reads the worker's version. "
	}
	o, ok := prompts.PromptTierOverride(tier, b.Key)
	if !ok {
		return shared, note + "The " + tier + " reads the shared wording. Save a change here to give it its own."
	}
	who := "by hand"
	if o.Via == "tuned" {
		who = "by Optimize"
	}
	note += "The " + tier + "'s own wording, written " + who
	if !o.At.IsZero() {
		note += " " + o.At.Format("Jan 2")
	}
	if m := shortModel(o.Model); m != "" {
		note += " for " + m
	}
	note += "."
	if prompts.TierOverrideStale(tier, b.Key, tierModel(tier)) {
		note += " The " + tier + " now runs " + shortModel(tierModel(tier)) + ": it was fitted to another model."
	}
	// Whether it reaches the model at all: a block in no prompt the tier is
	// sent is wording nothing reads. A tool's description is replaced by
	// name and not counted here.
	if !strings.HasPrefix(b.Key, prompts.ToolBlockPrefix) {
		if at := prompts.TierTextApplied(tier, b.Key); !at.IsZero() {
			note += " Last sent " + at.Format("Jan 2 15:04") + "."
		} else {
			note += " Not sent to the " + tier + " since the server started."
		}
	}
	return o.Text, note
}

// saveVariant writes block b's text in one version. A model's version saved
// as the shared text, or empty, stops being its own.
func (T *PromptsApp) saveVariant(b PromptBlock, variant, body string) error {
	tier := variant
	shared := EffectivePromptText(b.Key, b.Text)
	prior, _ := variantText(b, tier)
	clearing := strings.TrimSpace(body) == "" || body == shared
	if !clearing {
		if why := tierTextProblem(b, body); why != "" {
			return fmt.Errorf("%s", why)
		}
	}
	next := shared
	if !clearing {
		next = body
	}
	if next != prior {
		T.snapshotRevision(b.Key, prior, "edit", "the "+tier+"'s wording, "+time.Now().Format("Jan 2 15:04"))
	}
	if clearing {
		prompts.ClearPromptTierOverride(tier, b.Key)
		return nil
	}
	prompts.SetPromptTierOverrideBy(tier, b.Key, body, tierModel(tier), "edit")
	return nil
}
