package prompts

import (
	"testing"
	"time"
)

// When the worker is a peer's model the peer's wording wins over the local
// override; clearing it hands the block back to the local override.
func TestPeerWordingGovernsAndClearingRestoresLocal(t *testing.T) {
	tierTestBlocks(t)
	SetPromptOverride(tierPlainKey, "local words")
	res := SetPeerPromptLayer("den", "qwen3.6", time.Now(), map[string]string{tierPlainKey: "peer words"})
	if res.Kept != 1 || !res.Changed {
		t.Fatalf("result %+v, want one kept and changed", res)
	}
	if got := EffectivePromptText(tierPlainKey, tierPlain); got != "peer words" {
		t.Fatalf("with a peer layer: %q, want the peer's text", got)
	}
	st := PeerPromptLayer()
	if st.Source != "den" || st.Model != "qwen3.6" || st.Count != 1 || st.At.IsZero() {
		t.Fatalf("status %+v", st)
	}
	if !ClearPeerPromptLayer() {
		t.Fatal("clearing a layer in force reported nothing to clear")
	}
	if got := EffectivePromptText(tierPlainKey, tierPlain); got != "local words" {
		t.Fatalf("after clearing: %q, want the local override back", got)
	}
	if PeerPromptLayer().Source != "" || ClearPeerPromptLayer() {
		t.Fatal("the layer is still reported after clearing")
	}
}

// The layer is kept in the store, so a restart with the peer down still has
// the last wording it sent; a missing store empties it.
func TestPeerWordingSurvivesARestart(t *testing.T) {
	db := jsonStore{}
	tierTestBlocks(t)
	SetPromptOverrideDB(db)
	SetPeerPromptLayer("den", "qwen3.6", time.Now(), map[string]string{tierPlainKey: "peer words"})

	SetPromptOverrideDB(nil)
	if _, ok := PromptOverride(tierPlainKey); ok {
		t.Fatal("the layer outlived its store")
	}
	SetPromptOverrideDB(db)
	if got, _ := PromptOverride(tierPlainKey); got != "peer words" {
		t.Fatalf("after reload: %q, want the kept peer wording", got)
	}
	if st := PeerPromptLayer(); st.Source != "den" || st.Count != 1 {
		t.Fatalf("status after reload %+v", st)
	}
}

// Only keys registered here are taken, an operator's own rules are never taken
// whatever a peer sends, and a text naming a placeholder the block cannot fill
// is left out.
func TestPeerWordingKeepsOnlyWhatFits(t *testing.T) {
	tierTestBlocks(t)
	style := BuiltinStyleRules()
	if len(style) == 0 {
		t.Fatal("no builtin style rules to test against")
	}
	res := SetPeerPromptLayer("den", "m", time.Now(), map[string]string{
		tierPlainKey:          "peer plain",
		tierFillKey:           "[Budget: {rounds} rounds, and {secret} too, with {{tool_list}}.]",
		"test.not_registered": "anything",
		style[0].Key:          "the peer's house style",
	})
	if res.Kept != 1 || len(res.Unknown) != 2 || len(res.Broken) != 1 || res.Broken[0] != tierFillKey {
		t.Fatalf("result %+v, want 1 kept, 2 unknown, the fill block broken", res)
	}
	if _, ok := PromptOverride("test.not_registered"); ok {
		t.Error("an unregistered key was taken")
	}
	if got := EffectivePromptText(style[0].Key, style[0].Text); got != style[0].Text {
		t.Errorf("a peer reworded this operator's style rule: %q", got)
	}
	if got := EffectivePromptText(tierFillKey, tierFill); got != tierFill {
		t.Errorf("a placeholder-breaking text was taken: %q", got)
	}
	// Dropping a placeholder is not breaking one: the text is still fillable.
	res = SetPeerPromptLayer("den", "m", time.Now(), map[string]string{
		tierFillKey: "[Budget: up to {rounds} rounds, then answer from what you have.]",
	})
	if res.Kept != 1 || len(res.Broken) != 0 {
		t.Fatalf("a fillable text was refused: %+v", res)
	}
	// The same wording again is not a change.
	res = SetPeerPromptLayer("den", "m", time.Now(), map[string]string{
		tierFillKey: "[Budget: up to {rounds} rounds, then answer from what you have.]",
	})
	if res.Changed {
		t.Error("an identical refresh reported a change")
	}
}

// What this machine serves to a borrowing peer: its own overrides of
// registered blocks, and nothing else. Not the style rules, not a tier's own
// text, not wording it got from a peer of its own.
func TestSharedOverridesAreRegisteredBlocksOnly(t *testing.T) {
	tierTestBlocks(t)
	style := BuiltinStyleRules()[0]
	SetPromptOverride(tierPlainKey, "tuned here")
	SetPromptOverride(style.Key, "this operator's style")
	SetPromptOverride("test.not_registered", "loose")
	SetPromptTierOverride(TierWorker, tierFillKey, "worker only", "m")
	SetPeerPromptLayer("upstream", "m", time.Now(), map[string]string{tierFillKey: "relayed"})

	got := SharedBlockOverrides()
	if len(got) != 1 || got[tierPlainKey] != "tuned here" {
		t.Fatalf("served %v, want only the local override of the registered block", got)
	}
}

// A governing peer's wording outranks a tier's own text left on this machine:
// what both tiers read is the peer's, and LocalPromptOverride still shows
// this machine's own.
func TestPeerWordingBeatsLeftoverTierText(t *testing.T) {
	tierTestBlocks(t)
	SetPromptOverride(tierPlainKey, "local words")
	SetPromptTierOverrideBy(TierWorker, tierPlainKey, "worker's own", "local/qwen", "tuned")
	SetPeerPromptLayer("den", "qwen3.6", time.Now(), map[string]string{tierPlainKey: "peer words"})
	t.Cleanup(func() { ClearPeerPromptLayer() })
	if got := EffectivePromptTextFor(TierWorker, tierPlainKey, "shipped"); got != "peer words" {
		t.Fatalf("the worker reads %q, want the peer's wording", got)
	}
	if got, _ := LocalPromptOverride(tierPlainKey); got != "local words" {
		t.Fatalf("LocalPromptOverride = %q", got)
	}
}
