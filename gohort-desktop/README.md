# gohort-desktop

Native desktop host for gohort web apps + local-capability tool framework.

The desktop:
- Shows the gohort web UI (orchestrate / Agency / etc.) inside a native
  Wails window via reverse proxy.
- Exposes Mac OS-level capabilities (filesystem read, app launching,
  notifications, screenshots, etc.) as **tools** callable from the page
  via a small Wails bridge — and, in Phase 2, callable remotely from a
  gohort server over WebSocket.

Built on the same patterns as kitebroker / gohort:
self-registering modules, snake_case + UPPERCASE constants, snugforge
libraries (nfo for logging), clear separation between `core/`
infrastructure and `tools/` plugins.

## Repository layout

```
gohort-desktop/
├── core/                 # types, interfaces, registry, config, log aliases
│   ├── tool.go           # Tool interface + ToolParam / ToolHandler types
│   ├── registry.go       # RegisterTool, RegisteredTools, InvokeTool
│   ├── config.go         # Config struct + LoadConfig (kvlite-backed)
│   ├── settings.go       # Persisted settings (server URL, auto-approve, allowlist, cookies)
│   ├── fs_allowlist.go   # Shared read-allowlist consulted by every filesystem.* tool
│   └── log.go            # snugforge/nfo aliases (Log, Debug, Err, Fatal, …)
├── tools/                # self-registering tool packages (one per category)
│   └── filesystem/       # filesystem.read_local_file / list_directory /
│       │                 #   head_file / tail_file / read_file_range /
│       │                 #   grep_file / stat_file
│       ├── read_file.go
│       ├── list_dir.go
│       └── query.go      # head / tail / read_lines / grep / stat — same shape as core/ui workspace actions
├── frontend/             # Wails frontend assets
│   └── configure.html
├── tools.go              # blank-import loader (kitebroker pattern)
├── main.go               # entry point: load config + cookie jar + WS client + Wails
├── wails_app.go          # Wails-bound App: InvokeTool / ListTools / Settings / approvals / read-allowlist
├── proxy.go              # reverse proxy to gohort serve + popup_shim_script (approval modal, log viewer, allowed-folders manager)
├── ws_client.go          # WebSocket bridge to gohort server — announces tool catalog + handles invoke
├── approvals.go          # Per-invocation approval store + auto-approve toggle
├── log_buffer.go         # In-process ring buffer fed by nfo output
├── menu.go               # Native Mac menu (Account submenu)
├── wails.json            # Wails config
├── Makefile              # build / install / dev / clean / doctor / universal
└── go.mod
```

## How tools work

Every local capability is a package under `tools/`. Each tool:

1. Defines a struct implementing `core.Tool`.
2. Self-registers via an `init()` block:

   ```go
   func init() { core.RegisterTool(&my_tool{}) }
   ```

3. Gets pulled into the build via a blank import in `tools.go`:

   ```go
   import _ "github.com/cmcoffee/gohort/gohort-desktop/tools/mycategory"
   ```

That's it. No central switch statement, no per-tool wiring in main.

The `core.Tool` interface:

```go
type Tool interface {
    Name() string                          // e.g. "filesystem.read_local_file"
    Desc() string                          // LLM-facing
    Params() map[string]ToolParam          // JSON-schema-ish param shape
    Required() []string                    // required param names
    Handler() ToolHandler                  // execution
    Enabled() bool                         // skip if false
}
```

A registered + enabled tool is automatically:

- Listed by `core.RegisteredTools()` (sorted by name)
- Looked up by `core.FindTool(name)`
- Invocable via `core.InvokeTool(name, args)`
- Exposed to the webview's JS through `window.go.main.App.InvokeTool(name, args)`
- (Phase 2) Announced to a connected remote gohort server and dispatched
  via the same `core.InvokeTool` path

## Adding a new tool

```sh
# 1. Create the package
mkdir -p tools/notify
$EDITOR tools/notify/notify.go

# 2. Add a blank import to tools.go
$EDITOR tools.go

# 3. Done. Rebuild, the tool appears in window.go.main.App.ListTools().
make build
```

Skeleton for the new tool file:

```go
package notify

import "github.com/cmcoffee/gohort/gohort-desktop/core"

func init() { core.RegisterTool(&notify_tool{}) }

type notify_tool struct{}

func (t *notify_tool) Name() string { return "notify.show" }
func (t *notify_tool) Desc() string { return "Show a native macOS notification." }
func (t *notify_tool) Params() map[string]core.ToolParam {
    return map[string]core.ToolParam{
        "title":   {Type: "string", Description: "Notification title."},
        "message": {Type: "string", Description: "Notification body."},
    }
}
func (t *notify_tool) Required() []string { return []string{"title", "message"} }
func (t *notify_tool) Enabled() bool      { return true }
func (t *notify_tool) Handler() core.ToolHandler {
    return func(args map[string]any) (string, error) {
        // … run osascript or use a native lib …
        return "delivered", nil
    }
}
```

## Setup

### Prereqs

- Go 1.22+
- Wails v2 CLI: `go install github.com/wailsapp/wails/v2/cmd/wails@latest`
- Xcode CLI tools (system webview headers on macOS):
  `xcode-select --install`

Run `make doctor` after install to verify the toolchain.

### Dev loop

Terminal 1 (gohort server):

```sh
cd /path/to/gohort
go run . serve 127.0.0.1:8088
```

Terminal 2 (desktop dev mode with hot reload):

```sh
cd gohort-desktop
make dev
```

A window opens with the orchestrate chat. Edit any tool plugin
(`tools/filesystem/read_file.go`, future packages, etc.) and Wails
recompiles + reloads automatically.

Verify a tool from devtools (right-click → Inspect Element):

```js
// List the catalog
await window.go.main.App.ListTools()

// Invoke by name
await window.go.main.App.InvokeTool("filesystem.read_local_file", {path: "/var/log/system.log"})
```

### Release build

```sh
make build              # current architecture
make universal          # Intel + Apple Silicon
make install            # → /Applications/gohort-desktop.app
```

### First-run flow

On first launch, gohort-desktop shows a small configure form asking
for the gohort server URL (e.g. `http://192.168.1.50:8088` or
`https://gohort.example.com`). The URL is validated and probed before
saving; once saved, the webview reloads, navigates to
`/orchestrate/`, and lands on gohort's own `/login` page if no
session cookie exists. The `gohort_session` cookie persists in the
webview's cookie store so subsequent launches skip the login step.

To change servers later, the "Couldn't reach gohort" page (rendered
when the configured server stops responding) has a **Change server…**
button that wipes the saved URL and re-shows the configure form.

If the saved URL still responds but points at the wrong service
(e.g. a typo'd hostname behind a reverse proxy that returns 200 for
everything), the "Change server" button won't appear because the
proxy never sees an error. Three ways out:

- **Menu** — `gohort → Change Server…` (⌘,) is the everyday path.
- **Escape-hatch URL** — navigate to `/__desktop/configure`; always
  intercepted by the desktop regardless of state.
- **Log Out** — `gohort → Log Out` (⌘⇧L) wipes the session cookie
  jar and navigates to `/login` without touching the server URL.

### Menu

| Item | Shortcut | Effect |
|---|---|---|
| Account → Change Server… | ⌘, | Clears saved URL + cookie jar, returns to configure form |
| Account → Log Out | ⌘⇧L | Clears local cookie jar + hits gohort's `/logout` |
| Account → Auto-approve tool calls | — | Persisted toggle. When checked, server-initiated tool invocations skip the approval modal and run silently. Off by default — opt-in trust. |
| Account → Add Allowed Folder… | — | Native folder picker; the chosen path joins the shared read-allowlist that gates every `filesystem.*` tool. |
| Account → Show Allowed Folders | — | Modal listing the current allowlist with a remove button per row. |
| Account → Show Logs | ⌘⌥L | Modal that tails the in-app log buffer (everything `nfo` writes). |
| Account → Reload | ⌘R | Reloads the current page. |

### Cookies (why a proxy-side jar)

Wails' WKWebView on macOS reaches the AssetServer via a custom URL
scheme handler (`WKURLSchemeHandler`), which does **not** process
`Set-Cookie` response headers. If we relied on the webview to manage
cookies, login would set a session cookie that WKWebView immediately
discards, looping the user back to `/login`. The desktop maintains
its own cookie jar (persisted via kvlite next to the settings) and
injects `Cookie` headers into every upstream request. The jar is
cleared whenever the server URL changes, and by `Log Out`.

The settings file lives at:

| Platform | Path |
|---|---|
| macOS | `~/Library/Application Support/gohort-desktop/settings.db` |
| Linux | `~/.config/gohort-desktop/settings.db` |
| Windows | `%APPDATA%\gohort-desktop\settings.db` |

### Runtime overrides (dev)

| Env var | Effect |
|---|---|
| `GOHORT_DESKTOP_ADDR` | Override the saved server URL (e.g. `http://127.0.0.1:8088`). Useful for dev without overwriting the persisted production URL. |
| `GOHORT_DESKTOP_PATH` | Initial URL path inside the gohort server (default `/orchestrate/`) |
| `GOHORT_DESKTOP_WIDTH` | Window width (min 720, default 1280) |
| `GOHORT_DESKTOP_HEIGHT` | Window height (min 480, default 800) |

## Phase 1 — local scaffold (shipped)

- Wails app with reverse proxy to gohort serve
- First-run configure form (server URL); persisted via snugforge/kvlite
- Cookie auth delegated to gohort's existing `/login` page; jar lives proxy-side because WKWebView's custom-scheme handler drops `Set-Cookie`
- "Couldn't reach gohort" error page with **Change server…** action
- kitebroker-style tool plugin pattern (`core/` + `tools/<category>/`); self-registering modules via init() + blank imports
- Single Wails bridge (`InvokeTool` + `ListTools`) — no per-tool Wails methods
- snugforge/nfo logging aliases in `core/`
- In-app log viewer + native Mac menu (Account submenu)

## Phase 2 — remote gohort connection (shipped)

- WebSocket client to remote gohort server, authenticated via the persisted cookie jar; reconnects with exponential backoff
- Announces the tool catalog on connect; server registers them as `from_client.<name>` tools and gates them through the agent's `AllowedTools` like any other tool
- Tool dispatch: server sends `{kind: "invoke", name, args, id}` over WS → desktop runs via `core.InvokeTool` → result returns over WS
- Per-invocation approval modal in the page: the user sees the tool name + args before each call. **Auto-approve** menu toggle bypasses the modal when checked (persisted; off by default)
- Operator-controlled read-allowlist for `filesystem.*` tools — native folder picker via menu, in-page management modal, persisted across launches
- Six filesystem tools shipped: `filesystem.read_local_file` (64 KB cap), `filesystem.list_directory`, `filesystem.head_file`, `filesystem.tail_file`, `filesystem.read_file_range`, `filesystem.grep_file`, `filesystem.stat_file` — only the small targeted query result crosses the WS bridge, not the full file

## Phase 3 — more native tools

Each is one new package under `tools/`:

- `apps/` — open application, list running apps
- `notify/` — native notifications
- `screenshot/` — full-screen / window / region capture
- `clipboard/` — get / set clipboard text
- `shell/` — controlled shell command exec (allowlisted commands)
- `keychain/` — read approved keychain entries

## Phase 4 — distribution

- Apple Developer ID signing + notarization
- Auto-update channel
- Windows / Linux universal builds
- Per-tool config UI in the desktop's settings panel

## Security notes

- **Auth model: cookie-delegate.** Auth credentials are never entered
  in the desktop UI; the configure form only collects the server URL.
  Login happens on gohort's own `/login` page rendered inside the
  webview, which sets the `gohort_session` cookie in the embedded
  cookie store. All of gohort's auth features (lockout, password
  reset, signup approval, session expiry) apply unchanged. Per-client
  tokens / OAuth-style approval are deferred to the Phase 2 work
  where they become load-bearing (WebSocket back-channel).
- The reverse proxy forwards every webview request to gohort serve.
  Trust profile = same as a browser pointed at the same gohort.
- Tools execute with the full privileges of the desktop binary.
  Allowlists (`filesystem.read_local_file`'s root list, future
  `apps.open`'s allowed-apps list, etc.) are soft policy guards against
  the LLM going out of scope — NOT defense against a malicious page.
  OS-level boundaries (file permissions, app sandbox) remain the real
  security perimeter.
- File reads are symlink-safe (real-path resolution before allowlist
  check). `filesystem.read_local_file` hard-caps at 64 KB; larger files
  return a directive error pointing the LLM at the targeted query
  tools (`head_file` / `tail_file` / `read_file_range` / `grep_file`)
  which run on the host and ship only the small slice back.
- Phase-2 WebSocket connections will use a pre-shared key for now;
  proper pairing / OAuth-like flow is a Phase-3+ concern.
