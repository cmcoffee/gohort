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
	"time"

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

	// AllowUploads lets an assigned NON-ADMIN user write into this store
	// through the upload endpoint. Off by default, and separate from
	// AllowedUsers on purpose: reading a folder and writing into it are
	// different grants, and the common store is a log directory somebody
	// else fills — pointing at /var/log and having any account with an
	// agent able to add files to it is not what registering a path meant.
	//
	// An admin can always upload (subject to AllowedUsers, like every
	// other reach), so turning this on is about widening the door, never
	// about opening one that was shut.
	AllowUploads bool `json:"allow_uploads,omitempty"`

	// RetentionDays deletes bundle subfolders older than this many days.
	// Zero — the default — keeps everything forever, which is the only
	// safe default for a store that may hold the only copy of something.
	//
	// Nothing deletes on a timer. The window makes a folder ELIGIBLE; an
	// admin still runs the sweep from Maintenance, and the dry run there
	// lists exactly what the delete would remove. Evidence is not the
	// kind of thing to reclaim automatically at 3am.
	RetentionDays int `json:"retention_days,omitempty"`

	// Ephemeral makes the store a working area rather than a record: each
	// subfolder is deleted automatically once nothing in it has changed for
	// LifetimeHours, with no admin sweep. For a landing zone whose contents
	// are copied onward (raw downloads that are extracted and pulled into
	// another store), where keeping the originals is not the point.
	//
	// RetentionDays's "nothing deletes on a timer" is about EVIDENCE, and a
	// store an admin has marked temporary has said it is not that. A folder
	// in use (pathhold) is never removed, whatever its age.
	Ephemeral     bool `json:"ephemeral,omitempty"`
	LifetimeHours int  `json:"lifetime_hours,omitempty"`

	// MaxGB caps the store's total size. Work that would take it past the cap
	// is refused with how full it is, and nothing is deleted to make room:
	// eviction would remove what nobody chose to remove. 0 is no cap.
	MaxGB int `json:"max_gb,omitempty"`

	// PullFrom names another store (by slug) this one copies prepared cases
	// in from, and PullSubpath the part of each case it takes ("prepared"
	// unless set; "." for the whole case). The link is the grant: an agent
	// with THIS store attached may pull from that one without it being
	// attached, seeing its case names but not their contents. See pull.go.
	PullFrom    string `json:"pull_from,omitempty"`
	PullSubpath string `json:"pull_subpath,omitempty"`
	// AgentsMayPull offers pull_<store> to agents this store is attached to.
	// Off unless an admin turns it on: an agent acts on a store only through
	// tools an admin chose to give it. The admin's Pull button and an app's
	// granted pull do not depend on it.
	AgentsMayPull bool `json:"agents_may_pull,omitempty"`

	// AppGrants are the apps an admin has let use this store, and how. See
	// app_grants.go: the grant is the app's, and covers whoever uses it.
	AppGrants []AppGrant `json:"app_grants,omitempty"`

	// Toolboxes is dead weight kept for one reason: an existing deployment's
	// attachments have to be readable ONCE so they can be carried onto the
	// commands they belonged to. See MigrateToolboxesOntoCommands, which
	// empties it. Nothing writes it, and a mapped command's approval now lives
	// on the command.
	//
	// Deprecated: read by the migration and by nothing else.
	Toolboxes []string `json:"toolboxes,omitempty"`
}

// UploadsAllowedFor reports whether user may upload into this store.
//
// Two conditions, and they answer different questions: AllowsUser is
// "may this person reach the store at all", AllowUploads is "may
// non-admins write into it". An admin passes the second by identity —
// they are the one who would have flipped the toggle.
func (s Store) UploadsAllowedFor(user string, isAdmin bool) bool {
	if !s.AllowsUser(user) {
		return false
	}
	return isAdmin || s.AllowUploads
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

// defaultLifetimeHours is how long a temporary store keeps a folder when the
// admin turned it temporary without saying.
const defaultLifetimeHours = 24

// lifetime is how long a temporary store keeps an unchanged folder.
func (s Store) lifetime() time.Duration {
	h := s.LifetimeHours
	if h <= 0 {
		h = defaultLifetimeHours
	}
	return time.Duration(h) * time.Hour
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
	if s.Ephemeral && s.LifetimeHours <= 0 {
		s.LifetimeHours = defaultLifetimeHours
	}
	if s.MaxGB < 0 {
		s.MaxGB = 0
	}
	if err := validatePull(db, s); err != nil {
		return s, err
	}
	if strings.TrimSpace(s.Slug) == "" {
		s.Slug = RefToolSlug(s.Name)
		if s.Slug == "" {
			return s, Error("that name produces no usable handle: use letters and digits")
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
