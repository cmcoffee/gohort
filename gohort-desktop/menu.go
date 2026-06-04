// Native menu — the discoverable surface for the two things the
// embedded webview can't easily expose:
//
//   Account > Change Server…   ⌘,    wipes session, returns to configure form
//   Account > Log Out          ⌘⇧L   wipes session, navigates to /login
//   Account > Reload           ⌘R    standard reload, here for keyboard parity
//
// Built on top of the OS-standard roles so the canonical Cocoa items
// (About / Hide / Quit in the app menu, Undo / Cut / Copy in Edit,
// Minimize / Zoom in Window) keep their native placement and
// keyboard shortcuts unchanged. The custom submenu is labeled
// "Account" rather than the app name — Cocoa's auto-generated app
// menu (next to the Apple menu) is already labeled "Gohort" from
// wails.json's productName, and a duplicate would read "Gohort Edit
// Gohort Window" in the menu bar.
//
// Menu callbacks run on the app's lifecycle goroutine. They drive
// the webview via runtime.WindowExecJS — JS-driven navigation works
// through WKWebView normally (unlike HTTP redirects, see proxy.go's
// rewrite_redirect_as_js for that story).

package main

import (
	"encoding/json"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
	wails_menu "github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	wails_runtime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// build_app_menu constructs the full menu tree. Returns the menu so
// main.go passes it into wails.Run's Menu option in one line.
func build_app_menu(app *App) *wails_menu.Menu {
	custom := wails_menu.NewMenu()

	// Non-destructive: open the same form with the current server URL
	// prefilled and the bridge API-key field — to set/update the key (or
	// the URL) WITHOUT clearing the session. This is where you configure
	// the API key the Gohort-Bridge agent uses.
	custom.AddText("Server & Bridge Settings…", keys.CmdOrCtrl(","), func(_ *wails_menu.CallbackData) {
		if app.ctx == nil {
			return
		}
		wails_runtime.WindowExecJS(app.ctx, `location.replace('/__desktop/settings');`)
		core.Log("[gohort-desktop] menu: Settings invoked")
	})

	// Dedicated, probe-free bridge API-key editor — separate from the
	// server-connection flow so it can never hang on "Checking server…".
	custom.AddText("Set Bridge API Key…", nil, func(_ *wails_menu.CallbackData) {
		if app.ctx == nil {
			return
		}
		wails_runtime.WindowExecJS(app.ctx, `location.replace('/__desktop/apikey');`)
		core.Log("[gohort-desktop] menu: Set Bridge API Key invoked")
	})

	// Destructive escape hatch: wipe URL + cookies and reconfigure from
	// scratch (for when the saved URL responds but is the wrong service).
	custom.AddText("Change Server (reset)…", nil, func(_ *wails_menu.CallbackData) {
		if app.ctx == nil {
			return
		}
		// ResetSettings clears the URL + cookie jar; navigating to
		// the escape hatch path serves the configure form. Using
		// location.replace so the bounce isn't a history entry.
		wails_runtime.WindowExecJS(app.ctx,
			`window.go.main.App.ResetSettings().then(function(){location.replace('/__desktop/configure');});`)
		core.Log("[gohort-desktop] menu: Change Server invoked")
	})

	custom.AddText("Log Out", keys.Combo("L", keys.CmdOrCtrlKey, keys.ShiftKey), func(_ *wails_menu.CallbackData) {
		if app.ctx == nil {
			return
		}
		// Clear the local jar AND hit gohort's /logout so server-side
		// session state is destroyed too. /logout returns a redirect
		// to /login which the proxy's rewrite_redirect_as_js converts
		// into an HTML+JS bounce — WKWebView follows that fine.
		wails_runtime.WindowExecJS(app.ctx,
			`window.go.main.App.LogOut().then(function(){location.replace('/logout');});`)
		core.Log("[gohort-desktop] menu: Log Out invoked")
	})

	custom.AddSeparator()

	// Tool-approval mode toggle. When checked, the desktop skips
	// the modal prompt and silently approves every server-initiated
	// tool call. Off by default — the user opts in to trust mode.
	// State is persisted (kvlite) so the choice survives restarts.
	autoCb := custom.AddCheckbox("Auto-approve tool calls",
		app.GetAutoApprove(), nil, func(cb *wails_menu.CallbackData) {
			app.SetAutoApprove(cb.MenuItem.Checked)
			if cb.MenuItem.Checked {
				core.Log("[gohort-desktop] menu: tool calls now auto-approved")
			} else {
				core.Log("[gohort-desktop] menu: tool calls require manual approval")
			}
		})
	_ = autoCb

	// Manage the per-tool "Always allow" grants. Mirrors Show Logs:
	// proxy-served pages don't carry the Wails runtime, so we inject the
	// current list straight into the in-page manager via WindowExecJS
	// rather than relying on an EventsEmit the page may never receive.
	custom.AddText("Manage Tool Approvals…", nil, func(_ *wails_menu.CallbackData) {
		if app.ctx == nil {
			return
		}
		data, err := json.Marshal(app.GetAllowedTools())
		if err != nil {
			data = []byte("[]")
		}
		wails_runtime.WindowExecJS(app.ctx, "window.__desktop_tools_open && window.__desktop_tools_open("+string(data)+")")
	})

	custom.AddSeparator()

	// Bridge agent management — install/remove the separate
	// Gohort-Bridge.app as a login item, straight from the viewer. This
	// execs the agent's own --install (the viewer links no systray), so
	// Gohort-Bridge.app must already exist (in /Applications or beside
	// this app). After install we point the user at Full Disk Access,
	// which iMessage needs and can't be granted programmatically.
	custom.AddText("Install Gohort-Bridge…", nil, func(_ *wails_menu.CallbackData) {
		if app.ctx == nil {
			return
		}
		go func() {
			res := app.InstallBridge()
			if res.Error != "" {
				wails_runtime.MessageDialog(app.ctx, wails_runtime.MessageDialogOptions{
					Type: wails_runtime.WarningDialog, Title: "Gohort-Bridge", Message: res.Error,
				})
				return
			}
			sel, _ := wails_runtime.MessageDialog(app.ctx, wails_runtime.MessageDialogOptions{
				Type:          wails_runtime.InfoDialog,
				Title:         "Gohort-Bridge Installed",
				Message:       "The bridge is running and set to start at login.\n\nFor iMessage it needs Full Disk Access — grant it to Gohort-Bridge under System Settings → Privacy & Security → Full Disk Access.",
				Buttons:       []string{"Open Settings", "Later"},
				DefaultButton: "Open Settings",
			})
			if sel == "Open Settings" {
				wails_runtime.BrowserOpenURL(app.ctx, "x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles")
			}
			core.Log("[gohort-desktop] menu: Gohort-Bridge installed")
		}()
	})

	custom.AddText("Remove Gohort-Bridge", nil, func(_ *wails_menu.CallbackData) {
		if app.ctx == nil {
			return
		}
		go func() {
			res := app.UninstallBridge()
			msg, typ := "Gohort-Bridge has been removed from login items.", wails_runtime.InfoDialog
			if res.Error != "" {
				msg, typ = res.Error, wails_runtime.WarningDialog
			}
			wails_runtime.MessageDialog(app.ctx, wails_runtime.MessageDialogOptions{
				Type: typ, Title: "Gohort-Bridge", Message: msg,
			})
		}()
	})

	custom.AddSeparator()

	// (Filesystem read-allowlist management lives in the Gohort-Bridge
	// agent now — folder access is granted per-folder via a consent
	// prompt there, not from the viewer.)

	// Show the in-app log viewer (see log_buffer.go + the
	// "show-logs" event handler in proxy.go's popup_shim_script).
	// Useful when troubleshooting the WS bridge or a failed tool
	// invocation without spinning up a terminal to tail stderr.
	custom.AddText("Show Logs", keys.Combo("L", keys.CmdOrCtrlKey, keys.OptionOrAltKey), func(_ *wails_menu.CallbackData) {
		if app.ctx == nil {
			return
		}
		// Proxy-served pages don't carry the Wails JS runtime, so the
		// "show-logs" EventsOn listener never registers there and an
		// EventsEmit goes nowhere. Drive the overlay straight from Go via
		// WindowExecJS, injecting the current log snapshot (window.go's
		// GetLogs is likewise absent on proxy pages).
		data, err := json.Marshal(app.GetLogs())
		if err != nil {
			data = []byte("[]")
		}
		wails_runtime.WindowExecJS(app.ctx, "window.__desktop_logs_open && window.__desktop_logs_open("+string(data)+")")
	})

	custom.AddText("Reload", keys.CmdOrCtrl("R"), func(_ *wails_menu.CallbackData) {
		if app.ctx == nil {
			return
		}
		// location.reload() reloads the CURRENT proxied page; WindowReload
		// targets the frontend start URL, which isn't where the user is.
		wails_runtime.WindowExecJS(app.ctx, "window.location.reload()")
	})

	return wails_menu.NewMenuFromItems(
		wails_menu.AppMenu(),  // "Gohort": About, Hide, Quit
		wails_menu.EditMenu(), // Undo / Redo / Cut / Copy / Paste / Select All
		wails_menu.SubMenu("Account", custom),
		wails_menu.WindowMenu(), // Minimize, Zoom, Bring All to Front
	)
}
