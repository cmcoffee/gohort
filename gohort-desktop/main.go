// gohort-desktop entry point. One binary, two modes: with no flag it
// runs the Wails viewer window (runViewer below); with --bridge it
// runs the always-on daemon (menu-bar app + iMessage relay + WS tool
// bridge — see package bridge). Management flags (--setup/--install/
// --uninstall/--test) run a bridge action and exit. launchd runs us
// with --bridge; the tray's "Open Window" relaunches us with no flag.
//
// Tool registration happens in each tool package's init() (blank
// imports in tools.go). The Wails app (wails_app.go) exposes an
// InvokeTool bridge to the webview; the daemon serves the same
// registry to the server over the WS bridge.

package main

import (
	"embed"
	"fmt"
	"html"
	"net"
	"net/http"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

// frontend embeds the configure-page HTML. It is NOT served via
// Wails' AssetServer static-file middleware (that wraps Handler with
// FS-first routing, which would intercept "/" before our proxy could
// see it). Instead we read configure.html into memory at startup and
// the proxy serves it directly when no server URL is set.
//
//go:embed all:frontend
var assets embed.FS

func main() {
	// The viewer is a plain Wails window app. The bridge (iMessage relay
	// + WS tool bridge) is a SEPARATE app/process (Gohort-Bridge.app);
	// this binary just renders the gohort web UI and writes the bridge's
	// config to the shared sidecar when the user saves settings.
	runViewer()
}

// runViewer launches the Gohort app window.
func runViewer() {
	// Wrap every nfo output flag with the in-app ring buffer BEFORE
	// any code calls Log/Err/Warn — so the buffer captures startup
	// lines too. The viewer (Show Logs menu item) reads from this.
	installLogCapture()

	// The viewer keeps its own store (cookies, window state, server URL
	// for the proxy). The bridge agent is a different process with its
	// own config sidecar, so there's no kvlite lock contention here.
	cfg, err := core.LoadConfig()
	if err != nil {
		core.Fatal("gohort-desktop: load config: %v", err)
	}

	configure_html, err := assets.ReadFile("frontend/configure.html")
	if err != nil {
		core.Fatal("gohort-desktop: missing embedded configure.html: %v", err)
	}

	cookies, err := core.NewPersistentCookieJar(cfg.Settings())
	if err != nil {
		core.Fatal("gohort-desktop: init cookie jar: %v", err)
	}

	// The viewer registers NO filesystem tools and has no file-read
	// capability — all local tools (filesystem, contacts, iMessage) live
	// in the separate Gohort-Bridge agent, which gates folder reads
	// behind per-folder consent (see bridge/fsconsent.go).

	width, height := cfg.WindowSize()
	app := NewApp(cfg, cookies)

	// Local HTTP listener on 127.0.0.1:<random>. WKWebView refuses to
	// upgrade WebSocket connections from its custom-scheme handler
	// (wails://wails.localhost/...), which broke xterm panels and any
	// other WS-dependent feature when running through the desktop app.
	// Solution: serve the proxy through a real net/http server on
	// loopback, then redirect the initial wails:// navigation to that
	// http://127.0.0.1:<port>/ origin. WKWebView treats loopback HTTP
	// as a normal origin and upgrades WS just fine. The AssetServer's
	// HTML-redirect handler still gets the first hit; everything from
	// that point on is direct HTTP to the local listener.
	//
	// httputil.NewSingleHostReverseProxy (in proxy.go) already
	// supports WebSocket upgrades transparently when reached through a
	// real net/http server — it detects the Upgrade header and
	// switches to a tunnel mode. So no proxy-side change is needed
	// for WS to work; only the transport in front of it needs to be
	// real HTTP rather than the custom URL scheme.
	proxyHandler := new_gohort_proxy(cfg, configure_html, cookies)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		core.Fatal("gohort-desktop: local listener: %v", err)
	}
	localPort := listener.Addr().(*net.TCPAddr).Port
	localBase := fmt.Sprintf("http://127.0.0.1:%d", localPort)
	httpServer := &http.Server{Handler: proxyHandler}
	go func() {
		core.Log("[gohort-desktop] local proxy listening at %s", localBase)
		if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			core.Warn("[gohort-desktop] local listener stopped: %v", err)
		}
	}()
	defer httpServer.Close()

	err = wails.Run(&options.App{
		Title:     "Gohort",
		Width:     width,
		Height:    height,
		MinWidth:  core.MIN_WINDOW_WIDTH,
		MinHeight: core.MIN_WINDOW_HEIGHT,

		// AssetServer.Handler runs only for wails://wails.localhost/*
		// requests — that's just the initial webview load. Its sole
		// job is to bounce the navigation to the local HTTP listener
		// above; once redirected, the webview is on http://127.0.0.1
		// and every subsequent request (including WebSocket upgrades)
		// flows through real HTTP. Wails' window.go.main.App.*
		// bindings are injected independent of origin so they keep
		// working at the loopback URL.
		AssetServer: &assetserver.Options{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				target := localBase + r.URL.RequestURI()
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store")
				fmt.Fprintf(w,
					`<!DOCTYPE html><html><head><meta charset="utf-8">`+
						`<meta http-equiv="refresh" content="0;url=%s">`+
						`<title>Loading…</title></head>`+
						`<body><script>location.replace(%q);</script></body></html>`,
					html.EscapeString(target), target)
			}),
		},

		// Bind the App struct so its exported methods (ListTools,
		// InvokeTool, GetSettings, SaveSettings, ResetSettings) are
		// JS-callable as window.go.main.App.*.
		Bind: []interface{}{
			app,
		},
		Menu:       build_app_menu(app),
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,

		Mac: &mac.Options{
			// Solid (opaque) title bar that pushes content DOWN, so the
			// traffic-light buttons sit in their own strip and gohort's
			// own UI starts cleanly below them. Earlier setup used
			// TitlebarAppearsTransparent + FullSizeContent which let the
			// webview render under the buttons — works for apps that
			// design around it, but gohort's web UI doesn't reserve
			// space for native chrome, so the close/maximize buttons
			// were overlapping its top toolbar.
			TitleBar: &mac.TitleBar{
				TitlebarAppearsTransparent: false,
				HideTitle:                  false,
				HideTitleBar:               false,
				FullSizeContent:            false,
				UseToolbar:                 false,
			},
			Appearance:           mac.NSAppearanceNameDarkAqua,
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
		},
	})
	if err != nil {
		core.Fatal("gohort-desktop: wails run error: %v", err)
	}
}
