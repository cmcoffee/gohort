package replyguard

import (
	"encoding/json"
	"strings"
	"testing"
)

// memStore is a JSON round-tripping map, the way the real store encodes.
type memStore map[string]map[string][]byte

func (m memStore) Get(table, key string, out interface{}) bool {
	b, ok := m[table][key]
	if !ok {
		return false
	}
	return json.Unmarshal(b, out) == nil
}
func (m memStore) Set(table, key string, v interface{}) {
	if m[table] == nil {
		m[table] = map[string][]byte{}
	}
	b, _ := json.Marshal(v)
	m[table][key] = b
}
func (m memStore) Keys(table string) []string {
	var out []string
	for k := range m[table] {
		out = append(out, k)
	}
	return out
}

// A model's own setting wins over the guard's default, the default over On,
// and clearing a model's setting puts it back on the default.
func TestAModelsSettingWinsOverTheDefault(t *testing.T) {
	SetStore(memStore{})
	defer SetStore(nil)
	Register(Guard{ID: "t-guard", Name: "Test"})
	if m := ModeFor("t-guard", "gemini-2.5-flash"); m != On {
		t.Errorf("unset: %q, want on", m)
	}
	if err := SetMode("t-guard", AllModels, Shadow); err != nil {
		t.Fatal(err)
	}
	if err := SetMode("t-guard", "Models/Gemini-2.5-Flash", Off); err != nil {
		t.Fatal(err)
	}
	if m := ModeFor("t-guard", "gemini-2.5-flash"); m != Off {
		t.Errorf("the model's own setting (named any which way) wins: %q", m)
	}
	if m := ModeFor("t-guard", "qwen3.6"); m != Shadow || DefaultMode("t-guard") != Shadow {
		t.Errorf("another model follows the default: %q", m)
	}
	if err := SetMode("t-guard", "gemini-2.5-flash", ""); err != nil {
		t.Fatal(err)
	}
	if m := ModeFor("t-guard", "gemini-2.5-flash"); m != Shadow {
		t.Errorf("cleared, the model follows the default again: %q", m)
	}
	if SetMode("no-such-guard", AllModels, Off) == nil || SetMode("t-guard", AllModels, "sometimes") == nil {
		t.Error("an unknown guard or mode is refused")
	}
}

// Firings are tallied per guard and model, corrected apart from shadow, with
// the latest replies' tails kept and capped.
func TestFiringsAreTalliedWithRecentSamples(t *testing.T) {
	SetStore(memStore{})
	defer SetStore(nil)
	for i := 0; i < maxSamples+3; i++ {
		Record("t-guard", "qwen3.6", strings.Repeat("x", 2000)+" and", i%2 == 0)
	}
	st := Stats()
	if len(st) != 1 || st[0].Acted+st[0].Shadowed != maxSamples+3 || st[0].Shadowed == 0 {
		t.Fatalf("tallied per guard and model, both kinds: %+v", st)
	}
	if len(st[0].Samples) != maxSamples || !strings.HasSuffix(st[0].Samples[0].Text, " and") || len([]rune(st[0].Samples[0].Text)) > maxSampleChars+1 {
		t.Errorf("the last %d replies' tails are kept: %d samples, first %q", maxSamples, len(st[0].Samples), st[0].Samples[0].Text[:20])
	}
	SetStore(nil)
	Record("t-guard", "qwen3.6", "x", true)
	if Stats() != nil {
		t.Error("with no store nothing is recorded")
	}
}
