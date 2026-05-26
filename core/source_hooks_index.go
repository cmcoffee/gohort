package core

import (
	"fmt"
	"sort"
	"strings"
)

// This file implements tier-2 document-level search over cached source
// hook results. Tier 1 is the exact-key cache in source_hooks.go that
// requires identical normalized queries to hit. Tier 2 is an inverted
// index over the DOCUMENTS inside cached results, so a new query can
// match cached content even when no prior query exactly overlaps.
//
// Storage architecture — fully persisted via kvlite, no in-memory index:
//
//   Bucket hook_doc_store:    docID -> indexedDoc{HookName, Title, URL, Snippet}
//   Bucket hook_token_index:  "hookName|token" -> docIDList{IDs: [...]}
//
// Lookups read from kvlite directly, which uses boltdb page caching
// under the hood. Hot tokens stay warm in OS page cache; cold data
// only touches disk on first access. Total RAM usage is bounded by
// boltdb's working set, not by the total size of the index.
//
// On tier-2 search: tokenize the query, for each token fetch its
// docIDList entry for the current hook, sum per-doc overlap, rank
// and return the top candidates. Only the token buckets relevant to
// the current query's tokens (and the current hook) get touched —
// the rest of the index never loads.

const (
	hookDocStoreBucket   = "hook_doc_store"
	hookTokenIndexBucket = "hook_token_index"
)

