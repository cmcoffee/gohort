# gohort-desktop

Native desktop host for gohort + the local-capability tool bridge.

Shipped as **two separate apps** (one Go module, two `package main` build
targets):

- **Gohort.app** — the viewer. A Wails window that reverse-proxies the
  gohort web UI (orchestrate / Agency / etc.). Dock icon, no special
  permissions, quittable. Knows nothing about tools or iMessage.
- **Gohort-Bridge.app** — the bridge. An always-on **menu-bar** daemon
  (`LSUIElement`, no dock icon) that owns every OS-level permission and
  capability. It hosts the WebSocket tool bridge to the gohort server, the
  local tool catalog (filesystem, contacts), an MCP host, and —
  on macOS — the iMessage relay. Launches the viewer via its tray menu.

The split exists because Wails and the system-tray library can't share one
process (`fyne.io/systray` claims the `NSApplication` delegate, which Wails
needs). Two bundles with distinct bundle IDs also dodge the single-instance
/ dock conflicts of a one-bundle, two-mode design.

```
launchd / Run-key ── Gohort-Bridge.app (cmd/gohort-bridge; always on)
                       ├─ menu-bar icon (fyne.io/systray)
                       ├─ WS tool bridge → gohort  /api/desktop/ws   (X-API-Key)
                       ├─ local tools: filesystem.* , contacts.lookup / contacts.search
                       ├─ MCP host (configured stdio MCP servers → tools)
                       ├─ declared-command host (fixed commands → tools)
                       ├─ server-push install: server sends a capability (a
                       │   desktop_mcp / desktop_command connector, admin-approved
                       │   + user-consented) → new local tool, no reship
                       ├─ iMessage relay (macOS): chat.db → /bridges/api/hook,
                       │                          outbox poll → /bridges/api/poll
                       ├─ owns Full Disk Access, Contacts, folder consent
                       └─ "Open Gohort" → launches Gohort.app

user / tray opens ──── Gohort.app (Wails viewer; quittable, no perms)
                       ├─ reverse proxy + webview + gohort login
                       └─ reads server URL + API key from the shared sidecar
```

## Unified auth + config

