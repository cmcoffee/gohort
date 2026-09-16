package core

import (
	"context"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// The hybrid merge must keep the STRONGER signal for a chunk both halves
// found. It used to keep whichever copy came first (the vector one), so a
// chunk with cosine 0.30 and a keyword score of 0.80 left the merge at 0.30
// and was dropped by the 0.35 relevance floor.
func TestHybridMergeKeepsTheHigherScore(t *testing.T) {
	vec := []SearchHit{{ID: "x", Score: 0.30}, {ID: "y", Score: 0.50}}
	kw := []SearchHit{{ID: "x", Score: 0.80}}
	m := MergeHitsByScore(vec, kw, 5)
	if len(m) != 2 {
		t.Fatalf("expected 2 merged hits, got %d", len(m))
	}
	if m[0].ID != "x" || m[0].Score != 0.80 {
		t.Fatalf("x must carry its keyword score 0.80 and rank first, got %q at %.2f", m[0].ID, m[0].Score)
	}
	if kept := aboveFloor(m); len(kept) != 2 {
		t.Fatalf("both hits clear the floor once the higher score is kept, got %d", len(kept))
	}
	// Order of the arguments must not matter.
	m = MergeHitsByScore(kw, vec, 5)
	if m[0].ID != "x" || m[0].Score != 0.80 {
		t.Fatalf("reversed args: got %q at %.2f", m[0].ID, m[0].Score)
	}
}

// Stamped chunks reassemble in ingest order, whatever their headings.
func TestAssemblyFollowsTheOrdStamp(t *testing.T) {
	var chunks []EmbeddedChunk
	for i, s := range []string{"Overview", "Background", "Conclusion"} {
		chunks = append(chunks, EmbeddedChunk{ID: UUIDv4(), Title: "Report", Section: s, Text: s + " body", Ord: i + 1})
	}
	// Hand them over shuffled.
	chunks[0], chunks[2] = chunks[2], chunks[0]
	out := AssembleChunkDoc(chunks, 0)
	want := []string{"# Report", "## Overview", "## Background", "## Conclusion"}
	last := -1
	for _, w := range want {
		idx := strings.Index(out, w)
		if idx < 0 || idx < last {
			t.Fatalf("expected %q in document order, got:\n%s", w, out)
		}
		last = idx
	}
}

// Legacy rows (no Ord) fall back to a natural sort: "(part 10)" after
// "(part 2)", and nested "(part N/M)" splits ordered numerically too.
func TestLegacyAssemblyOrdersPartsNumerically(t *testing.T) {
	var chunks []EmbeddedChunk
	for _, s := range []string{"Guide (part 10)", "Guide (part 2)", "Guide (part 1)", "Guide (part 11)", "Guide (part 2) (part 2/2)", "Guide (part 2) (part 1/2)"} {
		chunks = append(chunks, EmbeddedChunk{ID: UUIDv4(), Section: s, Text: s})
	}
	SortChunksForAssembly(chunks)
	var got []string
	for _, c := range chunks {
		got = append(got, c.Section)
	}
	want := "Guide (part 1) | Guide (part 2) | Guide (part 2) (part 1/2) | Guide (part 2) (part 2/2) | Guide (part 10) | Guide (part 11)"
	if strings.Join(got, " | ") != want {
		t.Fatalf("legacy order wrong:\n got %s\nwant %s", strings.Join(got, " | "), want)
	}
}

// One legacy row in a document switches the whole set to the fallback, so a
// stamped chunk never compares its Ord against a 0.
func TestMixedStampAndLegacyUsesFallback(t *testing.T) {
	chunks := []EmbeddedChunk{
		{ID: "b", Section: "Zeta", Ord: 1},
		{ID: "a", Section: "Alpha", Ord: 0},
	}
	SortChunksForAssembly(chunks)
	if chunks[0].Section != "Alpha" {
		t.Fatalf("mixed set must natural-sort by section, got %q first", chunks[0].Section)
	}
}

// Ingest stamps every row, in order, including the sub-pieces the embed
// fallback produces — so a re-read of an unstructured upload with more than
// nine parts comes back in the order it was written.
func TestIngestStampsOrdInDocumentOrder(t *testing.T) {
	// Another test in the package may leave a fake embedding endpoint
	// configured; this one is about the stamp, not the vector.
	prev := GetEmbeddingConfig()
	defer SetEmbeddingConfig(prev)
	SetEmbeddingConfig(EmbeddingConfig{})
	db := &DBase{Store: kvlite.MemStore()}
	var b strings.Builder
	for i := 1; i <= 12; i++ {
		b.WriteString("## Section ")
		b.WriteString(strings.Repeat("x", i)) // distinct headings that do not sort numerically
		b.WriteString("\n\npara ")
		b.WriteString(strings.Repeat("y", i))
		b.WriteString("\n\n")
	}
	IngestReportTitled(context.Background(), db, "collection:t", "doc-1", "Twelve", b.String(), "")
	chunks := ChunksWhere(db, func(c EmbeddedChunk) bool { return c.ReportID == "doc-1" })
	if len(chunks) != 12 {
		t.Fatalf("expected 12 chunks, got %d", len(chunks))
	}
	SortChunksForAssembly(chunks)
	for i, c := range chunks {
		if c.Ord != i+1 {
			t.Fatalf("chunk %d has Ord %d", i, c.Ord)
		}
		if !strings.HasSuffix(c.Section, strings.Repeat("x", i+1)) {
			t.Fatalf("chunk %d out of order: %q", i, c.Section)
		}
	}
	if got := AssembleChunkDoc(chunks, 0); !strings.HasPrefix(got, "# Twelve\n\n## Section x\n") {
		t.Fatalf("assembled doc must open with the title then the first section, got:\n%.80s", got)
	}
}

// The keyword and substring scans see the parent document's Title, so a
// query that names the document finds chunks whose own text never does.
func TestLexicalSearchMatchesTheTitle(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(EmbeddedChunks, "c1", EmbeddedChunk{
		ID: "c1", Source: "collection:t", ReportID: "r1", Title: "OPNsense firewall guide",
		Section: "## Overview", Text: "This document covers installation and rule ordering.",
	})
	db.Set(EmbeddedChunks, "c2", EmbeddedChunk{
		ID: "c2", Source: "collection:t", ReportID: "r2", Title: "Bread recipes",
		Section: "## Overview", Text: "Knead, prove, bake.",
	})
	all := func(EmbeddedChunk) bool { return true }
	hits := SearchChunksKeywordByPredicate(db, all, "opnsense", 5)
	if len(hits) != 1 || hits[0].ID != "c1" {
		t.Fatalf("keyword search must match the title, got %+v", hits)
	}
	hits = SearchChunksSubstringByPredicate(db, all, "opnsense", 5)
	if len(hits) != 1 || hits[0].ID != "c1" {
		t.Fatalf("substring search must match the title, got %+v", hits)
	}
}

// The embed prompt carries the title once, and not when it merely repeats
// the section (an upload's first section IS its name).
func TestEmbedHeaderCarriesTheTitleOnce(t *testing.T) {
	if got := embedHeader("Guide", "## Overview"); got != "Guide\n## Overview" {
		t.Fatalf("got %q", got)
	}
	if got := embedHeader("", "## Overview"); got != "## Overview" {
		t.Fatalf("empty title must leave the section alone, got %q", got)
	}
	if got := embedHeader("Overview", "Overview"); got != "Overview" {
		t.Fatalf("title equal to section must not repeat, got %q", got)
	}
}
