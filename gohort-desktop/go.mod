// gohort-desktop is the cross-platform desktop host for gohort web
// apps. Separate Go module so the desktop-only dependencies (Wails,
// webview shims) stay isolated from the main gohort binary build.
//
// Hosts gohort web surfaces (orchestrate chat today; phantom, admin,
// future apps later) inside a native window via Wails v2. Adds
// system-level capabilities the browser can't reach: filesystem
// reads, native notifications, screenshots, menu-bar status.
//
// Architecture:
//   - Wails v2 with embedded webview (WKWebView on macOS)
//   - AssetServer configured as a reverse proxy → http://localhost:8088
//     (the operator runs `gohort serve` separately; this is a thin
//     viewer + native bridge layer, NOT a server)
//   - Native bridges exposed to JS via Wails' Bind mechanism, namespaced
//     under window.go.main.App in the webview
//   - Logging via snugforge/nfo to match the rest of the gohort project
//
// snugforge is a sibling repo checked out at ../../snugforge; the
// replace directive points there. Same pattern main gohort uses
// (relative path to the sibling clone) so a single git pull in
// snugforge propagates to both binaries without version games.
module github.com/cmcoffee/gohort/gohort-desktop

go 1.24.0

require (
	github.com/cmcoffee/snugforge v0.0.0
	github.com/gorilla/websocket v1.5.3
	github.com/wailsapp/wails/v2 v2.9.2
)

require (
	github.com/atotto/clipboard v0.1.4 // indirect
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/bep/debounce v1.2.1 // indirect
	github.com/boltdb/bolt v1.3.1 // indirect
	github.com/charmbracelet/bubbles v0.21.0 // indirect
	github.com/charmbracelet/bubbletea v1.3.10 // indirect
	github.com/charmbracelet/colorprofile v0.2.3-0.20250311203215-f60798e515dc // indirect
	github.com/charmbracelet/lipgloss v1.1.0 // indirect
	github.com/charmbracelet/x/ansi v0.10.1 // indirect
	github.com/charmbracelet/x/cellbuf v0.0.13-0.20250311204145-2c3ea96c31dd // indirect
	github.com/charmbracelet/x/term v0.2.1 // indirect
	github.com/erikgeiser/coninput v0.0.0-20211004153227-1c3628e74d0f // indirect
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/jchv/go-winloader v0.0.0-20210711035445-715c2860da7e // indirect
	github.com/labstack/echo/v4 v4.10.2 // indirect
	github.com/labstack/gommon v0.4.0 // indirect
	github.com/leaanthony/go-ansi-parser v1.6.0 // indirect
	github.com/leaanthony/gosod v1.0.3 // indirect
	github.com/leaanthony/slicer v1.6.0 // indirect
	github.com/leaanthony/u v1.1.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.2.0 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-localereader v0.0.1 // indirect
	github.com/mattn/go-runewidth v0.0.16 // indirect
	github.com/muesli/ansi v0.0.0-20230316100256-276c6243b2f6 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/muesli/termenv v0.16.0 // indirect
	github.com/pkg/browser v0.0.0-20210911075715-681adbf594b8 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/samber/lo v1.38.1 // indirect
	github.com/tkrajina/go-reflector v0.5.6 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/valyala/fasttemplate v1.2.2 // indirect
	github.com/wailsapp/go-webview2 v1.0.16 // indirect
	github.com/wailsapp/mimetype v1.4.1 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	golang.org/x/crypto v0.23.0 // indirect
	golang.org/x/exp v0.0.0-20230522175609-2e198f4a06a1 // indirect
	golang.org/x/net v0.25.0 // indirect
	golang.org/x/sys v0.36.0 // indirect
	golang.org/x/term v0.20.0 // indirect
	golang.org/x/text v0.15.0 // indirect
)

replace github.com/cmcoffee/snugforge => ../../snugforge
