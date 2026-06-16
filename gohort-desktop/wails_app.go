// Wails-bound App. Exposes a SMALL, STABLE surface to the webview:
//
//   window.go.main.App.InvokeTool(name, args)  → result string
//   window.go.main.App.ListTools()             → [{name, desc, params, required}]
//   window.go.main.App.IsConfigured()          → bool
//   window.go.main.App.GetSettings()           → {server_url, api_key_set}
//   window.go.main.App.SaveSettings(url, key)  → {ok, error?}
//   window.go.main.App.ResetSettings()         → {ok, error?}
//   window.go.main.App.InstallBridge()         → {ok, error?}   (execs Gohort-Bridge --install)
//   window.go.main.App.UninstallBridge()       → {ok, error?}
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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
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
	// Auto-negotiate the daemon's bridge key from the viewer's session.
	a.startBridgeKeyProvisioning()
}

// provisionBridgeKey mints (or fetches) this user's bridge key from the
// server using the viewer's logged-in session cookie, and writes it to the
// sidecar the daemon reads. This is what auto-negotiates the key — no
// manual "Set Bridge API Key" step. One attempt; returns an error so the
// caller can retry (first run has no session until the user logs in
// through the webview).
func (a *App) provisionBridgeKey() error {
	base := strings.TrimRight(a.config.ServerURL(), "/")
	if base == "" {
		return fmt.Errorf("no server URL configured")
	}
	req, err := http.NewRequest(http.MethodGet, base+"/api/desktop/key", nil)
	if err != nil {
		return err
	}
	// Attach the session cookies for the server origin — the webview login
	// lives in this jar, so the request authenticates as the logged-in user.
	if a.cookies != nil {
		if u, e := url.Parse(base); e == nil {
			for _, c := range a.cookies.Cookies(u) {
				req.AddCookie(c)
			}
		}
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %d (not logged in yet?)", resp.StatusCode)
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if strings.TrimSpace(out.Key) == "" {
		return fmt.Errorf("server returned an empty key")
	}
	if err := core.WriteAPIKeySidecar(out.Key); err != nil {
		return err
	}
	core.Log("[gohort-desktop] auto-provisioned bridge key → sidecar (the daemon picks it up)")
	return nil
}

// startBridgeKeyProvisioning keeps the bridge-key sidecar current in the
// background. It provisions on startup (retrying fast until the user is
// logged in through the webview), then REFRESHES periodically — so if the
// server's key store ever changes (rotation, a data reset), the sidecar
// re-syncs within the refresh window instead of going stale until the next
// app restart. provisionBridgeKey's write is idempotent, so re-running is
// cheap and harmless.
func (a *App) startBridgeKeyProvisioning() {
	go func() {
		for {
			wait := 15 * time.Minute // refresh cadence once provisioned
			if err := a.provisionBridgeKey(); err != nil {
				// Surface WHY so a stale sidecar can be traced to "not
				// logged in (401)" vs "no server URL" vs a transport error,
				// rather than failing silently.
				core.Log("[gohort-desktop] bridge-key provisioning failed: %v (retrying)", err)
				wait = 20 * time.Second
			}
			time.Sleep(wait)
		}
	}()
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
	APIKeySet bool   `json:"api_key_set"` // true if a bridge API key is stored (the key itself is never sent to JS)
}

// GetSettings returns the current visible settings to the webview. The
// api_key_set flag reflects the bridge's effective key (core.BridgeAPIKey:
// auto-provisioned sidecar first, then the manual bridge config), not the
// viewer's own store.
func (a *App) GetSettings() settings_view {
	return settings_view{
		ServerURL: a.config.ServerURL(),
		APIKeySet: core.BridgeAPIKey() != "",
	}
}

// save_result is the uniform return shape for SaveSettings /
// ResetSettings. Carrying an error string lets the form surface a
// human-readable message inline instead of a generic promise
// rejection (which Wails renders as just "error").
type save_result struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// SaveSettings validates + persists a new server URL and, optionally,
// the bridge API key. Probes the URL to confirm reachability before
// saving — a saved-but-unreachable URL puts the user in a frustrating
// loop, easier to catch up front. An empty api_key leaves any stored
// key unchanged (users who don't run the background bridge never need
// to enter one).
func (a *App) SaveSettings(server_url, api_key string) save_result {
	server_url = strings.TrimSpace(server_url)
	if server_url == "" {
		return save_result{Error: "Enter a server URL."}
	}
	// Normalize to a BARE origin. The viewer's proxy and the bridge both
	// want the origin: the bridge appends /phantom/api/* and
	// /api/desktop/ws itself, so a pasted ".../phantom" would otherwise
	// become ".../phantom/phantom/api/hook" → 404.
	server_url = strings.TrimRight(server_url, "/")
	server_url = strings.TrimSuffix(server_url, "/phantom")
	server_url = strings.TrimRight(server_url, "/")
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
	// Only probe (and clear cookies) when the URL actually CHANGES.
	// Re-saving the same URL — e.g. just to update the bridge API key —
	// shouldn't re-probe a server that's already known-good, and must
	// not drop the session. This is what lets "update the API key" work
	// without going through the reachability check.
	changed := server_url != a.config.ServerURL()
	if changed {
		if err := probe_gohort(parsed.String()); err != nil {
			return save_result{Error: fmt.Sprintf("Couldn't reach %s — %v", parsed.Host, err)}
		}
		if a.cookies != nil {
			a.cookies.Clear() // new host → old session cookie is stale
		}
	}
	if err := a.config.SetServerURL(server_url); err != nil {
		return save_result{Error: fmt.Sprintf("Save failed: %v", err)}
	}
	// Write the bridge config sidecar so the separate Gohort-Bridge agent
	// picks up the server URL + API key. Preserve any poll interval set
	// via the agent's CLI.
	bc := core.ReadBridgeConfig()
	bc.ServerURL = server_url
	if key := strings.TrimSpace(api_key); key != "" {
		bc.APIKey = key
	}
	if err := core.WriteBridgeConfig(bc); err != nil {
		return save_result{Error: fmt.Sprintf("Saved URL, but writing the bridge config failed: %v", err)}
	}
	core.Log("[gohort-desktop] saved server URL: %s", server_url)
	return save_result{OK: true}
}

// SetBridgeAPIKey writes ONLY the bridge API key to the sidecar — no
// server URL, no reachability probe, no reload. This is the dedicated
// "just update the key" path; it can't get stuck on the Connect probe.
// Exposed to JS as window.go.main.App.SetBridgeAPIKey(key).
func (a *App) SetBridgeAPIKey(api_key string) save_result {
	key := strings.TrimSpace(api_key)
	if key == "" {
		return save_result{Error: "Enter an API key."}
	}
	bc := core.ReadBridgeConfig()
	bc.APIKey = key
	if err := core.WriteBridgeConfig(bc); err != nil {
		return save_result{Error: fmt.Sprintf("Save failed: %v", err)}
	}
	core.Log("[gohort-desktop] bridge API key updated")
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
// modal in the page calls this with the request ID, the user's
// verdict (true=Allow, false=Deny), and whether the choice should
// stick (always=true → "Always allow <tool>": the tool is added to
// the per-tool allow-list and won't prompt again). No-op on unknown
// / already-resolved IDs.
func (a *App) ApproveInvoke(id string, allow bool, always bool) {
	if a.approvals != nil {
		a.approvals.Resolve(id, allow, always)
	}
}

// GetAllowedTools returns the per-tool always-allow list, for the
// Preferences manager (Account → Manage Tool Approvals).
func (a *App) GetAllowedTools() []string {
	if a.approvals == nil {
		return nil
	}
	return a.approvals.AllowedTools()
}

// RemoveAllowedTool revokes a tool's always-allow grant — it will
// prompt again on its next invocation.
func (a *App) RemoveAllowedTool(name string) save_result {
	if a.approvals == nil {
		return save_result{Error: "approval store not ready"}
	}
	if err := a.approvals.RemoveAllowedTool(name); err != nil {
		return save_result{Error: err.Error()}
	}
	return save_result{OK: true}
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

// SaveAttachment is the desktop download path. The web UI delivers a file
// attachment as an <a download href="data:<mime>;base64,...">, but WKWebView
// (the macOS webview Wails uses) ignores the `download` attribute on data: URIs
// — so that link is a dead click in the desktop client even though it works in
// a normal browser. popup_shim.js intercepts the click and calls this instead,
// handing over the filename, mime type, and base64 bytes; we pop a native save
// dialog and write the decoded bytes to the chosen path. Returns OK with an
// empty path when the user cancels (the JS treats that as a quiet no-op).
// openURL opens an external link in the user's default browser. Called from the
// proxy's /__desktop/open handler: proxy-served pages can't reach the Wails
// Go-bridge (window.runtime is absent there), so popup_shim.js POSTs the URL and
// we open it from the Go side via the runtime. Only http/https is allowed, so a
// stray javascript:/file:/data: link can't be smuggled into the OS opener.
// Unexported on purpose — same package as the proxy, and keeping it off the
// Wails binding surface avoids a generated-bindings churn.
func (a *App) openURL(rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("only http(s) URLs can be opened externally")
	}
	wails_runtime.BrowserOpenURL(a.ctx, u.String())
	return nil
}

func (a *App) SaveAttachment(name, mimeType, b64 string) pick_result {
	_ = mimeType // reserved for a future extension-default; bytes write the same regardless
	if a.ctx == nil {
		return pick_result{Error: "desktop not ready"}
	}
	b64 = strings.TrimSpace(b64)
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		// Some senders use URL-safe base64 — try that before failing.
		if alt, aerr := base64.URLEncoding.DecodeString(b64); aerr == nil {
			raw = alt
		} else {
			return pick_result{Error: "decode attachment: " + err.Error()}
		}
	}
	dest, err := wails_runtime.SaveFileDialog(a.ctx, wails_runtime.SaveDialogOptions{
		DefaultFilename: name,
		Title:           "Save attachment",
	})
	if err != nil {
		return pick_result{Error: err.Error()}
	}
	if dest == "" {
		return pick_result{OK: true} // user canceled — quiet no-op
	}
	if err := os.WriteFile(dest, raw, 0o644); err != nil {
		return pick_result{Error: err.Error()}
	}
	core.Log("[gohort-desktop] saved attachment %q (%d bytes) to %s", name, len(raw), dest)
	return pick_result{OK: true, Path: dest}
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
