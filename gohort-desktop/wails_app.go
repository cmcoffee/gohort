// Wails-bound App. Exposes a SMALL, STABLE surface to the webview:
//
//   window.go.main.App.InvokeTool(name, args)  → result string
//   window.go.main.App.ListTools()             → [{name, desc, params, required}]
//   window.go.main.App.IsConfigured()          → bool
//   window.go.main.App.GetSettings()           → {server_url}
//   window.go.main.App.SaveSettings(url)       → {ok, error?}
//   window.go.main.App.ResetSettings()         → {ok, error?}
//
// Tool-specific methods (ReadLocalFile, OpenApp, etc.) are NOT
// individually bound — that would mean every new tool requires a new
// Wails-side method + JS-side wrapper. Instead, the registry is the
// single source of truth: tools register in their own packages, the
// desktop exposes one generic invoke shim, and the page-side JS (or
// the WebSocket protocol for remote gohort, Phase 2) drives off the
// catalog.
//
// Settings methods are bound here because they're administrative
// (configure / reset the desktop itself), distinct from tool calls.
// Auth credentials are intentionally NOT in this surface — cookie
// login is delegated to gohort's own /login page inside the webview.
//
// PascalCase forced by Wails for any method JS-callable.

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
	wails_runtime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the Wails app object. Keeps the lifecycle ctx, config
// reference, the cookie jar, and the tool-invocation approval
// store (consent gate for the WS bridge — see approvals.go).
type App struct {
	ctx       context.Context
	config    *core.Config
	cookies   *core.PersistentCookieJar
	approvals *approvalStore
}

// NewApp constructs the App with the loaded config + cookie jar.
// main.go calls this once and passes the result to wails.Run via
// Bind.
func NewApp(cfg *core.Config, cookies *core.PersistentCookieJar) *App {
	return &App{
		config:    cfg,
		cookies:   cookies,
		approvals: newApprovalStore(cfg.Settings()),
	}
}

// startup runs once when the Wails runtime is ready. ctx is needed
// for Wails runtime APIs (notifications, dialog, clipboard) that
// arrive in later tool packages.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	core.Log("[gohort-desktop] startup — %d local tool(s) registered", len(core.RegisteredTools()))
	for _, t := range core.RegisteredTools() {
		core.Debug("[gohort-desktop]   • %s", t.Name())
	}
	if url := a.config.ServerURL(); url != "" {
		core.Log("[gohort-desktop] server URL: %s", url)
	} else {
		core.Log("[gohort-desktop] no server URL set — first-run configure page will render")
	}
}

// shutdown is called when the user quits. Closes the settings DB so
// kvlite flushes cleanly; future tool packages may add their own
// teardown here.
func (a *App) shutdown(ctx context.Context) {
	if err := a.config.Close(); err != nil {
		core.Warn("[gohort-desktop] settings close failed: %v", err)
	}
	core.Log("[gohort-desktop] shutdown")
}

// tool_descriptor is the JS-facing shape returned by ListTools.
// Mirrors core.Tool but flattened for JSON serialization (the
// interface doesn't survive Wails' JS bridge marshaling on its own).
type tool_descriptor struct {
	Name     string                    `json:"name"`
	Desc     string                    `json:"desc"`
	Params   map[string]core.ToolParam `json:"params"`
	Required []string                  `json:"required"`
}

// ListTools returns the current registered + enabled tool catalog
// to the webview. Exposed to JS as window.go.main.App.ListTools().
func (a *App) ListTools() []tool_descriptor {
	tools := core.RegisteredTools()
	out := make([]tool_descriptor, 0, len(tools))
	for _, t := range tools {
		out = append(out, tool_descriptor{
			Name:     t.Name(),
			Desc:     t.Desc(),
			Params:   t.Params(),
			Required: t.Required(),
		})
	}
	return out
}

// InvokeTool runs a tool from the registry by name with the supplied
// args. Exposed to JS as window.go.main.App.InvokeTool(name, args).
//
// Returns (result, nil) on success; errors propagate to JS as a
// rejected promise. ALL Phase-1 tool execution from JS goes through
// this one entrypoint — adding a new tool requires no Wails changes.
//
// The Phase-2 WebSocket dispatcher uses the same core.InvokeTool
// path, so JS-direct calls and server-driven calls share execution.
func (a *App) InvokeTool(name string, args map[string]any) (string, error) {
	return core.InvokeTool(name, args)
}

