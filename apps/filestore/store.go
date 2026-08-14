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

// ListStores returns every registered store, by name.
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
