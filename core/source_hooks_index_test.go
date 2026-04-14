package core

import (
	"fmt"
	"strings"
	"testing"
)

// testCacheDB returns an in-memory kvlite-backed Database wired as the
// hookCacheDB for the duration of a test. The caller should not assume
// persistence across tests — each call returns a fresh store.
func testCacheDB(t *testing.T) Database {
	t.Helper()
	db := OpenCache()
	SetHookCacheDB(db)
	t.Cleanup(func() {
		SetHookCacheDB(nil)
	})
	return db
}

// buildCachedResult formats sources as the numbered block format that
// parseCachedSources expects. Mirrors the format produced by real
// source hook API responses.
func buildCachedResult(docs []indexedDoc) string {
	var sb strings.Builder
	for i, d := range docs {
		fmt.Fprintf(&sb, "%d. %s\n   %s\n   %s\n\n", i+1, d.Title, d.URL, d.Snippet)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func TestIndexHookDocs_basicIngestAndSearch(t *testing.T) {
	testCacheDB(t)

	result := buildCachedResult([]indexedDoc{
		{Title: "Moral crumple zones in automation liability cases", URL: "https://example.com/a", Snippet: "automation liability doctrine crumple zone jurisprudence accountability"},
		{Title: "Algorithmic accountability gap in autonomous vehicles", URL: "https://example.com/b", Snippet: "autonomous vehicle automation liability algorithmic decision accountability"},
		{Title: "Product liability for AI medical devices", URL: "https://example.com/c", Snippet: "product liability AI medical device automation regulation FDA approval"},
	})
	indexHookDocs("CourtListener", result)

	// Query sharing multiple tokens with the indexed docs should hit.
	hits := searchHookDocs("CourtListener", "automation liability accountability algorithmic", 10)
	if len(hits) < 3 {
		t.Errorf("expected 3 hits for broad token overlap, got %d", len(hits))
	}
}

func TestSearchHookDocs_shardedByHook(t *testing.T) {
	testCacheDB(t)

	// Index distinct content under two hooks.
	indexHookDocs("CourtListener", buildCachedResult([]indexedDoc{
		{Title: "Automation liability jurisprudence", URL: "https://court.example/a", Snippet: "algorithmic accountability vehicle automation"},
		{Title: "Automation liability in product cases", URL: "https://court.example/b", Snippet: "automation liability vehicle accountability"},
		{Title: "Algorithmic vehicle accountability", URL: "https://court.example/c", Snippet: "algorithmic accountability automation vehicle"},
	}))
	indexHookDocs("PubMed", buildCachedResult([]indexedDoc{
		{Title: "Automation liability in anesthesia delivery", URL: "https://pubmed.example/x", Snippet: "automation anesthesia liability delivery accountability algorithmic"},
	}))

	// Search CourtListener only — must NOT return PubMed docs even
	// though they share tokens. Sharding is verified by URL prefix.
	hits := searchHookDocs("CourtListener", "automation liability accountability algorithmic vehicle", 10)
	if len(hits) == 0 {
		t.Fatalf("expected CourtListener hits, got 0")
	}
	for _, h := range hits {
		if !strings.HasPrefix(h.URL, "https://court.example/") {
			t.Errorf("CourtListener search returned cross-hook doc: %q", h.URL)
		}
	}
}

func TestSearchHookDocs_thresholds(t *testing.T) {
	testCacheDB(t)

	// Only 2 documents indexed — below the minDocsForMatch=3
	// global threshold. Should return no results even if tokens
	// match.
	indexHookDocs("CourtListener", buildCachedResult([]indexedDoc{
		{Title: "Automation liability", URL: "https://a.example/1", Snippet: "automation liability accountability algorithmic"},
		{Title: "Automation doctrine", URL: "https://a.example/2", Snippet: "automation doctrine accountability"},
	}))
	hits := searchHookDocs("CourtListener", "automation liability accountability", 10)
	if len(hits) != 0 {
		t.Errorf("expected 0 hits (below minDocsForMatch), got %d", len(hits))
	}

	// Single-token overlap per doc — below minTokensPerDoc=2.
	// Should return no results.
	indexHookDocs("CourtListener", buildCachedResult([]indexedDoc{
		{Title: "Aaaaa", URL: "https://b.example/1", Snippet: "unique1 filler1 filler2"},
		{Title: "Bbbbb", URL: "https://b.example/2", Snippet: "unique2 filler3 filler4"},
		{Title: "Ccccc", URL: "https://b.example/3", Snippet: "unique3 filler5 filler6"},
	}))
	hits = searchHookDocs("CourtListener", "unique1 unrelated query words", 10)
	if len(hits) != 0 {
		t.Errorf("expected 0 hits (single token overlap), got %d", len(hits))
	}
}

func TestIndexHookDocs_dedupesReindex(t *testing.T) {
	testCacheDB(t)

	result := buildCachedResult([]indexedDoc{
		{Title: "Automation liability", URL: "https://dedupe.example/a", Snippet: "automation liability accountability"},
	})
	// Index the same content twice.
	indexHookDocs("CourtListener", result)
	indexHookDocs("CourtListener", result)

	// Verify the token index has the docID only once by checking
	// the stored list length for a token it should contain.
	hookCacheMu.RLock()
	db := hookCacheDB
	hookCacheMu.RUnlock()
	var list docIDList
	if !db.Get(hookTokenIndexBucket, tokenKey("CourtListener", "automation"), &list) {
		t.Fatalf("expected token entry for 'automation' after indexing")
	}
	if len(list.IDs) != 1 {
		t.Errorf("expected 1 docID after double-indexing, got %d: %v", len(list.IDs), list.IDs)
	}
}

func TestParseCachedSources_standardFormat(t *testing.T) {
	input := `1. First Title
   https://example.com/1
   First snippet

2. Second Title
   https://example.com/2
   Second snippet line one
   second snippet line two

3. Third Title
   https://example.com/3
   Third snippet`

	docs := parseCachedSources(input)
	if len(docs) != 3 {
		t.Fatalf("expected 3 docs, got %d", len(docs))
	}
	if docs[0].Title != "First Title" {
		t.Errorf("doc 0 title: got %q", docs[0].Title)
	}
	if docs[1].URL != "https://example.com/2" {
		t.Errorf("doc 1 URL: got %q", docs[1].URL)
	}
	if !strings.Contains(docs[1].Snippet, "line one") {
		t.Errorf("doc 1 snippet should contain joined lines, got %q", docs[1].Snippet)
	}
}

func TestDocTokenize_basics(t *testing.T) {
	tokens := docTokenize("The AI automation liability in AI cases: algorithmic accountability in EU and US jurisdictions!")
	// Should drop stopwords: "the", "in", "and"
	// Should dedupe: "ai" and "in" both appear twice
	// Should KEEP: acronyms like "ai", "eu", "us" — they're the most
	// distinctive tokens in technical research queries
	expected := map[string]bool{
		"ai":             true,
		"eu":             true,
		"us":             true,
		"automation":     true,
		"liability":      true,
		"cases":          true,
		"algorithmic":    true,
		"accountability": true,
		"jurisdictions":  true,
	}
	for want := range expected {
		if !tokens[want] {
			t.Errorf("expected token %q in tokenization, missing (got: %v)", want, tokens)
		}
	}
	// Verify stopwords were dropped.
	for _, stop := range []string{"the", "in", "and"} {
		if tokens[stop] {
			t.Errorf("stopword %q should have been dropped", stop)
		}
	}
}
