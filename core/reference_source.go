package core

import (
	"context"
	"fmt"
	"sort"
	"strings"
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

// ReferenceToolProvider is an OPTIONAL interface a ReferenceSource may ALSO
// implement to contribute source-specific TOOLS for a selected item — instead of
// leaving a consumer only the flat Fetch reached through a generic
// pull_reference. When a consumer attaches item X of this source, it asks the
// source for X's tools and injects them straight into the agent's catalog, so an
// attached source shows up as concrete, named capabilities the LLM can see and
// call directly (e.g. search_<system>_knowledge, investigate_<system>) rather
// than something it must first discover through list_reference_sources.
//
// This is what makes attached Sources actually get used: a generic
// pull_reference competes with the framework's own knowledge tools and loses;
// distinctly-named per-item tools don't.
type ReferenceToolProvider interface {
	// ItemTools returns the tools that operate on ONE selected item, for user.
	// Tool names MUST be item-unique (fold the item's name/id into them) so
	// several attached sources can't collide in one catalog. Return nil when the
	// item is unknown or offers no tools — the consumer then falls back to the
	// flat pull_reference path. Handlers should tag Caps honestly (a live-access
	// tool is CapNetwork/CapExecute) so a consumer can gate them (e.g. a Private
	// guide drops the network/execute ones).
	ItemTools(user, itemID string) []AgentToolDef
}

// NetworkReferenceSource is an optional interface a ReferenceSource implements to
// declare that resolving its content (List/Fetch) reaches out over the network —
// e.g. a remote MCP document source. A consumer gathering in a private/offline
// mode skips network-reaching sources. Sources that don't implement it are
// treated as local (no network), so a local source's cached knowledge still
// grounds a private draft.
type NetworkReferenceSource interface {
	ReachesNetwork() bool
}

// ReferenceReachesNetwork reports whether the source for kind resolves its
// content over the network. False for unknown kinds and for local sources.
func ReferenceReachesNetwork(kind string) bool {
	refSourcesMu.RLock()
	s := refSources[kind]
	refSourcesMu.RUnlock()
	nr, ok := s.(NetworkReferenceSource)
	return ok && nr.ReachesNetwork()
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

// ReferenceItemTools returns the tools for one attached item — the (kind, itemID)
// a consumer stored from a picker selection — so EVERY attached source becomes a
// first-class, named tool in the agent's catalog, not just the ones with a rich
// bespoke surface. Resolution:
//   - a source that implements ReferenceToolProvider contributes its own tools
//     (e.g. servitor: search/facts/live-investigate per system);
//   - ANY OTHER source (a connected MCP doc source like Confluence, any future
//     source) gets a DEFAULT named search tool synthesized from its Fetch, so it
//     shows up and gets used exactly like an SSH system does — the capability is
//     "any source", not "SSH only".
//
// Empty when the kind is unknown or the item isn't available to user.
func ReferenceItemTools(user, kind, itemID string) []AgentToolDef {
	refSourcesMu.RLock()
	s := refSources[kind]
	refSourcesMu.RUnlock()
	if s == nil {
		return nil
	}
	if tp, ok := s.(ReferenceToolProvider); ok {
		return tp.ItemTools(user, itemID)
	}
	// Default: a generic named search tool wrapping the source's Fetch. Resolve
	// the item's display name (for a readable, unique tool name) from List; a
	// missing name means the item isn't available to this user.
	name := referenceItemName(s, user, itemID)
	if name == "" {
		return nil
	}
	slug := RefToolSlug(name)
	if slug == "" {
		slug = RefToolSlug(kind)
	}
	if slug == "" {
		return nil
	}
	label := s.Label()
	item := itemID // capture for the closure
	return []AgentToolDef{{
		Tool: Tool{
			Name:        "search_" + slug,
			Description: fmt.Sprintf("Search the connected source %q (%s) for material relevant to a query, and return what it finds. Use it to ground a section in this source's own content. Read-only; it may reach out over the network to the source.", name, label),
			Parameters: map[string]ToolParam{
				"query": {Type: "string", Description: "What you're writing about — a focused topic or question."},
			},
			Required: []string{"query"},
			// The source's Fetch may call out over the network (a remote MCP doc
			// source), so tag it accordingly — a Private consumer then strips it.
			Caps: []Capability{CapNetwork, CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			q := strings.TrimSpace(fmt.Sprint(args["query"]))
			txt := s.Fetch(context.Background(), user, item, q)
			if strings.TrimSpace(txt) == "" {
				return fmt.Sprintf("No results from %s for %q.", name, q), nil
			}
			return txt, nil
		},
	}}
}

// referenceItemName resolves an item's display name from its source's List, or
// "" when the item isn't in the user's available set.
func referenceItemName(s ReferenceSource, user, itemID string) string {
	for _, it := range s.List(user) {
		if it.ID == itemID {
			return it.Name
		}
	}
	return ""
}

// RefToolSlug turns a display name into a safe, lowercase tool-name fragment
// ([a-z0-9_], runs of other characters collapsed to one underscore, trimmed).
// Empty when the name has no usable characters. Shared so every source names its
// per-item tools the same way.
func RefToolSlug(name string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if b.Len() > 0 && !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
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
