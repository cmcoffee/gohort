package core

import (
	"context"
	"strings"
	"testing"
)

// TestFactVectorVersioning: cached fact vectors are only trusted inside the
// embedding space they were computed in. Legacy unstamped rows grandfather
// into the current version (cheap stamp, no embed); rows stamped under a
// DIFFERENT space are never cosine-compared — with the embedder unavailable
// (test default) they yield nil rather than a cross-space vector.
func TestFactVectorVersioning(t *testing.T) {
	db := memDB(t)
	ns := "agent:test"
	ver := EmbedVersion()

	current := MemoryFact{Namespace: ns, ID: "cur", Note: "n", Vector: []float32{1, 2}, VectorModel: ver}
	db.Set(MemoryFactsTable, factDBKey(ns, current.ID), current)
	if v := factVector(context.Background(), db, current); len(v) != 2 {
		t.Fatalf("current-version vector should be served from cache; got %v", v)
	}

	legacy := MemoryFact{Namespace: ns, ID: "leg", Note: "n", Vector: []float32{3, 4}}
	db.Set(MemoryFactsTable, factDBKey(ns, legacy.ID), legacy)
	if v := factVector(context.Background(), db, legacy); len(v) != 2 {
		t.Fatalf("legacy unstamped vector should be grandfathered; got %v", v)
	}
	var stamped MemoryFact
	if !db.Get(MemoryFactsTable, factDBKey(ns, legacy.ID), &stamped) || stamped.VectorModel != ver {
		t.Fatalf("grandfathering must persist the current stamp; got %q want %q", stamped.VectorModel, ver)
	}

	stale := MemoryFact{Namespace: ns, ID: "old", Note: "n", Vector: []float32{5, 6}, VectorModel: "other-model@elsewhere"}
	db.Set(MemoryFactsTable, factDBKey(ns, stale.ID), stale)
	if v := factVector(context.Background(), db, stale); v != nil {
		t.Fatalf("stale-space vector must not be served (re-embed or nothing); got %v", v)
	}
}

// TestChunkVectorComparable: chunk search must skip vectors from a different
// embedding model instead of cosine-comparing across spaces; empty model on
// either side is grandfathered (single-model backends can't be told apart).
func TestChunkVectorComparable(t *testing.T) {
	prev := GetEmbeddingConfig()
	defer SetEmbeddingConfig(prev)
	SetEmbeddingConfig(EmbeddingConfig{Enabled: true, Model: "nomic-v2", Endpoint: "http://x"})

	q := []float32{1, 2, 3}
	cases := []struct {
		name  string
		chunk EmbeddedChunk
		want  bool
	}{
		{"dim mismatch", EmbeddedChunk{Vector: []float32{1, 2}, Model: "nomic-v2"}, false},
		{"same model", EmbeddedChunk{Vector: []float32{1, 2, 3}, Model: "nomic-v2"}, true},
		{"different model", EmbeddedChunk{Vector: []float32{1, 2, 3}, Model: "bge-small"}, false},
		{"legacy empty model", EmbeddedChunk{Vector: []float32{1, 2, 3}}, true},
	}
	for _, c := range cases {
		if got := chunkVectorComparable(&c.chunk, q); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	// No configured model name (llama.cpp style): everything dimension-valid passes.
	SetEmbeddingConfig(EmbeddingConfig{Enabled: true, Endpoint: "http://x"})
	if !chunkVectorComparable(&EmbeddedChunk{Vector: []float32{1, 2, 3}, Model: "whatever"}, q) {
		t.Error("empty current model must grandfather stamped chunks")
	}
}

// TestResolveOperatingNotesSeedCap: a Builder/wizard-supplied seed honors the
// same rune cap update_notes enforces — it previously bypassed the bound.
func TestResolveOperatingNotesSeedCap(t *testing.T) {
	db := memDB(t)
	long := strings.Repeat("x", OperatingNotesCap+500)
	n := ResolveOperatingNotes(db, "agent:test", long)
	if got := len([]rune(n.Text)); got > OperatingNotesCap {
		t.Fatalf("seed not capped: %d runes > cap %d", got, OperatingNotesCap)
	}
	short := ResolveOperatingNotes(db, "agent:test", "hello")
	if short.Text != "hello" {
		t.Fatalf("short seed mangled: %q", short.Text)
	}
}