- **One API key** authenticates **both** `/bridges/api/*` and the tool
  bridge `/api/desktop/ws` (server side: `core.RegisterAPIKeyValidator` +
  the bridge key's `Owner`). Set it once in the viewer.
- **Config is a lock-free JSON sidecar** — `core.BridgeConfig`
  (`server_url`, `api_key`, allowed read/write roots, …) at
  `<app-support>/gohort-desktop/bridge-config.json` (0600). The viewer
  writes it; the bridge re-reads it live, so edits apply without a restart.
  No shared kvlite lock between the two processes.
- The viewer keeps its own `viewer.db` (cookies, window size); it falls
  back to the sidecar `server_url` when the daemon holds `settings.db`.

## Repository layout

```
gohort-desktop/
├── core/                 # config, settings, sidecar, fs read/write allowlists, log aliases
├── tools/filesystem/     # filesystem.* tools (read_file, list_dir, query suite, write_file)
├── macos/                # darwin-only capabilities (//go:build darwin)
│   ├── imsg/             # iMessage relay: chat.db watch + AppleScript send
│   └── contacts/         # AddressBook lookup → contacts.lookup core.Tool
├── mcp/                  # MCP host: stdio JSON-RPC client, adapts MCP tools → core.Tool; runtime Install/Remove + mcp.json persistence
├── command/              # declared-command host: fixed command → core.Tool (no shell); Install/Remove + commands.json persistence
├── wsbridge/             # WebSocket client to gohort (X-API-Key, Approver + Installer ifaces); announce, invoke, install frames
├── bridge/               # the daemon: systray, services, consent, per-OS install
├── cmd/gohort-bridge/    # bridge binary entry point (cgo + systray, NO Wails)
├── frontend/             # Wails viewer assets
├── proxy.go              # viewer reverse proxy + popup shims (logs, approval, settings)
├── menu.go               # viewer native menu
├── wails_app.go          # viewer App bindings
├── main.go               # viewer entry (runViewer)
├── Makefile              # build (viewer) / bridge / install-all
└── go.mod
```

## Build & install

```sh
make build          # Gohort.app           (wails build)
make bridge         # Gohort-Bridge.app    (go build cmd/gohort-bridge + LSUIElement bundle)
make install-all    # install both + register the bridge as a login item
```

The bridge is cgo-on and platform-specific (systray Cocoa on macOS); the
viewer builds for macOS and Windows (WebView2). The macOS-only packages
(`macos/imsg`, `macos/contacts`) are `//go:build darwin` and excluded from
the Windows build; Windows gets the tray + WS bridge + filesystem tools
with an HKCU Run-key login item, macOS adds iMessage via launchd.

## First-run

1. Launch **Gohort.app**, enter the gohort server URL + the bridge API key
   in the configure/settings page (written to the sidecar).
2. Install the bridge from the viewer's **Account → Install Gohort-Bridge…**
   (or `make bridge-install`); grant **Full Disk Access** to Gohort-Bridge
   under System Settings → Privacy & Security for iMessage.
3. The bridge connects, announces its tools, and starts the relay. It
   restarts at login and on crash, but a deliberate tray **Quit** stays quit.

## Tools

Local capabilities register against the shared `core.Tool` registry and are
announced to the server over the WS bridge, where they're dispatched through
the agent's allowlist like any server-side tool. Filesystem reads/writes go
through per-folder consent (read and write are separate, stronger grants);
a tool that returns a `data:image/...;base64,...` URI (e.g. MCP image
tools) is delivered to the LLM as a vision attachment automatically.

The registry has two tiers. **Native** tools (`filesystem.*`, `contacts.*`)
register at `init()` and are permanent. **Dynamic** tools register at runtime
via `core.ReplaceDynamicTools(source, tools)`, keyed by source (an MCP server,
a declared command), and can be swapped or dropped wholesale. A native tool
always wins a name clash. When the dynamic set changes the registry fires a
change hook, so the WS bridge **re-announces** the fresh catalog without a
reconnect. This is what makes the tool surface expandable at runtime.

**Server-push install.** The server can send an `install` frame (a
`desktop_mcp` or `desktop_command` connector, once admin-approved on the
server) carrying an MCP server spec or a declared-command spec. Applying it is
gated by the same user-consent prompt as a tool call (`install_capability:*`),
so the machine's owner authorizes running new local code; a nil `Installer`
turns server-push off entirely. Accepted installs persist to `mcp.json` /
`commands.json`, so they survive a daemon restart, and the resulting tools ride
the dynamic-registry path above. Removes apply directly (tearing down is safe).
Declared commands run via `exec` with no shell involved: `{placeholder}` tokens
only fill argument values, so there is no shell-injection surface.

## Menu (viewer, "Account")

| Item | Effect |
|---|---|
| Server & Bridge Settings… (⌘,) | Server URL + bridge config |
| Set Bridge API Key… | Update only the API key (sidecar) |
| Auto-approve tool calls | Toggle; off = each server-initiated tool prompts for approval |
| Install / Remove Gohort-Bridge | Install the bridge as a login item, or remove it |
| Show Logs (⌘⌥L) | In-app log viewer (in-process ring buffer) |
| Reload (⌘R) | Reload the current proxied page |
| Log Out (⌘⇧L) | Clear the local cookie jar + hit gohort `/logout` |

> Menu callbacks drive the webview via `runtime.WindowExecJS` — proxy-served
> pages don't carry the Wails JS runtime (`window.go` / `window.runtime`),
> so anything that reaches the page from Go must execute JS, not rely on
> bindings. Page-side config posts to Go-handled `/__desktop/*` endpoints.

## Security notes

- **Only Gohort-Bridge.app holds OS permissions** (Full Disk Access,
  Contacts, file access). The viewer needs none.
- **API-key auth**, loopback-scoped: the desktop proxy strips
  `X-Forwarded-For` so the gohort server never sees the request as
  loopback (which would bypass its auth).
- **Folder consent** is just-in-time and per-folder; write consent is a
  separate, stronger grant than read. Approvals are remembered in the
  sidecar.
- Tools execute with the bridge binary's privileges; the allowlists are
  scope guards against the LLM, not a sandbox. OS-level boundaries remain
  the real perimeter.
