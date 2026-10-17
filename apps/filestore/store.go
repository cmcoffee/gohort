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

	// Who may reach this store, and how far. NOBODY until an admin says:
	// a folder of customer captures is not something every account should
	// hold, and an empty list that meant "everyone" failed open for every new
	// store and every list somebody cleared. Each entry is a username or a
	// team ("group:<id>"). Admins are not special here: managing the list is
	// admin-only, reaching the files takes an entry like anyone else.
	//
	//   Read-Only:   browse, search and read; an attached agent gets the
	//                list, search, read and evidence tools.
	//   Full Access: also upload and run the store's commands; an attached
	//                agent also gets its approved commands and pull, when
	//                its attachment allows changes.
	//
	// Everyone (accessEveryone) is an entry like a team: it names every
	// account, at the level of the list it is in, and is only there when an
	// admin added it. A named entry can raise someone above it.
	ReadOnly   []string `json:"read_only,omitempty"`
	FullAccess []string `json:"full_access,omitempty"`

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

	// PullFrom names another store (by slug) this one copies files and
	// folders in from, and PullSubpath the part of a folder it takes
	// ("prepared" unless set; "." for the whole folder). The link is the
	// grant: what pulls here never needs the other store attached, and sees
	// its names but not their contents. See pull.go.
	PullFrom    string `json:"pull_from,omitempty"`
	PullSubpath string `json:"pull_subpath,omitempty"`
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

	// Evidence indexes each subfolder the way an evidence bundle is (see
	// evidence.go): encrypted, per-file index, parsed timestamps, a timeline.
	// The folder itself is never changed. Off by default: the index is a
	// second, encrypted copy of what the folder holds.
	Evidence bool `json:"evidence,omitempty"`
}

// Access levels: what a person, or an agent of theirs, may do with a store.
const (
	accessRead = "read"
	accessFull = "full"
	// accessEveryone is the entry that names every account. Not a name an
	// account can have.
	accessEveryone = "*"
)

// accessFor is user's level on this store: accessFull, accessRead or "" for
// none. The higher of every entry naming them, a team they are on, or
// Everyone.
func (s Store) accessFor(user string) string {
	keys := ShareRecipientKeys(user)
	if len(keys) == 0 {
		return ""
	}
	keys = append(keys, accessEveryone)
	if listNames(s.FullAccess, keys) {
		return accessFull
	}
	if listNames(s.ReadOnly, keys) {
		return accessRead
	}
	return ""
}

// listNames reports whether list holds any of keys.
func listNames(list, keys []string) bool {
	for _, e := range list {
		for _, k := range keys {
			if strings.EqualFold(strings.TrimSpace(e), k) {
				return true
			}
		}
	}
	return false
}

// accessText says who may reach the store, for the admin page.
func (s Store) accessText() string {
	names := func(list []string) string {
		shown := make([]string, 0, len(list))
		var rest []string
		for _, e := range list {
			if e == accessEveryone {
				shown = append(shown, "everyone")
			} else {
				rest = append(rest, e)
			}
		}
		if len(rest) > 0 {
			shown = append(shown, ShareRecipientsText(rest))
		}
		return strings.Join(shown, ", ")
	}
	var parts []string
	if len(s.FullAccess) > 0 {
		parts = append(parts, "Full Access: "+names(s.FullAccess))
	}
	if len(s.ReadOnly) > 0 {
		parts = append(parts, "Read-Only: "+names(s.ReadOnly))
	}
	if len(parts) == 0 {
		return "nobody yet"
	}
	return strings.Join(parts, "; ")
}

// AllowsUser reports whether user may see and reach this store at all.
func (s Store) AllowsUser(user string) bool { return s.accessFor(user) != "" }

// FullAccessFor reports whether user may change what the store holds:
// upload, run its commands, pull into it.
func (s Store) FullAccessFor(user string) bool { return s.accessFor(user) == accessFull }

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
	// Someone with Full Access is not also listed Read-Only: one entry each,
	// the higher one.
	s.FullAccess = cleanAccessList(s.FullAccess, nil)
	s.ReadOnly = cleanAccessList(s.ReadOnly, s.FullAccess)
	if strings.TrimSpace(s.Slug) == "" {
		s.Slug = RefToolSlug(s.Name)
		if s.Slug == "" {
			return s, Error("that name produces no usable handle: use letters and digits")
		}
	}
	db.Set(storesTable, s.Slug, s)
	return s, nil
}

// cleanAccessList trims, drops blanks and repeats, and drops anyone in skip.
func cleanAccessList(list, skip []string) []string {
	var out []string
	for _, e := range list {
		e = strings.TrimSpace(e)
		if e == "" || listNames(out, []string{e}) || listNames(skip, []string{e}) {
			continue
		}
		out = append(out, e)
	}
	return out
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
	// Its commands go with it. Left behind they kept their mapping and their
	// approval, and a later store whose name made the same handle brought
	// them back, approved, to every agent attached to it.
	for _, cmd := range StoreCommandsFor(db, slug) {
		DeleteStoreCommand(db, slug, cmd.Name)
	}
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
