// Admin-curated app groups. A group bundles several apps (by their web
// paths) under one name so an admin can grant a whole set of apps to a user
// in one assignment instead of ticking each app per user. Editing a group
// re-provisions every user assigned to it — access resolves through the group
// at check time (see AuthResolveUserApps), so membership changes propagate
// without touching each user record.
//
// Storage lives under the deployment-wide auth DB so the admin app and the
// access checker read from one place. This mirrors the ToolGroup pattern
// (core/tool_groups.go) but in a different domain: app groups gate ACCESS to
// apps; tool groups collapse chat TOOLS in the LLM catalog. There are no
// framework builtins here — app groups are entirely admin-created.

package core

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// appGroupsTable holds AppGroup records keyed by ID, in AuthDB() so the admin
// UI and UserHasAppAccess share one source of truth.
const appGroupsTable = "app_groups"

// AppGroup is the persisted shape. Apps reference apps by their web path
// (e.g. "/guides", "/servitor"); a path that no longer maps to a registered
// app simply grants nothing at resolve time, so a removed app drops out of the
// group without needing storage cleanup.
type AppGroup struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`        // human-readable ("Writers", "Ops")
	Description string    `json:"description"` // optional admin note
	Apps        []string  `json:"apps"`        // app web paths granted to members
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
}

// In-memory snapshot, refreshed on each mutation. Access checks are warm-path
// (one per dashboard card per request), so serve them from the cache and only
// touch the DB on CRUD. mu guards both fields together.
var (
	appGroupsMu    sync.RWMutex
	appGroupsCache map[string]AppGroup
)

// LoadAppGroups returns all groups, sorted by Name. db nil → nil (lets the
// admin UI render before AuthDB is wired).
func LoadAppGroups(db Database) []AppGroup {
	if db == nil {
		return nil
	}
	hydrateAppGroupsCache(db)
	appGroupsMu.RLock()
	out := make([]AppGroup, 0, len(appGroupsCache))
	for _, g := range appGroupsCache {
		out = append(out, g)
	}
	appGroupsMu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// LoadAppGroup fetches one group by ID. Returns false when not found.
func LoadAppGroup(db Database, id string) (AppGroup, bool) {
	if db == nil || id == "" {
		return AppGroup{}, false
	}
	hydrateAppGroupsCache(db)
	appGroupsMu.RLock()
	defer appGroupsMu.RUnlock()
	g, ok := appGroupsCache[id]
	return g, ok
}

// SaveAppGroup upserts a group, stamping timestamps + assigning an ID on new
// records. Returns the saved record so the admin UI can echo it back.
func SaveAppGroup(db Database, g AppGroup) (AppGroup, error) {
	if db == nil {
		return g, fmt.Errorf("app groups: db not initialized")
	}
	g.Name = strings.TrimSpace(g.Name)
	if g.Name == "" {
		return g, fmt.Errorf("app groups: name is required")
	}
	g.Apps = dedupeAndCleanMembers(g.Apps)
	now := time.Now()
	if g.ID == "" {
		g.ID = UUIDv4()
		g.Created = now
	}
	if g.Created.IsZero() {
		g.Created = now
	}
	g.Updated = now
	db.Set(appGroupsTable, g.ID, g)
	invalidateAppGroupsCache()
	return g, nil
}

// DeleteAppGroup removes a group by ID. Users assigned to it keep the group ID
// in their record, but it resolves to no apps once gone — harmless dangling
// reference, cleaned up whenever the user is next edited.
func DeleteAppGroup(db Database, id string) error {
	if db == nil || id == "" {
		return fmt.Errorf("app groups: id is required")
	}
	db.Unset(appGroupsTable, id)
	invalidateAppGroupsCache()
	return nil
}

// ExpandAppGroups returns the union of app paths granted by the given group
// IDs. Unknown IDs contribute nothing. Order is not significant (the caller
// dedupes against the user's own app grants).
func ExpandAppGroups(db Database, ids []string) []string {
	if db == nil || len(ids) == 0 {
		return nil
	}
	hydrateAppGroupsCache(db)
	appGroupsMu.RLock()
	defer appGroupsMu.RUnlock()
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		g, ok := appGroupsCache[id]
		if !ok {
			continue
		}
		for _, p := range g.Apps {
			if p != "" && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// hydrateAppGroupsCache populates the in-memory cache from the DB on first read
// after an invalidation. Cheap (small table) and once per CRUD cycle.
func hydrateAppGroupsCache(db Database) {
	appGroupsMu.RLock()
	if appGroupsCache != nil {
		appGroupsMu.RUnlock()
		return
	}
	appGroupsMu.RUnlock()

	appGroupsMu.Lock()
	defer appGroupsMu.Unlock()
	if appGroupsCache != nil {
		return // raced with another caller; their hydrate stands
	}
	cache := map[string]AppGroup{}
	for _, key := range db.Keys(appGroupsTable) {
		var g AppGroup
		if db.Get(appGroupsTable, key, &g) {
			cache[g.ID] = g
		}
	}
	appGroupsCache = cache
}

// invalidateAppGroupsCache drops the snapshot so the next read re-hydrates.
func invalidateAppGroupsCache() {
	appGroupsMu.Lock()
	appGroupsCache = nil
	appGroupsMu.Unlock()
}
