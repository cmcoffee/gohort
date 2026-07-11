package orchestrate

import (
	"reflect"
	"testing"
)

// TestRecallCorpusOnly: the corpus-only cap fires only when BOTH memory-write
// layers are off (the "answer strictly from authoritative sources" config).
func TestRecallCorpusOnly(t *testing.T) {
	cases := []struct {
		explicit, inferred, want bool
	}{
		{true, true, true},   // KB reader — capped
		{true, false, false}, // keeps findings — not capped
		{false, true, false}, // keeps pinned facts — not capped
		{false, false, false},
	}
	for _, c := range cases {
		ct := &chatTurn{agent: AgentRecord{DisableExplicit: c.explicit, DisableInferred: c.inferred}}
		if got := ct.recallCorpusOnly(); got != c.want {
			t.Errorf("recallCorpusOnly(explicit=%v,inferred=%v)=%v want %v", c.explicit, c.inferred, got, c.want)
		}
	}
}

// TestRecallLayerSetCap: a corpus-only agent is pinned to [knowledge] no matter
// what `layer` the model passes — the grounding guarantee can't be widened by a
// tool argument.
func TestRecallLayerSetCap(t *testing.T) {
	ct := &chatTurn{agent: AgentRecord{DisableExplicit: true, DisableInferred: true}}
	for _, layer := range []string{"", "all", "history", "finding", "pinned", "knowledge", "garbage"} {
		got := ct.recallLayerSet(map[string]any{"layer": layer})
		if !reflect.DeepEqual(got, map[string]bool{"knowledge": true}) {
			t.Errorf("corpus-only agent with layer=%q got %v, want {knowledge:true}", layer, got)
		}
	}
}

// TestRecallLayerSetParam: an ordinary agent honors the `layer` arg, defaulting
// to all four layers, tolerating the "findings" plural, and falling back to all
// on an unrecognized value (never silently narrowing to the wrong single layer).
func TestRecallLayerSetParam(t *testing.T) {
	ct := &chatTurn{agent: AgentRecord{}} // both layers on → not capped
	all := map[string]bool{"pinned": true, "finding": true, "knowledge": true, "history": true}

	cases := []struct {
		layer string
		want  map[string]bool
	}{
		{"", all},
		{"all", all},
		{"knowledge", map[string]bool{"knowledge": true}},
		{"finding", map[string]bool{"finding": true}},
		{"findings", map[string]bool{"finding": true}}, // plural tolerated
		{"pinned", map[string]bool{"pinned": true}},
		{"history", map[string]bool{"history": true}},
		{"KNOWLEDGE", map[string]bool{"knowledge": true}}, // case-insensitive
		{"nonsense", all},                                 // unknown → all, not empty
	}
	for _, c := range cases {
		got := ct.recallLayerSet(map[string]any{"layer": c.layer})
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("recallLayerSet(layer=%q)=%v want %v", c.layer, got, c.want)
		}
	}
}
