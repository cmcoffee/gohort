// Admin-curated tool groups. A group bundles multiple chat tools
// under one logical heading so the agent-loop catalog can collapse
// related tools into a single expandable entry — context savings
// when a deployment has many tools that share a purpose (a Calendar
// API surface, a comms-suite, etc.).
//
// Two layers, separated:
//
//  1. Storage + CRUD (this file). Admin-managed; lives under the
//     deployment-wide auth DB so every app sees the same groups.
//
//  2. Runtime catalog rewriter (separate file once wired). Replaces
//     group-member tools with one synthetic group entry plus an
//     expand_tool_group meta-tool the LLM can call to bring the
//     full member set into scope for the rest of the turn.
//
// The auto-grouping-by-credential alternative was rejected — admin
// curation lets unrelated tools that fire together ("communications"
// = email + sms + slack) sit in one logical group even though they
// each have their own credential.

package core

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// toolGroupsTable holds ToolGroup records keyed by ID. Lives in
	// AuthDB() so the admin app, the runtime catalog rewriter, and
	// any future per-user policy code all read from one place.
	toolGroupsTable = "tool_groups"
)

// ToolGroup is the persisted shape. Members reference chat tools by
// name; the runtime rewriter looks them up against the chat-tool
// registry, so a deleted tool simply drops out of the group at
// resolve time without needing storage cleanup.
type ToolGroup struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`        // human-readable; appears in catalog ("Communications", "Acme API")
	Description string    `json:"description"` // LLM-facing summary shown until the group is expanded
	Members     []string  `json:"members"`     // chat tool names — both built-in and persistent temp tools eligible
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
}

// In-memory snapshot of all groups, refreshed on each mutation.
// Reads from the runtime catalog rewriter are hot-path (every tool
// resolution) so we serve them from the cache and only touch the DB
// on CRUD. mu guards both fields together.
var (
	toolGroupsMu    sync.RWMutex
	toolGroupsCache map[string]ToolGroup
)

// LoadToolGroups returns all groups, sorted by Name. Reads from the
// in-memory cache; lazily hydrates on first call. db nil → returns
// empty slice (allows the admin UI to render before AuthDB is wired).
func LoadToolGroups(db Database) []ToolGroup {
	if db == nil {
		return nil
	}
	hydrateToolGroupsCache(db)
	toolGroupsMu.RLock()
	out := make([]ToolGroup, 0, len(toolGroupsCache))
	for _, g := range toolGroupsCache {
		out = append(out, g)
	}
	toolGroupsMu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// LoadToolGroup fetches one group by ID. Returns false when not found.
func LoadToolGroup(db Database, id string) (ToolGroup, bool) {
	if db == nil || id == "" {
		return ToolGroup{}, false
	}
	hydrateToolGroupsCache(db)
	toolGroupsMu.RLock()
	defer toolGroupsMu.RUnlock()
	g, ok := toolGroupsCache[id]
	return g, ok
}

// SaveToolGroup upserts a group, stamping timestamps + assigning an
// ID on new records. Returns the saved record so the admin UI can
// echo the canonical form back.
func SaveToolGroup(db Database, g ToolGroup) (ToolGroup, error) {
	if db == nil {
		return g, fmt.Errorf("tool groups: db not initialized")
	}
	g.Name = strings.TrimSpace(g.Name)
	if g.Name == "" {
		return g, fmt.Errorf("tool groups: name is required")
	}
	g.Members = dedupeAndCleanMembers(g.Members)
	now := time.Now()
	if g.ID == "" {
		g.ID = UUIDv4()
		g.Created = now
	}
	if g.Created.IsZero() {
		g.Created = now
	}
	g.Updated = now
	db.Set(toolGroupsTable, g.ID, g)
	invalidateToolGroupsCache()
	return g, nil
}

// DeleteToolGroup removes a group by ID. For admin-curated groups
// the row is dropped outright. For builtin IDs the row is the
// user's shadow; deleting it reverts the group to the in-code
// framework default (which surfaces back via the cache hydrator).
// Returns an error on a builtin ID with no shadow saved — there's
// nothing to revert.
func DeleteToolGroup(db Database, id string) error {
	if db == nil || id == "" {
		return fmt.Errorf("tool groups: id is required")
	}
	if IsBuiltinToolGroupID(id) {
		if !db.Get(toolGroupsTable, id, &ToolGroup{}) {
			return fmt.Errorf("tool group %q is at framework defaults (nothing to revert)", id)
		}
		db.Unset(toolGroupsTable, id)
		invalidateToolGroupsCache()
		return nil
	}
	db.Unset(toolGroupsTable, id)
	invalidateToolGroupsCache()
	return nil
}

// ToolGroupForMember returns the group (if any) that contains the
// given tool name. Linear scan over all groups; cheap at the cache
// scale we expect (a deployment with hundreds of groups is unusual).
// First match wins — admins should keep memberships exclusive but
// the runtime handles overlap by picking the first group seen.
func ToolGroupForMember(db Database, toolName string) (ToolGroup, bool) {
	if db == nil || toolName == "" {
		return ToolGroup{}, false
	}
	hydrateToolGroupsCache(db)
	toolGroupsMu.RLock()
	defer toolGroupsMu.RUnlock()
	for _, g := range toolGroupsCache {
		for _, m := range g.Members {
			if m == toolName {
				return g, true
			}
		}
	}
	return ToolGroup{}, false
}

// MemberSet returns the union of all tool names referenced by any
// group, as a set for O(1) lookup. Used by the runtime rewriter to
// quickly decide whether a candidate tool needs collapsing into a
// group entry. Empty when no groups exist — caller can short-circuit
// the whole rewrite pass.
func MemberSet(db Database) map[string]bool {
	if db == nil {
		return nil
	}
	hydrateToolGroupsCache(db)
	toolGroupsMu.RLock()
	defer toolGroupsMu.RUnlock()
	if len(toolGroupsCache) == 0 {
		return nil
	}
	out := map[string]bool{}
	for _, g := range toolGroupsCache {
		for _, m := range g.Members {
			out[m] = true
		}
	}
	return out
}

// builtinToolGroups returns the framework-defined default groups.
// These behave identically to admin-curated groups at runtime — the
// LLM sees them as <slug>_toolbox catalog entries — and follow the
// shadow/Revert pattern: an admin edit lands as a DB row with the
// same stable ID; deleting the shadow restores the in-code defaults.
//
// Adding a new builtin: append here with a stable ID prefixed
// "builtin-". Describe the bundle's capability without enumerating
// members (the framework lists those alongside the description).
func builtinToolGroups() []ToolGroup {
	return []ToolGroup{
		// Retired builtins (kept in comments as documentation for why
		// they're gone — the rewriter looks tools up in the global
		// ChatTool registry, and the listed members aren't there):
		//
		//   builtin-agent-management — replaced by the `agents`
		//     grouped tool which already collapses list/get/run into
		//     one catalog entry. create/update/clone/delete are
		//     Builder-exclusive (via builderInternalTools), so the
		//     rewriter couldn't find them anyway.
		//
		//   builtin-recurring-tasks — schedule_recurring family is
		//     constructed inline per chatTurn (scheduleRecurringToolDef
		//     in runner.go), not registered globally. Rewriter can't
		//     match member names.
		//
		//   builtin-facts — store_fact / forget_fact / list_facts are
		//     chatTurn-bound + gated by agent.DisableExplicit. Same
		//     "not in registry" reason as above.
		//
		//   builtin-knowledge — knowledge_search (Knowledge layer,
		//     always available) and memory_save / memory_search /
		//     memory_forget (Reference Memory layer, gated by
		//     DisableInferred) are all chatTurn-bound. Same reason.
		//
		// Only Web Media survives because its members ARE registered
		// ChatTools (browse_page, screenshot_page, fetch_image, etc.).
		{
			ID:          "builtin-web-media",
			Name:        "Web Media",
			Description: "Tools for rich web content and media beyond plain text fetching: rendering JavaScript-heavy pages, capturing screenshots, finding and downloading images, downloading and viewing videos. Expand when the user's request genuinely involves page interaction or image/video handling. For text-only research keep using web_search and fetch_url at the top level — those stay outside the toolbox because they're called every research turn.",
			Members: []string{
				"browse_page",
				"screenshot_page",
				"find_image",
				"fetch_image",
				"download_video",
				"view_video",
			},
		},
	}
}

// IsBuiltinToolGroupID reports whether the given ID belongs to a
// framework-defined builtin. Used at storage boundaries to flip
// Delete semantics: a Delete on a builtin reverts the shadow (if
// any) rather than removing the definition outright. Exported so
// the admin UI can branch labels (Delete vs Revert) per row.
func IsBuiltinToolGroupID(id string) bool {
	for _, g := range builtinToolGroups() {
		if g.ID == id {
			return true
		}
	}
	return false
}

// retiredBuiltinToolGroupIDs is the set of IDs that USED to be in
// builtinToolGroups() but have been removed (because the member
// tools aren't in the global ChatTool registry, so the rewriter
// can't actually collapse them). Hydrate drops any DB shadows of
// these IDs to keep them out of the admin UI; they were dead state
// for any deployment that previously customized them.
var retiredBuiltinToolGroupIDs = map[string]bool{
	"builtin-agent-management": true,
	"builtin-recurring-tasks":  true,
	"builtin-facts":            true,
	"builtin-knowledge":        true,
	"builtin-chat-updates":     true, // earlier retirement
}

// hydrateToolGroupsCache populates the in-memory cache from the DB
// on first read after a cache invalidation, then layers in any
// builtins that don't have a shadow row. Cheap (both sources are
// small) and only happens once per CRUD cycle. Result: cache always
// reflects "effective" group state — shadow wins where present,
// in-code default elsewhere.
func hydrateToolGroupsCache(db Database) {
	toolGroupsMu.RLock()
	if toolGroupsCache != nil {
		toolGroupsMu.RUnlock()
		return
	}
	toolGroupsMu.RUnlock()

	toolGroupsMu.Lock()
	defer toolGroupsMu.Unlock()
	if toolGroupsCache != nil {
		return // raced with another caller; their hydrate stands
	}
	cache := map[string]ToolGroup{}
	for _, key := range db.Keys(toolGroupsTable) {
		var g ToolGroup
		if !db.Get(toolGroupsTable, key, &g) {
			continue
		}
		// Drop shadows of retired builtins from both cache and DB.
		// They're dead state from before these groups were removed
		// in code; leaving them would surface as confusing user-
		// created groups in the admin UI.
		if retiredBuiltinToolGroupIDs[g.ID] {
			db.Unset(toolGroupsTable, key)
			continue
		}
		cache[g.ID] = g
	}
	// Layer in builtins that aren't shadowed. A user's saved shadow
	// (same ID, different content) takes precedence; an absent
	// shadow surfaces the in-code default.
	for _, g := range builtinToolGroups() {
		if _, shadowed := cache[g.ID]; shadowed {
			continue
		}
		cache[g.ID] = g
	}
	toolGroupsCache = cache
}

// invalidateToolGroupsCache drops the in-memory snapshot so the next
// read re-hydrates from the DB. Called after every mutation; cheap.
func invalidateToolGroupsCache() {
	toolGroupsMu.Lock()
	toolGroupsCache = nil
	toolGroupsMu.Unlock()
}

// dedupeAndCleanMembers normalizes the member list: trims whitespace,
// drops empties, dedupes while preserving order. The admin UI's chip
// picker can post nearly-anything; we sanitize at the storage boundary.
func dedupeAndCleanMembers(members []string) []string {
	if len(members) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := members[:0]
	for _, m := range members {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}
