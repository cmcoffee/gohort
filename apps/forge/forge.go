// Package forge is gohort's self-improvement surface: an admin-only,
// web-only app that runs claude-code (or any CLI) inside a detached
// tmux session in gohort's own source tree, streamed to the browser
// through an interactive xterm terminal — plus a build-gated
// "rebuild + restart" actuator so the operator can recompile and
// bounce the server from the same page.
//
// The design is deliberately thin. claude-code brings its own diff
// review, permission prompts, and interactive steering; forge does not
// reimplement any of that. tmux is the persistence layer: claude runs
// in a detached session that outlives a gohort restart, and the web
// terminal simply *attaches* to it (surviving the bounce exactly like
// servitor's terminal reconnect). What forge adds on top is small:
//
//   - a local-PTY WebSocket backend that attaches to the tmux session
//     (terminal.go),
//   - a build-gated restart actuator that refuses to restart on a red
//     build (rebuild.go),
//   - a config surface for the environment-specific knobs — working
//     dir, claude command, rebuild command, restart command — none of
//     which are hardcoded (config lives in the DB).
//
// Safety invariants that are NOT settings (never disableable): the
// build-gate before restart, and admin-only access. The blast radius
// here is the whole server editing its own running source, so the app
// is gated to admins and every risk dial (the restart command) defaults
// to off until an operator sets it.
package forge

import (
	"net/http"
	"os"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

func init() { RegisterApp(new(Forge)) }

// forgeConfigTable is the kvlite table holding the single config record.
const forgeConfigTable = "forge_config"

// ForgeConfig holds the environment-specific knobs. Everything here is
// operator-configurable; nothing is compiled in. Persisted in the DB
// so it survives restarts (and so the "which binary / which restart
// command" answer is a setting, not a code change).
type ForgeConfig struct {
	WorkDir     string `json:"work_dir"`     // where claude runs + where the build happens; "" = server cwd
	ClaudeCmd   string `json:"claude_cmd"`   // command launched in the tmux session
	RebuildCmd  string `json:"rebuild_cmd"`  // the build-gate; must exit 0 before a restart fires
	RestartCmd  string `json:"restart_cmd"`  // how to bounce the server; "" = restart disabled (safe default)
	TmuxSession string `json:"tmux_session"` // detached tmux session name claude lives in
}

// defaults fills any unset field with a sane default. Called after load
// so an empty/absent record still yields a working config.
func (c ForgeConfig) defaults() ForgeConfig {
	if strings.TrimSpace(c.ClaudeCmd) == "" {
		c.ClaudeCmd = "claude"
	}
	if strings.TrimSpace(c.RebuildCmd) == "" {
		// `go install .` (not `go build ./...`): in module mode it both
		// compiles AND installs the server binary to GOBIN, so the restart
		// command's copy moves a freshly-built binary. `go install gohort`
		// fails in module mode — the bare path isn't a valid package.
		c.RebuildCmd = "go install ."
	}
	if strings.TrimSpace(c.TmuxSession) == "" {
		c.TmuxSession = "forge"
	}
	// RestartCmd intentionally left empty by default — the high-blast
	// dial is off until an operator opts in.
	return c
}

// resolvedWorkDir returns the working directory claude + the build run
// in, falling back to the server's cwd when unset.
func (c ForgeConfig) resolvedWorkDir() string {
	if d := strings.TrimSpace(c.WorkDir); d != "" {
		return d
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// Forge is the app entry point. Embedding AppCore wires in the shared
// framework state (DB, LLM handles, flag set).
type Forge struct {
	AppCore
}

// loadConfig reads the persisted config (with defaults applied).
func (T *Forge) loadConfig() ForgeConfig {
	var c ForgeConfig
	if T.DB != nil {
		T.DB.Get(forgeConfigTable, "current", &c)
	}
	return c.defaults()
}

// saveConfig persists the config.
func (T *Forge) saveConfig(c ForgeConfig) {
	if T.DB != nil {
		T.DB.Set(forgeConfigTable, "current", c)
	}
}

// requireAdmin gates a handler on a logged-in admin. Returns the user
// id and true when allowed; writes the error response and returns false
// otherwise. Every forge handler goes through this — the surface edits
// and restarts the running server, so it is admins-only, full stop.
func (T *Forge) requireAdmin(w http.ResponseWriter, r *http.Request) (string, bool) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return "", false
	}
	// Admin status lives in AuthDB() (the user/session store), which is a
	// distinct handle from T.DB (the root DB menu.go wires in). Passing
	// T.DB here would miss the user record and read as "not admin" — the
	// admin app uses AuthDB() for the same reason.
	if AuthDB == nil || !AuthIsAdmin(AuthDB(), r) {
		http.Error(w, "admin only", http.StatusForbidden)
		return "", false
	}
	return user, true
}

// --- core.Agent interface ----------------------------------------------------

func (T Forge) Name() string         { return "forge" }
func (T Forge) SystemPrompt() string { return "" }
func (T Forge) Desc() string {
	return "Apps: Self-improvement — run claude-code in gohort's source tree, then rebuild + restart."
}

func (T *Forge) Init() error { return T.Flags.Parse() }

func (T *Forge) Main() error {
	// Dashboard-only app; no CLI surface.
	Log("forge is a web-only app. Start with:\n  gohort serve :8181")
	return nil
}

// --- core.WebApp interface ---------------------------------------------------

func (T *Forge) WebPath() string { return "/forge" }
func (T *Forge) WebName() string { return "Forge" }
func (T *Forge) WebDesc() string {
	return "Run claude-code in gohort's own source, then rebuild + restart."
}

func (T *Forge) Routes() {
	T.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		T.handlePage(w, r)
	})
	T.HandleFunc("/api/status", T.handleStatus)
	T.HandleFunc("/api/config", T.handleConfig)
	T.HandleFunc("/api/terminal", T.handleTerminal)
	T.HandleFunc("/api/rebuild", T.handleRebuild)
	T.HandleFunc("/api/session/new", T.handleNewSession)
}

