// Shared read-allowlist for filesystem tools. Centralizes the list of
// root directories that any filesystem.* tool may resolve paths
// against. read_local_file and list_directory both check here, so a
// folder added once becomes visible to every read-side tool at the
// same time.
//
// State is persisted in Settings under SETTING_READ_ALLOWLIST as a
// []string of absolute paths. On first run (no persisted value yet)
// the seed defaults are written so the operator can immediately
// inspect them in the menu, then prune what they don't want.
//
// Add a new tool that needs read access? Call PathAllowed(abs) in
// the handler. Add a new persisted setting that scopes a DIFFERENT
// kind of access (e.g. write-allowlist)? Make a sibling file —
// don't widen this one beyond reads.

package core

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// SETTING_READ_ALLOWLIST is the kvlite key under which the persisted
// allowlist lives. Stable identifier — renaming it would orphan
// existing user configuration.
const SETTING_READ_ALLOWLIST = "fs_read_allowlist"

// fs_allowlist_state caches the live allowlist in memory so handlers
// don't hit kvlite on every PathAllowed check. Set on first Init,
// refreshed by every mutator.
var (
	fs_allowlist_mu       sync.RWMutex
	fs_allowlist_state    []string
	fs_allowlist_settings *Settings
)

// InitFSAllowlist wires the allowlist to Settings and seeds the
// defaults on first run. Idempotent — safe to call repeatedly. main.go
// invokes this once after Config loads.
func InitFSAllowlist(settings *Settings) {
	if settings == nil {
		return
	}
	fs_allowlist_mu.Lock()
	defer fs_allowlist_mu.Unlock()
	fs_allowlist_settings = settings

	var loaded []string
	found := settings.GetStrings(SETTING_READ_ALLOWLIST, &loaded)
	if !found || len(loaded) == 0 {
		loaded = default_read_allowlist()
		if err := settings.SetStrings(SETTING_READ_ALLOWLIST, loaded); err != nil {
			Warn("[fs_allowlist] seed write failed: %v", err)
		} else {
			Log("[fs_allowlist] seeded with %d default root(s): %v", len(loaded), loaded)
		}
	}
	fs_allowlist_state = normalize_roots(loaded)
}

// AllowedReadRoots returns a sorted copy of the live allowlist. Safe
// for callers to mutate the result — the backing slice is not shared.
func AllowedReadRoots() []string {
	fs_allowlist_mu.RLock()
	defer fs_allowlist_mu.RUnlock()
	out := make([]string, len(fs_allowlist_state))
	copy(out, fs_allowlist_state)
	return out
}

// SetAllowedReadRoots replaces the allowlist with the given paths
// (after normalization). Persists immediately. Returns the
// normalized in-memory list on success so callers can show the
// canonical form back to the user.
func SetAllowedReadRoots(paths []string) ([]string, error) {
	clean := normalize_roots(paths)
	fs_allowlist_mu.Lock()
	defer fs_allowlist_mu.Unlock()
	if fs_allowlist_settings != nil {
		if err := fs_allowlist_settings.SetStrings(SETTING_READ_ALLOWLIST, clean); err != nil {
			return nil, err
		}
	}
	fs_allowlist_state = clean
	return append([]string(nil), clean...), nil
}

// AddAllowedReadRoot adds path to the allowlist. No-op if the path is
// already present (after normalization). Returns the new allowlist.
func AddAllowedReadRoot(path string) ([]string, error) {
	clean, err := resolve_root(path)
	if err != nil {
		return nil, err
	}
	current := AllowedReadRoots()
	for _, r := range current {
		if r == clean {
			return current, nil // already present
		}
	}
	return SetAllowedReadRoots(append(current, clean))
}

// RemoveAllowedReadRoot drops path from the allowlist. No-op if the
// path isn't present. Returns the new allowlist.
func RemoveAllowedReadRoot(path string) ([]string, error) {
	clean, err := resolve_root(path)
	if err != nil {
		return nil, err
	}
	current := AllowedReadRoots()
	out := make([]string, 0, len(current))
	for _, r := range current {
		if r != clean {
			out = append(out, r)
		}
	}
	return SetAllowedReadRoots(out)
}

// PathAllowed reports whether abs resolves under any configured read
// root. Caller is responsible for resolving symlinks (filepath.EvalSymlinks)
// BEFORE calling — that lets the filesystem tool layer keep one
// canonical place for the symlink-safety policy.
func PathAllowed(abs string) bool {
	abs = filepath.Clean(abs)
	for _, root := range AllowedReadRoots() {
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			continue
		}
		if rel == "." || (!strings.HasPrefix(rel, "..") && !strings.HasPrefix(rel, string(os.PathSeparator)+"..")) {
			return true
		}
	}
	return false
}

// resolve_root normalizes a single user-supplied path into the form
// the allowlist stores: absolute, symlinks resolved when possible,
// trailing slashes stripped. Empty input is an error. Non-existent
// paths are NOT an error — the user may add a path that doesn't
// exist yet (e.g. a project dir created later), and refusing to
// store it would be more annoying than useful.
func resolve_root(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", os.ErrInvalid
	}
	if strings.HasPrefix(path, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	return filepath.Clean(abs), nil
}

// normalize_roots cleans, dedupes, and sorts the allowlist. Called on
// every mutation so the persisted state and in-memory state always
// agree on canonical form. Empty entries are dropped — operator
// editing in a freeform text input shouldn't produce ghost roots.
func normalize_roots(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		clean, err := resolve_root(raw)
		if err != nil || clean == "" {
			continue
		}
		if seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, clean)
	}
	sort.Strings(out)
	return out
}

// default_read_allowlist returns the first-run seed list. Same paths
// that read_local_file shipped with before the allowlist became
// persisted, so existing users see no behavior change. Chosen for
// typical Mac log / temp file read needs:
//
//   - /var/log: system logs
//   - /tmp: scratch files
//   - ~/Library/Logs: per-user app logs (Console.app's source)
//   - ~/.gohort: the operator's own gohort data dir
//
// Deliberately NOT included: $HOME root, /, /etc, cloud-sync dirs —
// adding any of those exposes credentials / browser profiles / etc.
// The operator can add them explicitly if they really want to.
func default_read_allowlist() []string {
	roots := []string{
		"/var/log",
		"/tmp",
	}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots,
			filepath.Join(home, "Library", "Logs"),
			filepath.Join(home, ".gohort"),
		)
	}
	return roots
}
