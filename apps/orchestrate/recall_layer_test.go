package orchestrate

import (
	"reflect"
	"strings"
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

// TestRecallLayerSetPrunesGatedLayers: layers this agent can't serve are
// removed BEFORE the k budget splits, so a gated layer no longer silently
// shrinks every live layer's share (the pre-A/B drift: k=8 across 4 requested
// but 2 live layers handed each live layer 2 hits instead of 4).
func TestRecallLayerSetPrunesGatedLayers(t *testing.T) {
	ct := &chatTurn{agent: AgentRecord{DisableExplicit: true}} // pinned gated, findings live
	got := ct.recallLayerSet(map[string]any{})
	want := map[string]bool{"finding": true, "knowledge": true, "history": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected pinned pruned, got %v", got)
	}
	// The budget now divides by the LIVE count.
	if per := recallPerLayerBudget(map[string]any{"k": float64(9)}, len(got)); per != 3 {
		t.Fatalf("k=9 over 3 live layers should give 3/layer, got %d", per)
	}

	ct = &chatTurn{agent: AgentRecord{DisableInferred: true}} // findings gated
	got = ct.recallLayerSet(map[string]any{})
	want = map[string]bool{"pinned": true, "knowledge": true, "history": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected finding pruned, got %v", got)
	}
	// Caller names ONLY the gated layer → empty set (recallSearch turns this
	// into a "layer disabled" error instead of a hollow no-match).
	if got := ct.recallLayerSet(map[string]any{"layer": "finding"}); len(got) != 0 {
		t.Fatalf("gated single-layer request should yield empty set, got %v", got)
	}
}

// TestRecallSearchDisabledLayerErrors: naming a layer the agent has disabled
// must error explicitly — a plain "no matches" would read as a genuine miss.
func TestRecallSearchDisabledLayerErrors(t *testing.T) {
	ct := &chatTurn{agent: AgentRecord{DisableExplicit: true}}
	if _, err := ct.recallSearch("anything", map[string]any{"layer": "pinned"}); err == nil {
		t.Fatal("expected an error for a disabled layer, got nil")
	}
}

// TestRecallChunkScope: a single-provenance recall scopes the vector search so
// the other provenance's chunks can't occupy candidate slots that get
// discarded (legacy memory_search depth via ChunkScopeDerivedOnly).
func TestRecallChunkScope(t *testing.T) {
	cases := []struct {
		layers map[string]bool
		want   ChunkScope
	}{
		{map[string]bool{"finding": true}, ChunkScopeDerivedOnly},
		{map[string]bool{"finding": true, "history": true}, ChunkScopeDerivedOnly},
		{map[string]bool{"knowledge": true}, ChunkScopeCuratedOnly},
		{map[string]bool{"finding": true, "knowledge": true}, ChunkScopeAll},
		{map[string]bool{"pinned": true, "finding": true, "knowledge": true, "history": true}, ChunkScopeAll},
	}
	for _, c := range cases {
		if got := recallChunkScope(c.layers); got != c.want {
			t.Errorf("recallChunkScope(%v)=%v want %v", c.layers, got, c.want)
		}
	}
}

// TestRecallNoMatchMessageNamesSearchedLayers: with a layer pruned by the
// agent's gates, the no-match text must not claim a sweep of layers that were
// never searched.
func TestRecallNoMatchMessageNamesSearchedLayers(t *testing.T) {
	msg := recallNoMatchMessage(map[string]bool{"finding": true, "knowledge": true, "history": true})
	if strings.Contains(msg, "pinned") {
		t.Fatalf("no-match message claims pinned was searched: %q", msg)
	}
	for _, want := range []string{"findings", "knowledge", "conversation history"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("no-match message missing %q: %q", want, msg)
		}
	}
}