// pageBody assembles the ui.Page. The terminal itself is mounted by the
// JS in web_assets (there is no core/ui terminal primitive); the page
// provides a status/actions panel and a collapsed config form, and the
// script appends the xterm host into the sections container.
func (T *Forge) handlePage(w http.ResponseWriter, r *http.Request) {
	if _, ok := T.requireAdmin(w, r); !ok {
		return
	}
	cfg := T.loadConfig()
	// The page is terminal-first: the only ui.Section is the settings form,
	// which web_assets.go's script folds into a drawer behind the ⚙ button in
	// the terminal bar. Live status (session + working dir) and the action
	// buttons also live in that bar, so the visible page is mostly the
	// terminal itself.
	page := ui.Page{
		Title:         "Forge",
		ShowTitle:     true,
		BackURL:       "/",
		MaxWidth:      "100%",
		ExtraHeadHTML: forgeHeadHTML,
		Sections: []ui.Section{
			{
				Title:    "Settings",
				Subtitle: "Environment-specific knobs. The restart command is empty (disabled) until you set it.",
				Body: ui.FormPanel{
					Source:  "api/config",
					PostURL: "api/config",
					Fields: []ui.FormField{
						{Field: "work_dir", Label: "Working directory", Type: "text", Placeholder: cfg.resolvedWorkDir(), Help: "Where claude runs and the build happens. Blank = server working directory."},
						{Field: "claude_cmd", Label: "Launch command", Type: "text", Placeholder: "claude", Help: "Command started in the tmux session. If it exits, the session drops to a shell so it stays alive."},
						{Field: "rebuild_cmd", Label: "Build gate command", Type: "text", Placeholder: "go install .", Help: "Must exit 0 before a restart is allowed. `go install .` compiles + installs the binary to GOBIN. Never bypassed."},
						{Field: "restart_cmd", Label: "Restart command", Type: "text", Placeholder: "(disabled)", Help: "How to bounce the server, e.g. cp /opt/bin/gohort /opt/gohort/ && sudo systemctl restart gohort. Blank disables restart."},
						{Field: "tmux_session", Label: "tmux session name", Type: "text", Placeholder: "forge"},
					},
				},
			},
		},
	}
	page.ServeHTTP(w, r)
}
