package core

import (
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// The override round-trip is what makes editing a block on the Prompts page
// actually change what agents receive: EffectivePromptText returns the override
// when set, the default otherwise, and clearing reverts.
func TestPromptOverrideRoundTrip(t *testing.T) {
	SetPromptOverrideDB(&DBase{Store: kvlite.MemStore()})
	t.Cleanup(func() { SetPromptOverrideDB(nil) })

	const key = "test.block.roundtrip"

	// No override → effective text is the default.
	if got := EffectivePromptText(key, "DEFAULT"); got != "DEFAULT" {
		t.Fatalf("no override: got %q, want DEFAULT", got)
	}
	if _, ok := PromptOverride(key); ok {
		t.Fatal("expected no override initially")
	}

	// Set an override → effective text is the override.
	SetPromptOverride(key, "CUSTOM")
	if s, ok := PromptOverride(key); !ok || s != "CUSTOM" {
		t.Fatalf("PromptOverride after set: %q ok=%v", s, ok)
	}
	if got := EffectivePromptText(key, "DEFAULT"); got != "CUSTOM" {
		t.Fatalf("with override: got %q, want CUSTOM", got)
	}

	// Clear → back to the default.
	ClearPromptOverride(key)
	if _, ok := PromptOverride(key); ok {
		t.Fatal("expected override cleared")
	}
	if got := EffectivePromptText(key, "DEFAULT"); got != "DEFAULT" {
		t.Fatalf("after clear: got %q, want DEFAULT", got)
	}
}

// With no override DB wired, everything falls through to the default (a fresh
// process before SetPromptOverrideDB runs, and the safe path in tests).
func TestEffectivePromptTextNoDB(t *testing.T) {
	SetPromptOverrideDB(nil)
	if got := EffectivePromptText("anything", "DEF"); got != "DEF" {
		t.Fatalf("no DB: got %q, want DEF", got)
	}
}
