package prompts

import (
	"encoding/json"
	"testing"
)

// jsonStore round-trips through JSON, as the real store encodes structs.
type jsonStore map[string][]byte

func (m jsonStore) Get(table, key string, out interface{}) bool {
	b, ok := m[table+"/"+key]
	if !ok {
		return false
	}
	return json.Unmarshal(b, out) == nil
}
func (m jsonStore) Set(table, key string, v interface{}) {
	b, _ := json.Marshal(v)
	m[table+"/"+key] = b
}
func (m jsonStore) Unset(table, key string) { delete(m, table+"/"+key) }

// A tier's own text wins for that tier only; the other tier follows the
// all-tiers override; clearing puts the tier back on it; an unknown tier is
// all tiers. The model a tier ran when its text was set is remembered.
func TestTierOverridesResolve(t *testing.T) {
	SetPromptOverrideDB(jsonStore{})
	t.Cleanup(func() { SetPromptOverrideDB(nil) })
	const key = "test.block"
	if got := EffectivePromptTextFor(TierWorker, key, "shipped"); got != "shipped" {
		t.Fatalf("no overrides: %q", got)
	}
	SetPromptOverride(key, "all tiers")
	SetPromptTierOverride(TierWorker, key, "worker words", "qwen3.6")
	if got := EffectivePromptTextFor(TierWorker, key, "shipped"); got != "worker words" {
		t.Fatalf("worker: %q", got)
	}
	if got := EffectivePromptTextFor(TierLead, key, "shipped"); got != "all tiers" {
		t.Fatalf("lead: %q", got)
	}
	if got := EffectivePromptTextFor("", key, "shipped"); got != "all tiers" {
		t.Fatalf("no tier: %q", got)
	}
	if got := EffectivePromptTextFor("bogus", key, "shipped"); got != "all tiers" {
		t.Fatalf("unknown tier: %q", got)
	}
	if !TierOverrideStale(TierWorker, key, "qwen3.7") || TierOverrideStale(TierWorker, key, "QWEN3.6") || TierOverrideStale(TierLead, key, "x") {
		t.Fatal("staleness wrong")
	}
	ClearPromptTierOverride(TierWorker, key)
	if got := EffectivePromptTextFor(TierWorker, key, "shipped"); got != "all tiers" {
		t.Fatalf("after clear: %q", got)
	}
	SetPromptTierOverride(TierLead, key, "lead words", "")
	SetPromptTierOverride(TierLead, key, "  ", "")
	if _, ok := PromptTierOverride(TierLead, key); ok {
		t.Fatal("empty text did not clear")
	}
	if _, ok := PromptOverride(key); !ok {
		t.Fatal("a tier write touched the all-tiers override")
	}
}
