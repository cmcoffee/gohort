// Package bridge is the Gohort-Bridge agent: a menu-bar (system tray)
// app that hosts the iMessage relay + the WebSocket tool bridge to the
// gohort server, and offers an "Open Gohort" item that launches the
// separate viewer app (Gohort.app). It is its OWN .app bundle
// (com.gohort.bridge, LSUIElement — no dock icon) and its own process,
// distinct from the Wails viewer, so there's no single-instance/dock
// conflict and systray's NSApplication-delegate use is harmless (the
// agent is not a Wails app).
//
// Config comes from the lock-free sidecar the viewer writes
// (core.BridgeConfig) — the two processes never share a kvlite file.
// The agent is the only process that needs Full Disk Access (chat.db).
package bridge

import (
	_ "embed"

	"fyne.io/systray"

	"github.com/cmcoffee/gohort/gohort-desktop/command"
	"github.com/cmcoffee/gohort/gohort-desktop/core"
	"github.com/cmcoffee/gohort/gohort-desktop/mcp"
	"github.com/cmcoffee/gohort/gohort-desktop/wsbridge"
	"github.com/cmcoffee/snugforge/nfo"
)

//go:embed appicon.png
var appIcon []byte

// sidecarCfg is a live wsbridge.Config backed by the bridge-config
// sidecar — re-read on every access so a config edit in the viewer
// takes effect without restarting the agent.
type sidecarCfg struct{}

func (sidecarCfg) ServerURL() string { return core.ReadBridgeConfig().ServerURL }
func (sidecarCfg) APIKey() string    { return core.BridgeAPIKey() }

// Run is the agent's main loop: start the native services (iMessage
// relay on macOS) + the WS tool bridge from the sidecar config, then
// show the menu-bar item. systray.Run owns the main thread / Cocoa run
// loop and must be called from main() — never wrapped in `go`. Blocks
// until the user quits from the menu.
func Run() {
	// Single-instance: if another bridge already holds the lock, exit
	// cleanly so we don't end up with two menu-bar icons (launchd's copy
	// + a hand-launched / rebuild copy). lock stays open for our lifetime.
	lock, ok := acquireSingleInstance()
	if !ok {
		core.Warn("[bridge] another Gohort-Bridge is already running — exiting")
		return
	}
	defer func() {
		if lock != nil {
			lock.Close()
		}
	}()

	if p := logPath(); p != "" {
		if _, err := nfo.LogFile(p, 10, 1); err != nil {
			core.Warn("cannot open log file %s: %v", p, err)
		}
	}

	initFSConsent()        // load approved folders + wire just-in-time consent
	startNativeServices()  // iMessage relay (macOS); no-op elsewhere
	stopMCP := mcp.Start() // host configured MCP servers; their tools register like native ones
	command.Start()        // host persisted declared-command tools (desktop_command)
	// daemonInstaller lets the server push a new local capability (a desktop_mcp
	// or desktop_command connector, once admin-approved AND user-consented) —
	// applied through the mcp / command hosts, persisted so it survives restart.
	stopWS := wsbridge.StartClient(sidecarCfg{}, daemonApprover{}, daemonInstaller{})

	onReady := func() {
		systray.SetIcon(appIcon)
		systray.SetTooltip("Gohort bridge")

		mOpen := systray.AddMenuItem("Open Gohort", "Open the Gohort window")
		systray.AddSeparator()
		mStatus := systray.AddMenuItem(daemonStatus(), "")
		mStatus.Disable()
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("Quit Gohort Bridge", "Stop the bridge")

		go func() {
			for {
				select {
				case <-mOpen.ClickedCh:
					if err := openViewer(); err != nil {
						core.Warn("[bridge] open Gohort: %v", err)
					}
				case <-mQuit.ClickedCh:
					systray.Quit()
					return
				}
			}
		}()
	}

	systray.Run(onReady, func() { stopMCP(); stopWS() })
}

// daemonStatus is the one-line status shown (disabled) in the menu.
func daemonStatus() string {
	c := core.ReadBridgeConfig()
	if c.ServerURL == "" || core.BridgeAPIKey() == "" {
		return "Not configured — set up in Gohort"
	}
	return "Connected"
}
