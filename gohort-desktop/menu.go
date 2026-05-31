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
	"github.com/cmcoffee/gohort/gohort-desktop/core"
	wails_menu "github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	wails_runtime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// build_app_menu constructs the full menu tree. Returns the menu so
// main.go passes it into wails.Run's Menu option in one line.
func build_app_menu(app *App) *wails_menu.Menu {
	custom := wails_menu.NewMenu()

	custom.AddText("Change Server…", keys.CmdOrCtrl(","), func(_ *wails_menu.CallbackData) {
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

	custom.AddSeparator()

	// Filesystem read-allowlist management — controls which folders
	// the desktop's filesystem.* tools (read_local_file, list_directory)
	// may resolve paths under. Add uses the OS-native folder picker;
	// Show opens an in-page modal listing the current roots with a
	// remove button per row (popup_shim_script catches the event).
	custom.AddText("Add Allowed Folder…", nil, func(_ *wails_menu.CallbackData) {
		if app.ctx == nil {
			return
		}
		// Run on the Wails ctx so the dialog parent is the app window
		// and OS focus behaves correctly. Adding is done inside
		// PickReadRoot — the menu callback just kicks it.
		go func() {
			res := app.PickReadRoot()
			if res.Error != "" {
				core.Warn("[gohort-desktop] menu: Add Allowed Folder failed: %s", res.Error)
				return
			}
			if res.Path == "" {
				return // user canceled
			}
			// Notify the open page so an active "Allowed Folders"
			// modal can refresh without a manual reopen.
			wails_runtime.EventsEmit(app.ctx, "allowed-folders-changed")
		}()
	})

	custom.AddText("Show Allowed Folders", nil, func(_ *wails_menu.CallbackData) {
		if app.ctx == nil {
			return
		}
		wails_runtime.EventsEmit(app.ctx, "show-allowed-folders")
	})

	custom.AddSeparator()

	// Show the in-app log viewer (see log_buffer.go + the
	// "show-logs" event handler in proxy.go's popup_shim_script).
	// Useful when troubleshooting the WS bridge or a failed tool
	// invocation without spinning up a terminal to tail stderr.
	custom.AddText("Show Logs", keys.Combo("L", keys.CmdOrCtrlKey, keys.OptionOrAltKey), func(_ *wails_menu.CallbackData) {
		if app.ctx == nil {
			return
		}
		wails_runtime.EventsEmit(app.ctx, "show-logs")
	})

	custom.AddText("Reload", keys.CmdOrCtrl("R"), func(_ *wails_menu.CallbackData) {
		if app.ctx == nil {
			return
		}
		wails_runtime.WindowReload(app.ctx)
	})

	return wails_menu.NewMenuFromItems(
		wails_menu.AppMenu(),  // "Gohort": About, Hide, Quit
		wails_menu.EditMenu(), // Undo / Redo / Cut / Copy / Paste / Select All
		wails_menu.SubMenu("Account", custom),
		wails_menu.WindowMenu(), // Minimize, Zoom, Bring All to Front
	)
}
