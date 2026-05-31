// Runtime configuration for gohort-desktop. The *server URL* is
// persisted via Settings (kvlite — see settings.go) because the user
// chooses it interactively on first launch. Cosmetic knobs (window
// size, landing path) stay env-var-driven since they're dev concerns.
//
// Dev override: GOHORT_DESKTOP_ADDR, if set, wins over the persisted
// URL. Useful for running against a local gohort serve without
// blowing away the saved production URL.
//
// All accessors are safe to call from any goroutine — Settings
// synchronizes its own reads/writes.

package core

import (
	"os"
	"strconv"
)

const (
	DEFAULT_LANDING_PATH  = "/orchestrate/"
	DEFAULT_WINDOW_WIDTH  = 1280
	DEFAULT_WINDOW_HEIGHT = 800
	MIN_WINDOW_WIDTH      = 720
	MIN_WINDOW_HEIGHT     = 480

	ENV_GOHORT_ADDR   = "GOHORT_DESKTOP_ADDR"
	ENV_LANDING_PATH  = "GOHORT_DESKTOP_PATH"
	ENV_WINDOW_WIDTH  = "GOHORT_DESKTOP_WIDTH"
	ENV_WINDOW_HEIGHT = "GOHORT_DESKTOP_HEIGHT"
)

// Config bundles the desktop's runtime knobs + the persistent settings
// handle. One instance per process, constructed at startup by
// LoadConfig and passed into the proxy + Wails app.
type Config struct {
	settings *Settings

	landing_path  string
	window_width  int
	window_height int
}

// LoadConfig opens the on-disk settings store and reads env-driven
// cosmetic knobs. Returns an error (rather than calling Fatal) so
// main.go can decide how to surface a "can't write to ~/Library"
// problem — usually a permissions issue worth telling the user about
// explicitly.
func LoadConfig() (*Config, error) {
	settings, err := open_settings_store()
	if err != nil {
		return nil, err
	}

	c := &Config{
		settings:      settings,
		landing_path:  env_or(ENV_LANDING_PATH, DEFAULT_LANDING_PATH),
		window_width:  DEFAULT_WINDOW_WIDTH,
		window_height: DEFAULT_WINDOW_HEIGHT,
	}
	if s := os.Getenv(ENV_WINDOW_WIDTH); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= MIN_WINDOW_WIDTH {
			c.window_width = n
		}
	}
	if s := os.Getenv(ENV_WINDOW_HEIGHT); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= MIN_WINDOW_HEIGHT {
			c.window_height = n
		}
	}
	return c, nil
}

// ServerURL returns the active gohort server URL — env var wins for
// dev override, otherwise the persisted value. Empty string means
// "show first-run configure page".
func (c *Config) ServerURL() string {
	if v := os.Getenv(ENV_GOHORT_ADDR); v != "" {
		return v
	}
	return c.settings.ServerURL()
}

// SetServerURL persists a new server URL (assumes caller already
// validated form + reachability).
func (c *Config) SetServerURL(url string) error {
	return c.settings.SetServerURL(url)
}

// ClearServerURL drops the persisted URL so the next webview request
// renders the configure page. Used by the "Change server" button on
// the unreachable-server error page.
func (c *Config) ClearServerURL() error {
	return c.settings.ClearServerURL()
}

// IsConfigured reports whether the desktop has somewhere to point.
// True when env override is set or the user has saved a URL.
func (c *Config) IsConfigured() bool {
	return c.ServerURL() != ""
}

// Settings exposes the underlying settings store for components
// (PersistentCookieJar) that need direct kvlite access.
func (c *Config) Settings() *Settings { return c.settings }

// LandingPath returns the initial URL path opened in the webview
// after the server is reachable (default "/orchestrate/").
func (c *Config) LandingPath() string { return c.landing_path }

// WindowSize returns the (width, height) the Wails window should
// open at.
func (c *Config) WindowSize() (int, int) { return c.window_width, c.window_height }

// Close releases the settings DB. Call from the shutdown handler.
func (c *Config) Close() error {
	if c.settings == nil {
		return nil
	}
	return c.settings.Close()
}

// env_or returns the env var's value if non-empty; otherwise the
// fallback. Package-private — callers use LoadConfig + accessors.
func env_or(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