// indexedDoc is one document in the inverted index.
type indexedDoc struct {
	HookName string `json:"hook_name"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Snippet  string `json:"snippet"`
}

// docIDList is the stored value for a token index entry. Wrapped in a
// struct (rather than a bare slice) so the JSON encoding stays stable
// if we add fields later (term frequency, last-seen timestamp, etc.).
type docIDList struct {
	IDs []string `json:"ids"`
}

// docTokenizeStopwords are removed during tokenization. A superset of
// the cache-key stopwords plus very common English filler that would
// produce mostly-noisy matches if kept.
var docTokenizeStopwords = map[string]bool{
	"and": true, "or": true, "not": true, "but": true,
	"the": true, "a": true, "an": true,
	"of": true, "in": true, "on": true, "at": true,
	"to": true, "for": true, "by": true, "with": true, "from": true,
	"is": true, "are": true, "was": true, "were": true, "be": true, "been": true,
	"as": true, "it": true, "its": true, "this": true, "that": true, "these": true, "those": true,
	"how": true, "what": true, "which": true, "who": true, "when": true, "where": true, "why": true,
	"can": true, "will": true, "may": true, "has": true, "have": true, "had": true,
	"do": true, "does": true, "did": true, "would": true, "could": true, "should": true,
}

// docTokenize splits text into lowercased unique tokens suitable for
// indexing. Non-alphanumeric characters become word boundaries.
// Stopwords are dropped via docTokenizeStopwords, which already
// covers all the common short filler. Short acronyms like "AI",
// "EU", "US", "ML" are intentionally preserved — they're among the
// most distinguishing tokens in technical research queries and
// dropping them by length would cripple match quality.
func docTokenize(text string) map[string]bool {
	tokens := make(map[string]bool)
	var sb strings.Builder
	sb.Grow(len(text))
	for _, r := range text {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			sb.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			sb.WriteRune(r + 32)
		default:
			sb.WriteByte(' ')
		}
	}
	for _, tok := range strings.Fields(sb.String()) {
		if tok == "" || docTokenizeStopwords[tok] {
			continue
		}
		tokens[tok] = true
	}
	return tokens
}

// parseCachedSources extracts (Title, URL, Snippet) triples from a cached
// source hook result. Implements the same parser as
// tools/websearch.ParseSearchResults inline to keep core free of the
// tool-package import.
//
// Expected block format per source:
//
//	1. Title
//	   URL
//	   Snippet (one or more lines)
//
// Blocks are separated by blank lines.
func parseCachedSources(result string) []indexedDoc {
	var docs []indexedDoc
	blocks := strings.Split(result, "\n\n")
	for _, block := range blocks {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) < 2 {
			continue
		}
		title := strings.TrimSpace(lines[0])
		if idx := strings.Index(title, ". "); idx >= 0 && idx < 4 {
			title = title[idx+2:]
		}
		u := strings.TrimSpace(lines[1])
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			continue
		}
		var snippet string
		if len(lines) > 2 {
			snippet = strings.TrimSpace(strings.Join(lines[2:], " "))
		}
		docs = append(docs, indexedDoc{Title: title, URL: u, Snippet: snippet})
	}
	return docs
}

// docID is a stable identifier for an indexed document. hookName + URL
// ensures that re-indexing the same source URL for the same hook does
// not create duplicates.
func docID(hookName, url string) string {
	return strings.ToLower(hookName) + "|" + url
}

// tokenKey formats the composite key for the token index bucket. Keys
// are scoped to a hook so that two hooks with overlapping vocabulary
// don't collide (e.g., "ai" appearing in both CourtListener and PubMed
// indexes remains cleanly separated).
func tokenKey(hookName, token string) string {
	return strings.ToLower(hookName) + "|" + token
}

// indexHookDocs parses a cached source hook result into documents and
// persists them into the tier-2 inverted index. Called from
// storeHookCache after each successful non-empty write. Safe to call
// multiple times for the same (hookName, result) pair — the docID is
// stable, and token-list appends dedupe.
func indexHookDocs(hookName, result string) {
	if result == "" {
		return
	}
	hookCacheMu.RLock()
	db := hookCacheDB
	hookCacheMu.RUnlock()
	if db == nil {
		return
	}
	docs := parseCachedSources(result)
	if len(docs) == 0 {
		return
	}
	for _, d := range docs {
		d.HookName = hookName
		id := docID(hookName, d.URL)

		// Persist document content.
		db.Set(hookDocStoreBucket, id, d)

		// Update the token → docIDs inverted index. For each token,
		// append the new docID to its list unless already present.
		tokens := docTokenize(d.Title + " " + d.Snippet)
		for tok := range tokens {
			key := tokenKey(hookName, tok)
			var list docIDList
			db.Get(hookTokenIndexBucket, key, &list)
			// Dedupe append — skip if already listed.
			already := false
			for _, existing := range list.IDs {
				if existing == id {
					already = true
					break
				}
			}
			if already {
				continue
			}
			list.IDs = append(list.IDs, id)
			db.Set(hookTokenIndexBucket, key, list)
		}
	}
}

// searchHookDocs returns the top-matching cached documents for a query
// against the given hook's on-disk document index. Matching is by
// token overlap — documents sharing more query tokens rank higher.
//
// Sharding behavior: only the token buckets touched by the query's
// tokens get loaded from disk. The rest of the index stays untouched.
// boltdb's OS-level page caching keeps the hot paths warm without
// holding the full index in application memory.
//
// Threshold policy (moderate): require at least 2 matching tokens per
// document, and return results only if at least 3 documents meet that
// bar. Prevents false positives from single-token coincidences while
// still returning useful hits when a query lands on well-indexed
// content.
func searchHookDocs(hookName string, query string, limit int) []indexedDoc {
	queryTokens := docTokenize(query)
	if len(queryTokens) == 0 {
		return nil
	}
	hookCacheMu.RLock()
	db := hookCacheDB
	hookCacheMu.RUnlock()
	if db == nil {
		return nil
	}

	// Fetch only the token buckets relevant to the current query.
	// This is the sharding — we never touch other hooks' tokens or
	// tokens unrelated to this query.
	scores := make(map[string]int)
	for tok := range queryTokens {
		var list docIDList
		if !db.Get(hookTokenIndexBucket, tokenKey(hookName, tok), &list) {
			continue
		}
		for _, id := range list.IDs {
			scores[id]++
		}
	}
	if len(scores) == 0 {
		return nil
	}

	const minTokensPerDoc = 2
	type scored struct {
		id    string
		score int
	}
	var candidates []scored
	for id, s := range scores {
		if s >= minTokensPerDoc {
			candidates = append(candidates, scored{id, s})
		}
	}
	const minDocsForMatch = 3
	if len(candidates) < minDocsForMatch {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].id < candidates[j].id
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	// Hydrate the matched docIDs from the document store. Each Get is
	// a single kvlite read, again touching only the relevant pages.
	results := make([]indexedDoc, 0, len(candidates))
	for _, c := range candidates {
		var d indexedDoc
		if db.Get(hookDocStoreBucket, c.id, &d) {
			results = append(results, d)
		}
	}
	return results
}

// formatIndexedDocs renders matched documents back to the numbered
// search-result format used by source hook APIs, so downstream
// consumers can treat tier-2 hits identically to live results.
func formatIndexedDocs(docs []indexedDoc) string {
	var sb strings.Builder
	for i, d := range docs {
		fmt.Fprintf(&sb, "%d. %s\n   %s\n", i+1, d.Title, d.URL)
		if d.Snippet != "" {
			fmt.Fprintf(&sb, "   %s\n", d.Snippet)
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

