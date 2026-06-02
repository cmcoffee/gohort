package main

// Lets the viewer (Gohort.app) install/remove the separate Gohort-Bridge
// agent as a login item, and launch it — without importing the bridge
// package (which would link systray and clobber Wails's app delegate).
// Instead it locates the agent binary and execs its own --install /
// --uninstall, so the agent registers itself with the correct path.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// findBridgeBinary locates the Gohort-Bridge agent executable: the
// standard /Applications install first, then a sibling of the viewer's
// own bundle (so it works when both apps sit in the same folder, e.g.
// build/bin during development). Returns "" if not found.
func findBridgeBinary() string {
	var cands []string
	switch runtime.GOOS {
	case "darwin":
		cands = append(cands, "/Applications/Gohort-Bridge.app/Contents/MacOS/gohort-bridge")
		if exe, err := os.Executable(); err == nil {
			appBundle := filepath.Dir(filepath.Dir(filepath.Dir(exe))) // …/Gohort.app
			sibling := filepath.Dir(appBundle)                         // folder holding the bundles
			cands = append(cands, filepath.Join(sibling, "Gohort-Bridge.app", "Contents", "MacOS", "gohort-bridge"))
		}
	default:
		if exe, err := os.Executable(); err == nil {
			cands = append(cands, filepath.Join(filepath.Dir(exe), "gohort-bridge.exe"))
			cands = append(cands, filepath.Join(filepath.Dir(exe), "gohort-bridge"))
		}
	}
	for _, c := range cands {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// InstallBridge registers the Gohort-Bridge agent to run at login and
// launches it now. Exposed to JS as window.go.main.App.InstallBridge().
func (a *App) InstallBridge() save_result {
	bin := findBridgeBinary()
	if bin == "" {
		return save_result{Error: "Gohort-Bridge.app not found — install it (e.g. `make install-all`) or drag it to /Applications, then try again."}
	}
	if out, err := exec.Command(bin, "--install").CombinedOutput(); err != nil {
		return save_result{Error: fmt.Sprintf("Bridge install failed: %v — %s", err, strings.TrimSpace(string(out)))}
	}
	// Launch it now so the user doesn't have to wait for next login.
	if runtime.GOOS == "darwin" {
		exec.Command("open", "-a", "Gohort-Bridge").Start()
	} else {
		exec.Command(bin).Start()
	}
	return save_result{OK: true}
}

// UninstallBridge removes the agent's login item. Exposed to JS as
// window.go.main.App.UninstallBridge().
func (a *App) UninstallBridge() save_result {
	bin := findBridgeBinary()
	if bin == "" {
		return save_result{Error: "Gohort-Bridge.app not found."}
	}
	if out, err := exec.Command(bin, "--uninstall").CombinedOutput(); err != nil {
		return save_result{Error: fmt.Sprintf("Bridge uninstall failed: %v — %s", err, strings.TrimSpace(string(out)))}
	}
	return save_result{OK: true}
}
