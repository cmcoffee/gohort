package prompts

// Per-tier prompt text: the same block worded differently for the lead and
// for the worker.
//
// Prompt discipline is model-dependent data, not a fixed floor: a frontier
// lead may need a block said once and plainly where a small local worker
// needs it said firmly and early. A tier override is how a block says that,
// and the tuning harness is how a deployment finds out a block wants it. It
// is the exception: a block stays one text for both tiers until there is
// evidence the two want different words.
//
// Resolution is the tier's own text, then the all-tiers override, then what
// shipped. Each tier override remembers the model the tier was running when
// it was set, so a model swapped in behind a tier can be told the text was
// fitted to another.

import (
	"strings"
	"time"
)

// Tiers a block's text can differ by.
const (
	TierLead   = "lead"
	TierWorker = "worker"
)

// Tiers lists them in the order they are shown.
func Tiers() []string { return []string{TierLead, TierWorker} }

// TierOverride is one tier's text for a block.
type TierOverride struct {
	Text string `json:"text"`
	// Model is what the tier was running when this was set; empty when the
	// setter did not know.
	Model string    `json:"model,omitempty"`
	At    time.Time `json:"at"`
	// Via says who wrote it: "tuned" for Optimize, "edit" by hand, "" when
	// the writer did not say.
	Via string `json:"via,omitempty"`
}

// A prefix of its own, so no block key can collide with a tier key.
const promptTierPrefix = "prompt_tier_override."

func tierOverrideKey(tier, key string) string { return promptTierPrefix + tier + "|" + key }

func validTier(tier string) bool { return tier == TierLead || tier == TierWorker }

// PromptTierOverride returns a tier's own text for a block, if it has one.
func PromptTierOverride(tier, key string) (TierOverride, bool) {
	db := promptOverrideStore()
	if db == nil || !validTier(tier) {
		return TierOverride{}, false
	}
	var o TierOverride
	if db.Get(OverrideTable, tierOverrideKey(tier, key), &o) && strings.TrimSpace(o.Text) != "" {
		return o, true
	}
	return TierOverride{}, false
}

// SetPromptTierOverride gives a tier its own text for a block. model is what
// the tier runs now. Empty text clears it.
func SetPromptTierOverride(tier, key, text, model string) {
	SetPromptTierOverrideBy(tier, key, text, model, "")
}

// SetPromptTierOverrideBy is SetPromptTierOverride saying who wrote it
// (TierOverride.Via).
func SetPromptTierOverrideBy(tier, key, text, model, via string) {
	db := promptOverrideStore()
	if db == nil || !validTier(tier) {
		return
	}
	defer tierTextChanged()
	if strings.TrimSpace(text) == "" {
		db.Unset(OverrideTable, tierOverrideKey(tier, key))
		return
	}
	db.Set(OverrideTable, tierOverrideKey(tier, key), TierOverride{Text: text, Model: strings.TrimSpace(model), At: time.Now(), Via: via})
}

// ClearPromptTierOverride drops a tier's own text, so the tier follows the
// all-tiers text again.
func ClearPromptTierOverride(tier, key string) {
	if db := promptOverrideStore(); db != nil && validTier(tier) {
		db.Unset(OverrideTable, tierOverrideKey(tier, key))
		tierTextChanged()
	}
}

// EffectivePromptTextFor is the text a block gives a loop served by tier:
// the tier's own text, else the all-tiers override, else def. An empty or
// unknown tier reads as all tiers, which is EffectivePromptText.
func EffectivePromptTextFor(tier, key, def string) string {
	if o, ok := PromptTierOverride(tier, key); ok {
		return o.Text
	}
	return EffectivePromptText(key, def)
}

// TierOverrideStale reports whether a tier's text was set while the tier ran
// a different model than current: worded for a model that is no longer the
// one reading it. False when either model is unknown.
func TierOverrideStale(tier, key, current string) bool {
	o, ok := PromptTierOverride(tier, key)
	current = strings.TrimSpace(current)
	return ok && o.Model != "" && current != "" && !strings.EqualFold(o.Model, current)
}
