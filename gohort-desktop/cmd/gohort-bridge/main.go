// gohort-bridge is the Gohort-Bridge agent binary. Packaged as
// Gohort-Bridge.app (LSUIElement) it runs as a menu-bar agent that hosts the
// iMessage relay + WS tool bridge and offers "Open Gohort". It is a separate
// process from the viewer (Gohort.app): the viewer installs and removes it
// by running this binary with --install / --uninstall, and launchd starts it
// at login once installed.
//
// Rebuilt 2026-10-11. The original was never committed: the desktop
// .gitignore's bare "gohort-bridge" rule, meant for a stray built binary,
// also matched this directory, and a pair of directory moves lost the only
// copy. The rule is anchored now.
package main

import (
	"fmt"
	"os"

	"github.com/cmcoffee/gohort/gohort-desktop/bridge"
	"github.com/cmcoffee/gohort/gohort-desktop/core"
)

func main() {
	mode := ""
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	switch mode {
	case "":
		// systray owns the main thread and the Cocoa run loop: Run is called
		// here, on main, and blocks until the user quits from the menu.
		bridge.Run()
	case "--install":
		if err := bridge.Install(); err != nil {
			core.Fatal("gohort-bridge: install: %v", err)
		}
		core.Log("gohort-bridge: registered to run at login")
	case "--uninstall":
		if err := bridge.Uninstall(); err != nil {
			core.Fatal("gohort-bridge: uninstall: %v", err)
		}
		core.Log("gohort-bridge: login item removed")
	case "--installed":
		if !bridge.Installed() {
			fmt.Println("not installed")
			os.Exit(1)
		}
		fmt.Println("installed")
	case "--test":
		bridge.Test()
	case "--setup":
		bridge.Setup()
	case "-h", "--help", "help":
		usage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "gohort-bridge: unknown argument %q\n\n", mode)
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `gohort-bridge: the Gohort-Bridge menu-bar agent.

  gohort-bridge               run the agent (menu bar; what launchd starts)
  gohort-bridge --install     register it to run at login
  gohort-bridge --uninstall   remove the login item
  gohort-bridge --installed   exit 0 if registered, 1 if not
  gohort-bridge --test        print recent Messages (macOS; checks Full Disk Access)
  gohort-bridge --setup       headless configuration wizard (the viewer does this too)
`)
}