// IsConfigured tells the configure page whether the desktop already
// has a server URL. Used by the page-load JS to short-circuit
// (immediately navigate to "/") if the user reached it accidentally
// after configuration was already done.
func (a *App) IsConfigured() bool { return a.config.IsConfigured() }

// settings_view is the JS-visible settings shape. Excludes anything
// secret on purpose — there is nothing secret at this layer (auth is
// cookie-based in the webview), and keeping the shape lean means the
// future settings page can grow naturally.
type settings_view struct {
	ServerURL string `json:"server_url"`
}

// GetSettings returns the current visible settings to the webview.
func (a *App) GetSettings() settings_view {
	return settings_view{ServerURL: a.config.ServerURL()}
}

// save_result is the uniform return shape for SaveSettings /
// ResetSettings. Carrying an error string lets the form surface a
// human-readable message inline instead of a generic promise
// rejection (which Wails renders as just "error").
type save_result struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// SaveSettings validates + persists a new server URL. Probes the URL
// to confirm reachability before saving — a saved-but-unreachable URL
// puts the user in a frustrating loop, easier to catch up front.
func (a *App) SaveSettings(server_url string) save_result {
	server_url = strings.TrimSpace(server_url)
	if server_url == "" {
		return save_result{Error: "Enter a server URL."}
	}
	parsed, err := url.Parse(server_url)
	if err != nil {
		return save_result{Error: fmt.Sprintf("Invalid URL: %v", err)}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return save_result{Error: "URL must start with http:// or https://"}
	}
	if parsed.Host == "" {
		return save_result{Error: "URL is missing a host."}
	}
	if err := probe_gohort(parsed.String()); err != nil {
		return save_result{Error: fmt.Sprintf("Couldn't reach %s — %v", parsed.Host, err)}
	}
	// Server change → clear cookies. Old session cookie was for a
	// different host (or the same host but stale), and the new server
	// will issue a fresh one on login.
	if previous := a.config.ServerURL(); previous != server_url && a.cookies != nil {
		a.cookies.Clear()
	}
	if err := a.config.SetServerURL(server_url); err != nil {
		return save_result{Error: fmt.Sprintf("Save failed: %v", err)}
	}
	core.Log("[gohort-desktop] saved server URL: %s", server_url)
	return save_result{OK: true}
}

// ResetSettings clears the persisted URL AND the cookie jar so the
// next request hits the configure form with no leftover session
// state. JS reloads after calling. Used by the "Change server"
// button on the unreachable-server error page and by the Mac menu's
// Change Server… item.
func (a *App) ResetSettings() save_result {
	if a.cookies != nil {
		a.cookies.Clear()
	}
	if err := a.config.ClearServerURL(); err != nil {
		return save_result{Error: err.Error()}
	}
	core.Log("[gohort-desktop] settings + cookies cleared — first-run configure will render")
	return save_result{OK: true}
}

// GetLogs returns the full in-app log ring buffer for the Show
// Logs viewer. Caller renders the array in a modal; the buffer is
// bounded at logBufferCap entries so the response stays small.
func (a *App) GetLogs() []LogLine {
	return log_buffer.Lines()
}

// LogOut wipes the local cookie jar so the next upstream request has
// no session cookie. Caller (menu handler) follows up with a JS
// navigation to /login. Doesn't touch the server URL — only the
// session — so the user lands back on the same gohort's login page,
// not the configure form.
func (a *App) LogOut() save_result {
	if a.cookies != nil {
		a.cookies.Clear()
	}
	core.Log("[gohort-desktop] logged out — cookie jar cleared")
	return save_result{OK: true}
}

// --- bridge approval surface (see approvals.go + ws_client.go) ---

// ApproveInvoke resolves a pending tool-invocation approval. The
// modal in the page calls this with the request ID + the user's
// verdict (true=Allow, false=Deny). No-op on unknown / already-
// resolved IDs.
func (a *App) ApproveInvoke(id string, allow bool) {
	if a.approvals != nil {
		a.approvals.Resolve(id, allow)
	}
}

