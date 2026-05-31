// Persistent desktop settings — currently just the gohort server URL,
// shaped so adding fields later (preferred orchestrate session, last
// window position, per-tool toggles) is a single key in the same db.
//
// Backed by snugforge/kvlite (encrypted bolt). Per-machine config
// directory (~/Library/Application Support/gohort-desktop on macOS,
// $XDG_CONFIG_HOME/gohort-desktop on Linux, %APPDATA%\gohort-desktop
// on Windows) chosen by os.UserConfigDir so we play nice with each
// platform's conventions out of the box.
//
// Auth is intentionally NOT stored here: cookie auth is delegated to
// gohort's own /login page rendered inside the webview, the
// gohort_session cookie lives in the webview's cookie store, and the
// desktop never sees the password.

package core

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/cmcoffee/snugforge/kvlite"
)

const (
	SETTINGS_DIR_NAME  = "gohort-desktop"
	SETTINGS_DB_NAME   = "settings.db"
	SETTINGS_TABLE     = "desktop"
	SETTING_SERVER_URL = "server_url"
	SETTING_COOKIES    = "cookies"
)

// StoredCookie pairs a cookie with the origin URL it came from. The
// origin is what cookiejar uses to decide which requests get the
// cookie sent back, so we have to remember it alongside the cookie
// itself — cookiejar's own storage doesn't expose it on read.
type StoredCookie struct {
	URL    string       `json:"url"`
	Cookie *http.Cookie `json:"cookie"`
}

// Settings is the persistent settings handle. Thread-safe; concurrent
// reads and writes are serialized through the embedded mutex. Close
// before exit so the underlying bolt file flushes cleanly.
type Settings struct {
	mu sync.RWMutex
	db kvlite.Store
}

// open_settings_store opens (and creates if needed) the on-disk
// settings DB. Returns an error rather than panicking so main can
// surface a clean Fatal with the actual filesystem reason.
func open_settings_store() (*Settings, error) {
	dir, err := settings_dir()
	if err != nil {
		return nil, fmt.Errorf("locate config dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	db, err := kvlite.Open(filepath.Join(dir, SETTINGS_DB_NAME))
	if err != nil {
		return nil, fmt.Errorf("open settings db: %w", err)
	}
	return &Settings{db: db}, nil
}

// settings_dir returns the platform-appropriate config directory for
// gohort-desktop. Falls through os.UserConfigDir so the desktop ends
// up wherever the OS expects app config to live.
func settings_dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, SETTINGS_DIR_NAME), nil
}

// ServerURL returns the persisted gohort server URL. Empty string
// means not yet configured — the proxy interprets that as "show the
// first-run configure page".
func (s *Settings) ServerURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var v string
	s.db.Get(SETTINGS_TABLE, SETTING_SERVER_URL, &v)
	return v
}

// SetServerURL persists the server URL. Caller is responsible for
// validation (URL parse + reachability probe) — this layer just
// writes.
func (s *Settings) SetServerURL(url string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Set(SETTINGS_TABLE, SETTING_SERVER_URL, url)
}

// ClearServerURL drops the persisted URL, forcing the configure
// page back on the next webview request. Used by the "Change server"
// button on the unreachable-server error page and by ResetSettings.
func (s *Settings) ClearServerURL() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Unset(SETTINGS_TABLE, SETTING_SERVER_URL)
}

// Close flushes and releases the underlying bolt file. Call from the
// Wails shutdown handler.
func (s *Settings) Close() error {
	return s.db.Close()
}

// LoadCookies returns every persisted cookie. Used by
// PersistentCookieJar on startup to rehydrate the jar so login
// survives restarts. Returns empty slice if nothing is stored or the
// read fails (we treat persistence as best-effort — a corrupt cookie
// row just means the user logs in again).
func (s *Settings) LoadCookies() []StoredCookie {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var cookies []StoredCookie
	s.db.Get(SETTINGS_TABLE, SETTING_COOKIES, &cookies)
	return cookies
}

// AppendCookies merges new cookies into the persisted set. Cookies
// are de-duplicated by (url, name) — a fresh value for an existing
// name replaces the old, matching how cookiejar handles overwrites.
// Expired cookies (Expires in the past, or MaxAge < 0) are dropped
// rather than persisted, so the disk record self-cleans over time.
func (s *Settings) AppendCookies(origin_url string, cookies []*http.Cookie) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var existing []StoredCookie
	s.db.Get(SETTINGS_TABLE, SETTING_COOKIES, &existing)

	// Build a lookup keyed on (url, name) so a fresh cookie replaces
	// any earlier one with the same key.
	key := func(u, name string) string { return u + "\x00" + name }
	index := make(map[string]int, len(existing))
	for i, sc := range existing {
		if sc.Cookie == nil {
			continue
		}
		index[key(sc.URL, sc.Cookie.Name)] = i
	}

	for _, c := range cookies {
		if c == nil {
			continue
		}
		// Drop deletion markers (MaxAge<0 or empty value with explicit
		// expiry in the past) rather than storing them.
		if c.MaxAge < 0 || (c.Value == "" && !c.Expires.IsZero()) {
			delete(index, key(origin_url, c.Name))
			continue
		}
		entry := StoredCookie{URL: origin_url, Cookie: c}
		if idx, ok := index[key(origin_url, c.Name)]; ok {
			existing[idx] = entry
		} else {
			existing = append(existing, entry)
			index[key(origin_url, c.Name)] = len(existing) - 1
		}
	}

	// Compact: rebuild without any entries removed above.
	compact := make([]StoredCookie, 0, len(index))
	seen := make(map[string]bool, len(index))
	for _, sc := range existing {
		if sc.Cookie == nil {
			continue
		}
		k := key(sc.URL, sc.Cookie.Name)
		if _, kept := index[k]; !kept {
			continue
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		compact = append(compact, sc)
	}

	return s.db.Set(SETTINGS_TABLE, SETTING_COOKIES, compact)
}

// ClearCookies drops every persisted cookie. Triggered on server-URL
// change (different host → different identity) and on Log Out from
// the desktop menu (server tells us to delete, we should too).
func (s *Settings) ClearCookies() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Unset(SETTINGS_TABLE, SETTING_COOKIES)
}

// GetBool reads a boolean key into out. Returns the kvlite "found"
// state. Generic getter for arbitrary scalar toggles (auto-approve,
// bridge enable/disable, etc.) so we don't grow a typed method per
// setting.
func (s *Settings) GetBool(key string, out *bool) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	found, _ := s.db.Get(SETTINGS_TABLE, key, out)
	return found
}

// SetBool persists a boolean key.
func (s *Settings) SetBool(key string, v bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Set(SETTINGS_TABLE, key, v)
}

// GetStrings reads a []string key into out. Same shape as GetBool —
// generic getter so callers can persist small slices (allowlists,
// tag lists, etc.) without growing a typed setting per use case.
// Returns the kvlite "found" state.
func (s *Settings) GetStrings(key string, out *[]string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	found, _ := s.db.Get(SETTINGS_TABLE, key, out)
	return found
}

// SetStrings persists a []string key.
func (s *Settings) SetStrings(key string, v []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Set(SETTINGS_TABLE, key, v)
}
