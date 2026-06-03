package core

import (
	"context"
	"sort"
	"sync"
)

// Reference sources — a generic, extensible registry that lets one app's
// accumulated knowledge be pulled into another app as drafting/reference
// context. The motivating case: techwriter drafting instructions for a
// system, backed by the facts servitor gathered about it. But the registry
// is deliberately source-agnostic so ANY service can expose its knowledge
// (collections, servitor systems, future services) and ANY consumer (the
// writer apps today, others later) can pull from all of them through one
// picker + one fetch path — "services reaching into services."
//
// A source registers once (at route-registration time, where its DB is
// live — same shape as RegisterAPIKeyValidator / RegisterChatTool). It does
// NOT require the consumer to import the producer: the consumer only ever
// talks to this registry.

// ReferenceItem is one selectable thing within a source — a collection, a
// servitor appliance, etc. ID is opaque to the consumer and meaningful only
// to the source that produced it.
type ReferenceItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Desc string `json:"desc,omitempty"`
}

// ReferenceGroup is one source's items, for grouped display in a picker.
type ReferenceGroup struct {
	Kind  string          `json:"kind"`  // stable source id, e.g. "collection", "system"
	Label string          `json:"label"` // human group label, e.g. "Collections", "Systems"
	Items []ReferenceItem `json:"items"`
}

// ReferenceSource is implemented by a producer app to expose its knowledge.
//
// A source binds its OWN database at registration (each app has its own DB —
// the consumer's per-user DB cannot read the producer's tables). List/Fetch
// therefore take only the user string; the source resolves that user's data
// from its own store (e.g. UserDB(myDB, user)). The consumer never sees, and
// never needs, the producer's database.
type ReferenceSource interface {
	// Kind is a stable identifier for this source ("collection", "system").
	// Used to route a Fetch back to the right source.
	Kind() string
	// Label is the human group name shown in a consumer's picker.
	Label() string
	// List returns the items available to user, for the picker. Cheap —
	// called on every picker render.
	List(user string) []ReferenceItem
	// Fetch returns reference TEXT for one item, suitable to inject into an
	// LLM prompt. query is the consumer's drafting context (article topic /
	// chat message) — sources that semantic-search (collections) use it;
	// sources that inject whole docs (servitor) may ignore it. Empty string
	// = nothing relevant / item not found.
	Fetch(ctx context.Context, user, itemID, query string) string
}

var (
	refSourcesMu sync.RWMutex
	refSources   = map[string]ReferenceSource{}
)

// RegisterReferenceSource registers a producer. Re-registering the same Kind
// replaces it. Call once at route-registration time.
func RegisterReferenceSource(s ReferenceSource) {
	if s == nil || s.Kind() == "" {
		return
	}
	refSourcesMu.Lock()
	refSources[s.Kind()] = s
	refSourcesMu.Unlock()
}

// ReferenceGroups returns every source's items for user, sorted by label —
// the data a consumer's picker renders. Empty groups are omitted.
func ReferenceGroups(user string) []ReferenceGroup {
	refSourcesMu.RLock()
	srcs := make([]ReferenceSource, 0, len(refSources))
	for _, s := range refSources {
		srcs = append(srcs, s)
	}
	refSourcesMu.RUnlock()

	var groups []ReferenceGroup
	for _, s := range srcs {
		items := s.List(user)
		if len(items) == 0 {
			continue
		}
		groups = append(groups, ReferenceGroup{Kind: s.Kind(), Label: s.Label(), Items: items})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Label < groups[j].Label })
	return groups
}

// FetchReference resolves (kind, itemID) to the owning source and returns its
// reference text for query. Empty string when the kind is unknown or the
// source has nothing to contribute.
func FetchReference(ctx context.Context, user, kind, itemID, query string) string {
	refSourcesMu.RLock()
	s := refSources[kind]
	refSourcesMu.RUnlock()
	if s == nil {
		return ""
	}
	return s.Fetch(ctx, user, itemID, query)
}

// FetchReferences fetches + concatenates several selected references into one
// labeled block, ready to drop into a system prompt. Each selection is a
// {Kind, ItemID}. Blank fetches are skipped.
type ReferenceSelection struct {
	Kind   string `json:"kind"`
	ItemID string `json:"item_id"`
}

func FetchReferences(ctx context.Context, user, query string, sel []ReferenceSelection) string {
	var b []string
	for _, s := range sel {
		if txt := FetchReference(ctx, user, s.Kind, s.ItemID, query); txt != "" {
			b = append(b, txt)
		}
	}
	if len(b) == 0 {
		return ""
	}
	out := "## Reference context\n\nBackground gathered by other gohort services, provided to ground this draft. Use it where relevant; do not invent details it doesn't contain.\n\n"
	for i, blk := range b {
		if i > 0 {
			out += "\n\n---\n\n"
		}
		out += blk
	}
	return out
}
