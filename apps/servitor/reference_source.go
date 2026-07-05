package servitor

import (
	"context"
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// servitorSource exposes each appliance servitor knows about as a generic
// reference source (core.ReferenceSource), so writer apps — techwriter today,
// others later — can ground a draft in the facts and knowledge docs servitor
// gathered about a system, without importing servitor. Registered once from
// RegisterRoutes, bound to servitor's own DB (the consumer's per-user DB can't
// read servitor's tables, so the source resolves the user's data itself).
type servitorSource struct{ db Database }

func (s servitorSource) Kind() string  { return "system" }
func (s servitorSource) Label() string { return "Systems" }

// List returns the user's appliances as pickable items.
func (s servitorSource) List(user string) []ReferenceItem {
	if s.db == nil || user == "" {
		return nil
	}
	udb := UserDB(s.db, user)
	if udb == nil {
		return nil
	}
	var items []ReferenceItem
	for _, key := range udb.Keys(applianceTable) {
		var a Appliance
		if udb.Get(applianceTable, key, &a) && a.ID != "" {
			desc := a.Type
			if a.Host != "" {
				desc = strings.TrimSpace(a.Type + " · " + a.Host)
			}
			name := a.Name
			if name == "" {
				name = a.ID
			}
			items = append(items, ReferenceItem{ID: a.ID, Name: name, Desc: desc})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items
}

// Fetch returns a reference block for one appliance. When query is non-empty it
// returns only the SLICE of servitor's knowledge relevant to that subject —
// semantic search over the appliance's linked collections, plus any structured
// docs and facts that mention the query terms — so a writer app grounding a
// specific section gets focused material instead of the whole appliance. With no
// query (e.g. a whole-guide audit) or when nothing relevant is found, it falls
// back to the full picture.
func (s servitorSource) Fetch(ctx context.Context, user, itemID, query string) string {
	if s.db == nil || user == "" || itemID == "" {
		return ""
	}
	udb := UserDB(s.db, user)
	if udb == nil {
		return ""
	}
	var a Appliance
	if !udb.Get(applianceTable, itemID, &a) || a.ID == "" {
		return ""
	}

	if q := strings.TrimSpace(query); q != "" {
		if focused := s.fetchFocused(ctx, udb, user, a, q); focused != "" {
			return focused
		}
		// Nothing matched the query — fall through to the full picture so the
		// consumer still gets grounding rather than an empty result.
	}
	return s.fetchFull(udb, a)
}

// fetchFocused returns only the knowledge relevant to query: semantic hits from
// the appliance's linked collections, plus structured docs and facts that
// mention the query terms. Returns "" when nothing matches so Fetch can fall
// back to the full picture.
func (s servitorSource) fetchFocused(ctx context.Context, udb Database, user string, a Appliance, query string) string {
	var hits []SearchHit
	if len(a.Collections) > 0 {
		hits = SearchCollections(ctx, CollectionsDB(), user, a.Collections, query, 6)
	}

	docs := allDocs(udb, a.ID)
	var matchedDocs []string
	for _, doc := range knowledgeDocNames {
		if c := strings.TrimSpace(docs[doc]); c != "" && refQueryMatch(c, query) {
			matchedDocs = append(matchedDocs, doc)
		}
	}

	var matchedFacts []SshFact
	for _, f := range factsForAppliance(udb, a.ID) {
		if f.Key != "" && refQueryMatch(f.Key+" "+f.Value, query) {
			matchedFacts = append(matchedFacts, f)
		}
	}

	if len(hits) == 0 && len(matchedDocs) == 0 && len(matchedFacts) == 0 {
		return ""
	}

	name := a.Name
	if name == "" {
		name = a.ID
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### System: %s (relevant to: %s)\n", name, query)
	if a.Host != "" {
		fmt.Fprintf(&b, "Host: %s\n", a.Host)
	}
	if len(hits) > 0 {
		b.WriteString("\n#### Relevant knowledge\n")
		for i, h := range hits {
			label := strings.TrimSpace(h.Title)
			if label == "" {
				label = h.Source
			}
			fmt.Fprintf(&b, "%d. [%s] %s\n\n", i+1, label, strings.TrimSpace(h.Text))
		}
	}
	for _, doc := range matchedDocs {
		fmt.Fprintf(&b, "\n#### %s\n%s\n", doc, strings.TrimSpace(docs[doc]))
	}
	if len(matchedFacts) > 0 {
		b.WriteString("\n#### Known facts\n")
		for _, f := range matchedFacts {
			fmt.Fprintf(&b, "- %s: %s\n", f.Key, f.Value)
		}
	}
	return strings.TrimSpace(b.String())
}

// fetchFull assembles the whole-knowledge reference block for one appliance: its
// operator notes, every accumulated knowledge doc, and the discrete facts. Used
// when there's no query, or as a fallback when a query matched nothing.
func (s servitorSource) fetchFull(udb Database, a Appliance) string {
	name := a.Name
	if name == "" {
		name = a.ID
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### System: %s\n", name)
	if a.Host != "" {
		fmt.Fprintf(&b, "Host: %s\n", a.Host)
	}
	if notes := strings.TrimSpace(a.Instructions); notes != "" {
		fmt.Fprintf(&b, "\nOperator notes:\n%s\n", notes)
	}

	// Knowledge docs servitor accumulated (overview/databases/filesystem/...).
	docs := allDocs(udb, a.ID)
	for _, doc := range knowledgeDocNames {
		if c := strings.TrimSpace(docs[doc]); c != "" {
			fmt.Fprintf(&b, "\n#### %s\n%s\n", doc, c)
		}
	}

	// Discrete facts (key/value, e.g. versions, ports, paths).
	if facts := factsForAppliance(udb, a.ID); len(facts) > 0 {
		b.WriteString("\n#### Known facts\n")
		for _, f := range facts {
			if f.Key == "" {
				continue
			}
			fmt.Fprintf(&b, "- %s: %s\n", f.Key, f.Value)
		}
	}

	out := strings.TrimSpace(b.String())
	// Header-only (no docs, no facts) → nothing worth injecting.
	if !strings.Contains(out, "####") {
		return ""
	}
	return out
}

// refQueryMatch reports whether text mentions any meaningful term (>=3 chars)
// from query, case-insensitively. A lightweight keyword filter for the
// structured docs and facts, which aren't embedded/searchable like collections.
func refQueryMatch(text, query string) bool {
	lt := strings.ToLower(text)
	for _, w := range strings.Fields(strings.ToLower(query)) {
		w = strings.Trim(w, ".,;:!?\"'()[]{}")
		if len(w) >= 3 && strings.Contains(lt, w) {
			return true
		}
	}
	return false
}
