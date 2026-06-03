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

// Fetch assembles a whole-knowledge reference block for one appliance: its
// operator notes, every accumulated knowledge doc, and the discrete facts.
// query is ignored — servitor injects the full picture, not a search slice.
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
