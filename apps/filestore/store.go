// Admin-registered file stores.
//
// A path on the gohort host is a DEPLOYMENT fact, not a per-user
// preference: it is the same folder whoever is asking, and letting a
// user name their own would make every account a filesystem reader. So
// the list lives in the root store and only an admin edits it, which
// also makes the allowlist meaningful — the set of readable paths is
// exactly the set somebody deliberately typed.

package filestore

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// storesTable holds the admin-registered roots, keyed by slug.
const storesTable = "file_stores"

// Store is one registered directory. The store itself is what an agent
// attaches to; its subfolders, if it has any, are what a search can be
// scoped to.
type Store struct {
	Slug        string `json:"slug"` // stable handle; folds into tool names
	Name        string `json:"name"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"` // what lands here, for the agent's benefit

	// AllowedUsers restricts who may reach this store. Empty = every
	// user, matching FeatureAccessPolicy's shape so there is one answer
	// in this codebase to "what does an empty allow-list mean".
	//
	// It matters more here than the default suggests. An admin registers
	// a path once; everything under it is then readable by any agent
	// that attaches the store, and a folder of customer log bundles is
	// not something every account should hold. Configuring the store is
	// already admin-only; WITHOUT this, reading it was not restricted at
	// all, which is a strange pairing — the gate was on the cheap half.
	//
	// Empty stays open because closing it by default would silently
	// break every store registered before this existed, and a security
	// change that presents as "the tools vanished" is one nobody
	// diagnoses correctly.
	AllowedUsers []string `json:"allowed_users,omitempty"`
}

// AllowsUser reports whether user may see and reach this store.
//
// Admin is deliberately NOT special-cased here. An admin who wants a
// store restricted to someone else has said so, and quietly reading it
// anyway would make the setting mean something other than what it says.
// Admin retains the thing admin should have: the ability to CHANGE the
// list.
func (s Store) AllowsUser(user string) bool {
	if len(s.AllowedUsers) == 0 {
		return true
	}
	user = strings.TrimSpace(user)
	for _, u := range s.AllowedUsers {
		if strings.EqualFold(strings.TrimSpace(u), user) {
			return true
		}
	}
	return false
}

// Valid reports whether a store is usable, and says why not when it
// isn't.
//
// Checked at save time AND at read time. At save because an operator
// typing a path deserves to hear about a typo immediately; at read
// because a folder that existed at save can be unmounted, renamed, or
// have its permissions changed, and "the tool silently returns nothing"
// is the worst possible presentation of that.
func (s Store) Valid() error {
	if strings.TrimSpace(s.Name) == "" {
		return Error("a file store needs a name")
	}
	path := strings.TrimSpace(s.Path)
	if path == "" {
		return Error("a file store needs a path")
	}
	if !filepath.IsAbs(path) {
		return Error("the path must be absolute, so it cannot depend on where the server happens to be running: " + path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return Error("that path cannot be read: " + err.Error())
	}
	if !info.IsDir() {
		return Error("that path is a file, not a folder: " + path)
	}
	return nil
}

// SaveStore writes a store, minting a slug from the name on first save.
func SaveStore(db Database, s Store) (Store, error) {
	if db == nil {
		return s, Error("no store available")
	}
	if err := s.Valid(); err != nil {
		return s, err
	}
	s.Path = filepath.Clean(strings.TrimSpace(s.Path))
	if strings.TrimSpace(s.Slug) == "" {
		s.Slug = RefToolSlug(s.Name)
		if s.Slug == "" {
			return s, Error("that name produces no usable handle — use letters and digits")
		}
	}
	db.Set(storesTable, s.Slug, s)
	return s, nil
}

// LoadStore reads one store by slug.
func LoadStore(db Database, slug string) (Store, bool) {
	if db == nil || strings.TrimSpace(slug) == "" {
		return Store{}, false
	}
	var s Store
	if !db.Get(storesTable, slug, &s) {
		return Store{}, false
	}
	return s, true
}

// ListStores returns every registered store, by name. ADMIN view: no
// access filtering, because the admin table has to show what exists in
// order to manage it.
func ListStores(db Database) []Store {
	if db == nil {
		return nil
	}
	var out []Store
	for _, k := range db.Keys(storesTable) {
		var s Store
		if db.Get(storesTable, k, &s) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// DeleteStore removes a store. The folder on disk is never touched:
// nothing here owns those files, and a config screen that deletes data
// is a config screen nobody can safely use.
func DeleteStore(db Database, slug string) {
	if db == nil || slug == "" {
		return
	}
	db.Unset(storesTable, slug)
}

// StoresForUser is ListStores filtered by access — the view every
// non-admin surface must use.
//
// Separate function rather than a flag on ListStores: a filtered and an
// unfiltered list are different enough that a boolean argument at the
// call site tells the reader nothing, and the unfiltered one is only
// ever right on the admin page.
func StoresForUser(db Database, user string) []Store {
	all := ListStores(db)
	out := all[:0:0]
	for _, s := range all {
		if s.AllowsUser(user) {
			out = append(out, s)
		}
	}
	return out
}
