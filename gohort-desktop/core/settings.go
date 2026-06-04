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
	"strings"
	"sync"

	"github.com/cmcoffee/snugforge/kvlite"
)

const (
	SETTINGS_DIR_NAME = "gohort-desktop"
	// Two stores so the always-on daemon and the on-demand viewer
	// never contend on one bolt file (kvlite takes an exclusive OS
	// lock). SETTINGS_DB_NAME is the daemon's config authority;
	// VIEWER_DB_NAME holds the viewer's cookies + window state only.
	SETTINGS_DB_NAME = "settings.db"
	VIEWER_DB_NAME   = "viewer.db"
	SETTINGS_TABLE   = "desktop"

	SETTING_SERVER_URL  = "server_url"
	SETTING_COOKIES     = "cookies"
	SETTING_API_KEY     = "api_key"     // unified key: phantom hook/poll + desktop WS tool bridge
	SETTING_POLL_SECS   = "poll_secs"   // chat.db poll interval, seconds
	SETTING_CHATDB_PATH = "chatdb_path" // override for ~/Library/Messages/chat.db

	// SERVER_URL_SIDECAR is a plain, lock-free file the daemon writes
	// on every server-URL change so the viewer (which can't open the
	// daemon-locked settings.db) can learn where to proxy.
	SERVER_URL_SIDECAR = "server_url.txt"

	// API_KEY_SIDECAR is the reverse handoff: the VIEWER (which has the
	// logged-in session cookie) mints the bridge key from the server and
	// writes it here, lock-free, so the daemon — which can't present a
	// cookie — can read it without opening the viewer's store. This is
	// what makes the key auto-negotiated instead of a manual setup step.
	API_KEY_SIDECAR = "api_key.txt"
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

// open_settings_store opens (and creates if needed) the named on-disk
// settings DB in the shared config dir. The daemon passes
// SETTINGS_DB_NAME, the viewer VIEWER_DB_NAME — two files so the two
// processes never contend on one kvlite/bolt exclusive lock. Returns
// an error rather than panicking so main can surface a clean Fatal
// with the actual filesystem reason.
func open_settings_store(db_name string) (*Settings, error) {
	dir, err := settings_dir()
	if err != nil {
		return nil, fmt.Errorf("locate config dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	db, err := kvlite.Open(filepath.Join(dir, db_name))
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

// ConfigDir returns the resolved config directory (where the sidecars
// live). Exported for diagnostics — so a process can log WHICH directory
// it's reading, to catch viewer/daemon dir mismatches.
func ConfigDir() string {
	d, err := settings_dir()
	if err != nil {
		return "(unresolved: " + err.Error() + ")"
	}
	return d
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

// APIKey returns the unified daemon API key (used for both phantom's
// /api/hook + /api/poll and the desktop WS tool bridge). Empty until
// configured via --setup.
func (s *Settings) APIKey() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var v string
	s.db.Get(SETTINGS_TABLE, SETTING_API_KEY, &v)
	return v
}

// SetAPIKey persists the unified daemon API key.
func (s *Settings) SetAPIKey(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Set(SETTINGS_TABLE, SETTING_API_KEY, key)
}

// PollSecs returns the chat.db poll interval in seconds, or 0 if
// unset (callers apply their own default/minimum).
func (s *Settings) PollSecs() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var v int
	s.db.Get(SETTINGS_TABLE, SETTING_POLL_SECS, &v)
	return v
}

// SetPollSecs persists the chat.db poll interval.
func (s *Settings) SetPollSecs(secs int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Set(SETTINGS_TABLE, SETTING_POLL_SECS, secs)
}

// ChatDBPath returns the override path to Messages' chat.db, or empty
// for the default (~/Library/Messages/chat.db).
func (s *Settings) ChatDBPath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var v string
	s.db.Get(SETTINGS_TABLE, SETTING_CHATDB_PATH, &v)
	return v
}

// SetChatDBPath persists a chat.db path override.
func (s *Settings) SetChatDBPath(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Set(SETTINGS_TABLE, SETTING_CHATDB_PATH, path)
}

// WriteServerURLSidecar writes the bare server origin to a plain,
// lock-free file in the config dir so the viewer can read it without
// opening the daemon-locked settings.db. Called by the daemon on
// every server-URL change. Best-effort: a write failure is returned
// but is non-fatal to the daemon.
func WriteServerURLSidecar(url string) error {
	dir, err := settings_dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, SERVER_URL_SIDECAR), []byte(url), 0o600)
}

// ReadServerURLSidecar reads the server origin the daemon last wrote.
// Returns "" if the file is absent (daemon never configured) so the
// viewer can show its "configure via the menu bar" hint.
func ReadServerURLSidecar() string {
	dir, err := settings_dir()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dir, SERVER_URL_SIDECAR))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// WriteAPIKeySidecar writes the auto-provisioned bridge key to a plain,
// lock-free file in the config dir so the daemon can read it without the
// viewer's store. Called by the viewer after it mints the key from the
// server with its session cookie. Best-effort.
func WriteAPIKeySidecar(key string) error {
	dir, err := settings_dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, API_KEY_SIDECAR), []byte(key), 0o600)
}

// ReadAPIKeySidecar reads the bridge key the viewer last provisioned.
// Returns "" if absent (never provisioned — daemon falls back to the
// manually-set settings key).
func ReadAPIKeySidecar() string {
	dir, err := settings_dir()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dir, API_KEY_SIDECAR))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
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
