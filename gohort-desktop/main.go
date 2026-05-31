// gohort-desktop entry point. Minimal by design — config + Wails
// bootstrap only. Tool registration happens in each tool package's
// init() (pulled in via blank imports in tools.go). The Wails app
// (wails_app.go) exposes a single InvokeTool bridge that delegates
// to the registry, plus the settings methods used by the first-run
// configure page. Neither this file nor wails_app.go knows what
// tools exist.

package main

import (
	"embed"

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
	// Wrap every nfo output flag with the in-app ring buffer BEFORE
	// any code calls Log/Err/Warn — so the buffer captures startup
	// lines too. The viewer (Show Logs menu item) reads from this.
	installLogCapture()

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

	// Seed / load the shared filesystem read-allowlist. Filesystem
	// tools (read_local_file, list_directory) consult core.PathAllowed
	// for every operation — InitFSAllowlist makes that lookup safe to
	// call. On first run it writes the seed defaults so the operator
	// sees them immediately in "Show Allowed Folders".
	core.InitFSAllowlist(cfg.Settings())

	width, height := cfg.WindowSize()
	app := NewApp(cfg, cookies)

	// Bridge — opens a WebSocket to the gohort server and exposes
	// our local tools (filesystem.read_local_file, eventually
	// notify / screenshot / shell) so server-side agents can invoke
	// them. Runs in the background; failures retry with backoff and
	// never block startup. The app reference gives ws_client the
	// approval-store handle for per-invocation consent (see
	// approvals.go) — without app, every server-initiated tool call
	// would silently auto-execute, no user gate.
	stopWS := startWSClient(cfg, cookies, app)
	defer stopWS()

	err = wails.Run(&options.App{
		Title:     "Gohort",
		Width:     width,
		Height:    height,
		MinWidth:  core.MIN_WINDOW_WIDTH,
		MinHeight: core.MIN_WINDOW_HEIGHT,

		// AssetServer.Handler is the sole responder for every webview
		// request. Assets is intentionally NOT set — its FS-first
		// middleware would intercept "/" (matching index.html) before
		// the proxy could route to the configure page or upstream
		// gohort serve. Wails' native bindings (window.go.main.App.*)
		// are injected by WKWebView's message handler, independent of
		// the asset server, so dropping Assets doesn't affect them.
		AssetServer: &assetserver.Options{
			Handler: new_gohort_proxy(cfg, configure_html, cookies),
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