// GetAutoApprove returns the persisted "skip the prompt and
// auto-approve every tool call" toggle. Page reads this to render
// the settings checkbox.
func (a *App) GetAutoApprove() bool {
	if a.approvals == nil {
		return false
	}
	return a.approvals.AutoApprove()
}

// SetAutoApprove persists the toggle. true = bypass the modal and
// allow every tool call silently; false = ask each time. Default
// is false (ask) — opt-in trust.
func (a *App) SetAutoApprove(on bool) save_result {
	if a.approvals == nil {
		return save_result{Error: "approval store not ready"}
	}
	if err := a.approvals.SetAutoApprove(on); err != nil {
		return save_result{Error: err.Error()}
	}
	return save_result{OK: true}
}

// --- filesystem read-allowlist surface ---
//
// Lets the operator manage which folders the desktop's filesystem.*
// tools may resolve paths under. The menu (Account → Add / Show
// Allowed Folders) and the in-page modal both call these methods.

// GetReadRoots returns the currently allowed root folders.
func (a *App) GetReadRoots() []string {
	return core.AllowedReadRoots()
}

// AddReadRoot adds path to the allowlist (normalized: absolute,
// symlinks resolved, deduped, sorted). Caller-facing — the menu's
// native folder picker and the in-page modal both route through this.
func (a *App) AddReadRoot(path string) save_result {
	if _, err := core.AddAllowedReadRoot(path); err != nil {
		return save_result{Error: err.Error()}
	}
	core.Log("[gohort-desktop] read-allowlist: added %s", path)
	return save_result{OK: true}
}

// RemoveReadRoot drops path from the allowlist. No-op if not present.
func (a *App) RemoveReadRoot(path string) save_result {
	if _, err := core.RemoveAllowedReadRoot(path); err != nil {
		return save_result{Error: err.Error()}
	}
	core.Log("[gohort-desktop] read-allowlist: removed %s", path)
	return save_result{OK: true}
}

// PickReadRoot opens the OS-native folder picker and, on selection,
// adds the chosen folder to the allowlist. Returns the chosen path
// (or empty string when the user canceled) so the caller can refresh
// its display. Used by the menu — the in-page modal can keep using
// AddReadRoot with a typed-in path.
func (a *App) PickReadRoot() pick_result {
	if a.ctx == nil {
		return pick_result{Error: "desktop not ready"}
	}
	chosen, err := wails_runtime.OpenDirectoryDialog(a.ctx, wails_runtime.OpenDialogOptions{
		Title: "Allow gohort to read from this folder",
	})
	if err != nil {
		return pick_result{Error: err.Error()}
	}
	if chosen == "" {
		// User canceled — surface as OK with empty path so the JS
		// can quietly do nothing instead of showing an error toast.
		return pick_result{OK: true}
	}
	if _, err := core.AddAllowedReadRoot(chosen); err != nil {
		return pick_result{Error: err.Error()}
	}
	core.Log("[gohort-desktop] read-allowlist: added %s (via picker)", chosen)
	return pick_result{OK: true, Path: chosen}
}

// pick_result mirrors save_result with the chosen path attached.
// Folder-picker callers need to know WHICH path got added so they
// can update their UI without re-querying the whole list.
type pick_result struct {
	OK    bool   `json:"ok"`
	Path  string `json:"path,omitempty"`
	Error string `json:"error,omitempty"`
}

// RequestApprovalBlocking is the non-Wails entry point ws_client.go
// uses on each incoming tool invocation. Wraps the approvalStore.
// Request with the in-page delivery callback (Wails EventsEmit) so
// the modal sees a fresh ApprovalRequest event.
func (a *App) RequestApprovalBlocking(id, name string, args map[string]any) bool {
	if a.approvals == nil {
		return false
	}
	return a.approvals.Request(
		ApprovalRequest{ID: id, Name: name, Args: args},
		func(req ApprovalRequest) {
			// EventsEmit fires the named event to every webview
			// listener. Page-side popup_shim_script catches it,
			// renders the approval modal, and calls back via
			// ApproveInvoke.
			if a.ctx != nil {
				wails_runtime.EventsEmit(a.ctx, "bridge-approval", req)
			}
		},
	)
}

// probe_gohort hits the URL's root with a short timeout. Any HTTP
// response (200, 302→/login, 401, 403, …) means gohort is answering;
// only network/transport errors count as unreachable.
func probe_gohort(target string) error {
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(target)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
