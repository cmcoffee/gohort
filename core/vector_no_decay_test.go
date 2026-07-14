package core

import (
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

// TestSearchRanksOnRelevanceNotAge pins the no-decay design (see the note in
// vector_store.go): a strong-match OLD chunk must outrank a weaker FRESH one.
// The removed 180d sort-key decay buried a year-old authoritative upload at
// ~x0.25, letting fresh derived chatter push it out of the candidate pool.
func TestSearchRanksOnRelevanceNotAge(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	oldDate := time.Now().AddDate(-2, 0, 0).Format(time.RFC3339)
	db.Set(EmbeddedChunks, "old-strong", EmbeddedChunk{
		ID: "old-strong", Source: "kb", ReportID: "r1", Section: "## Policy",
		Text: "the authoritative policy", Vector: []float32{1, 0}, Date: oldDate,
	})
	db.Set(EmbeddedChunks, "fresh-weak", EmbeddedChunk{
		ID: "fresh-weak", Source: "kb", ReportID: "r2", Section: "## Chatter",
		Text: "recent tangential note", Vector: []float32{0.7, 0.72}, Date: time.Now().Format(time.RFC3339),
	})

	hits := SearchChunks(db, []float32{1, 0}, 2)
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(hits))
	}
	if hits[0].ID != "old-strong" {
		t.Fatalf("stronger old match must rank first, got %q (score %.2f) over %q",
			hits[0].ID, hits[0].Score, hits[1].ID)
	}
}
