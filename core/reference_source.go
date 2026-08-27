package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
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

// SessionReferenceToolProvider is ReferenceToolProvider with the calling
// turn's session in hand. Implement this INSTEAD of ItemTools when a
// source's tools do work that OUTLIVES a single function call — spawn a
// sub-run, drive an SSH session, poll a remote job.
//
// The reason is cancellation, and it is not cosmetic. A tool handler's
// signature carries no context, so a handler that starts its own run has
// nothing to root it on and reaches for context.Background(); that run is
// then detached from the turn that asked for it. Stopping the agent ends
// the agent and leaves the investigation it dispatched running against
// the live system, reporting to nobody. Observed exactly that way: cancel
// the calling agent, and the servitor investigation it had dispatched
// carried on.
//
// sess.Context() is the turn's cancelable context, and it is nil-safe —
// a caller with no session (an app that never wired one, a background
// gather) falls back to context.Background() and behaves as before.
//
// A source may implement both; the session-aware form wins.
type SessionReferenceToolProvider interface {
	ItemToolsWithSession(sess *ToolSession, user, itemID string) []AgentToolDef
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
	start := time.Now()
	var slow []string
	for _, s := range srcs {
		at := time.Now()
		items := s.List(user)
		// Per-source, because "listing references is slow" is not actionable
		// and "the servitor source took 1.9s" is. Every source here enumerates
		// something it owns — appliances, agents, stores, a remote MCP server —
		// and any one of them can be the whole cost while the others are free.
		if d := time.Since(at); d > 100*time.Millisecond {
			slow = append(slow, fmt.Sprintf("%s %s", s.Kind(), d.Round(time.Millisecond)))
		}
		if len(items) == 0 {
			continue
		}
		groups = append(groups, ReferenceGroup{Kind: s.Kind(), Label: s.Label(), Items: items})
	}
	if len(slow) > 0 {
		Log("[reference] listing sources took %s — slow: %s", time.Since(start).Round(time.Millisecond), strings.Join(slow, ", "))
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Label < groups[j].Label })
	return groups
}

// ReferenceSourceCounter is the cheap half of a reference source: whether it
// has anything, without building what it has.
//
// It exists because the two questions have wildly different costs and the
// interface only offered the expensive one. The filestore source builds each
// item's description by WALKING THE FILESYSTEM — stat every file under every
// folder of every store, to render "· 12 folders" in a picker. Asking it
// merely whether it has any stores therefore cost a full tree walk, measured
// live at 2.3 and then 5.1 seconds on a page render that displayed none of it.
//
// Optional: a source that does not implement this is asked the long way, which
// is correct for the ones whose List is already cheap.
type ReferenceSourceCounter interface {
	// HasItems reports whether this source has anything for user, and must be
	// substantially cheaper than List — no I/O beyond what naming the items
	// requires.
	HasItems(user string) bool
}

// AnyReferenceSource reports whether user has ANY reference source with
// anything in it, stopping at the first one that does.
//
// It exists because asking that question through ReferenceGroups is
// enormously more expensive than the question deserves: that call makes every
// registered source enumerate everything it owns — appliances, all the user's
// agents, file stores, a remote MCP server over the network — assembles the
// full catalog, sorts it, and hands it back so the caller can compare its
// length to zero. The orchestrate page did exactly that on every render, to
// decide whether to hide one toolbar button, and it was the single largest
// cost in the page.
//
// Short-circuiting is a real fix rather than a micro-optimization: with any
// source populated, the work drops from "every source" to "one". It does not
// help when the FIRST source consulted is the slow one, which is why the
// timing above stays — the two together turn an unexplained page stall into a
// named source with a bounded cost.
func AnyReferenceSource(user string) bool {
	refSourcesMu.RLock()
	srcs := make([]ReferenceSource, 0, len(refSources))
	for _, s := range refSources {
		srcs = append(srcs, s)
	}
	refSourcesMu.RUnlock()

	// Timed per source here TOO. The page moved to this function precisely
	// because it is the cheap form of the question, and shipping the cheap
	// form without the measurement meant the next slow render had nothing to
	// say — which is exactly what happened on the first one.
	start := time.Now()
	for _, s := range srcs {
		at := time.Now()
		var found bool
		if c, ok := s.(ReferenceSourceCounter); ok {
			found = c.HasItems(user)
		} else {
			found = len(s.List(user)) > 0
		}
		if d := time.Since(at); d > 100*time.Millisecond {
			Log("[reference] %s.List took %s (checking whether ANY source has items)", s.Kind(), d.Round(time.Millisecond))
		}
		if found {
			if total := time.Since(start); total > 250*time.Millisecond {
				Log("[reference] any-source check took %s, satisfied by %s", total.Round(time.Millisecond), s.Kind())
			}
			return true
		}
	}
	if total := time.Since(start); total > 250*time.Millisecond {
		Log("[reference] any-source check took %s and found nothing", total.Round(time.Millisecond))
	}
	return false
}

// ReferenceSourceKnown reports whether kind names a registered reference
// source — i.e. whether it is something an agent can be ATTACHED to. Used
// by the path-scope gate to tell "you have not linked this" apart from
// "there is nothing here to link".
func ReferenceSourceKnown(kind string) bool {
	refSourcesMu.RLock()
	defer refSourcesMu.RUnlock()
	_, ok := refSources[kind]
	return ok
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
// Callers with a session in hand should use ReferenceItemToolsWithSession
// instead, so the tools they mint can be canceled with the turn.
func ReferenceItemTools(user, kind, itemID string) []AgentToolDef {
	return ReferenceItemToolsWithSession(nil, user, kind, itemID)
}

// ReferenceItemToolsWithContext is for the callers in between: an app that
// mints its tools OUTSIDE a turn (the app-tools contract is a plain
// []AgentToolDef, built before the run exists) but does hold the request or run
// context the work should die with.
//
// Without it those callers reach for ReferenceItemTools, and a tool that
// dispatches its own sub-run roots that run on context.Background(). Stopping
// the turn then stops the turn and nothing else: servitor's investigate_<system>
// keeps a lead investigator and an SSH worker running against a live machine
// with nobody left to report to. Wrapping the context in a minimal session is
// the whole fix, and it lives here rather than in each app so the shape of that
// stand-in session is decided once.
func ReferenceItemToolsWithContext(ctx context.Context, user, kind, itemID string) []AgentToolDef {
	if ctx == nil {
		return ReferenceItemToolsWithSession(nil, user, kind, itemID)
	}
	return ReferenceItemToolsWithSession(&ToolSession{Ctx: ctx, Username: user}, user, kind, itemID)
}

// ReferenceItemToolsWithSession is ReferenceItemTools with the calling
// turn's session, so a source whose tools spawn their own work can root it
// on a context that a Stop actually reaches. See
// SessionReferenceToolProvider. A nil session is legal and gives exactly
// the old behavior.
func ReferenceItemToolsWithSession(sess *ToolSession, user, kind, itemID string) []AgentToolDef {
	refSourcesMu.RLock()
	s := refSources[kind]
	refSourcesMu.RUnlock()
	if s == nil {
		return nil
	}
	// Session-aware first: a source implementing both means "give me the
	// session when there is one", and the plain form is its own fallback.
	if tp, ok := s.(SessionReferenceToolProvider); ok {
		return tp.ItemToolsWithSession(sess, user, itemID)
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
			// The turn's context, not Background: this Fetch may be a
			// network round-trip to a remote source, and a stopped turn
			// should stop waiting on it. Nil-safe.
			txt := s.Fetch(sess.Context(), user, item, q)
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

// Ref renders a selection as the "kind:item" handle the tools and the
// picker both speak.
func (r ReferenceSelection) Ref() string { return r.Kind + ":" + r.ItemID }

// ParseReferenceRef splits a composite handle back into a selection.
//
// It takes BOTH separators because both are already in the wild: the
// agent tool documents "<kind>:<item_id>" and guides' picker has always
// written "<kind>::<item_id>". A kind never contains a colon, so the
// single-colon form is unambiguous at the FIRST colon even when an item
// id contains one; the double form is checked first so it is not read as
// a kind plus an item id beginning with ":".
func ParseReferenceRef(ref string) (ReferenceSelection, bool) {
	ref = strings.TrimSpace(ref)
	kind, item, found := strings.Cut(ref, "::")
	if !found {
		kind, item, found = strings.Cut(ref, ":")
	}
	kind, item = strings.TrimSpace(kind), strings.TrimSpace(item)
	if !found || kind == "" || item == "" {
		return ReferenceSelection{}, false
	}
	return ReferenceSelection{Kind: kind, ItemID: item}, true
}

// ReferenceSelections decodes from EITHER shape: the object form this
// stores, or the "kind:item" strings every other surface uses.
//
// The two grew apart. The agent CRUD tool has always taken strings
// (referenceSelectionsFromArgs), the stored record has always been
// objects, and nothing ever posted the field over HTTP so the mismatch
// went unnoticed — until a picker needed to, and a chip picker submits a
// flat list of handles. Accepting both is the tolerant direction: a
// caller that sends what the tool documents should not be told the
// documentation is for a different door.
//
// Same posture as IntakeFormSpec, which accepts an array or a
// JSON-string array for the same reason.
type ReferenceSelections []ReferenceSelection

func (rs *ReferenceSelections) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*rs = nil
		return nil
	}
	// Object form first: it is what this stores, so it is the common case.
	var objs []ReferenceSelection
	if err := json.Unmarshal(data, &objs); err == nil {
		*rs = clean_selections(objs)
		return nil
	}
	var refs []string
	if err := json.Unmarshal(data, &refs); err != nil {
		return Error("attached_sources must be a list of \"kind:item_id\" strings or {kind,item_id} objects")
	}
	out := make([]ReferenceSelection, 0, len(refs))
	for _, raw := range refs {
		sel, ok := ParseReferenceRef(raw)
		if !ok {
			// Skipped rather than fatal, matching the tool path: one
			// malformed entry should not reject an otherwise good save.
			Log("[reference] ignoring attached source %q — expected \"<kind>:<item_id>\"", raw)
			continue
		}
		out = append(out, sel)
	}
	*rs = clean_selections(out)
	return nil
}

// clean_selections drops blanks and duplicates, preserving order.
func clean_selections(in []ReferenceSelection) []ReferenceSelection {
	out := in[:0:0]
	seen := map[string]bool{}
	for _, s := range in {
		s.Kind, s.ItemID = strings.TrimSpace(s.Kind), strings.TrimSpace(s.ItemID)
		if s.Kind == "" || s.ItemID == "" || seen[s.Ref()] {
			continue
		}
		seen[s.Ref()] = true
		out = append(out, s)
	}
	return out
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
