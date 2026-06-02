//go:build windows

package bridge

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
	"golang.org/x/sys/windows/registry"
)

const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
const runKeyName = "GohortBridge"

// logPath writes under %LOCALAPPDATA%\gohort-desktop (a tray app has no
// console, so a file is the only place output can go).
func logPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	d := filepath.Join(dir, "gohort-desktop")
	os.MkdirAll(d, 0o755)
	return filepath.Join(d, "gohort-bridge.log")
}

// openViewer launches the Gohort viewer. It looks for Gohort.exe next
// to the agent binary first, then falls back to PATH.
func openViewer() error {
	name := "Gohort.exe"
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(cand); err == nil {
			return exec.Command(cand).Start()
		}
	}
	return exec.Command(name).Start()
}

// promptFolderConsent has no native dialog yet on Windows — deny so
// no folder is ever read without explicit approval.
func promptFolderConsent(folder string) bool {
	core.Warn("[fs] denying read of %s — no folder-consent prompt on Windows yet", folder)
	return false
}

func promptWriteConsent(folder string) bool {
	core.Warn("[fs] denying write to %s — no folder-consent prompt on Windows yet", folder)
	return false
}

// promptApproval has no native dialog yet on Windows — deny by default
// so tools never run unprompted. Users enable the auto_approve setting
// to allow server-initiated tool calls. (A native toast/dialog is a
// follow-up.)
func promptApproval(name string, _ map[string]any) bool {
	core.Warn("[approval] denying %q — no Windows prompt yet; set auto_approve to allow", name)
	return false
}

// installAutostart adds an HKCU Run entry so the daemon starts at login.
func installAutostart() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("determine executable path: %w", err)
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open Run key: %w", err)
	}
	defer k.Close()
	// Run the bridge agent at login (its default mode is the daemon).
	cmd := `"` + exe + `"`
	if err := k.SetStringValue(runKeyName, cmd); err != nil {
		return fmt.Errorf("set Run value: %w", err)
	}
	core.Log("registered to run at login: HKCU\\%s\\%s = %s", runKeyPath, runKeyName, cmd)
	return nil
}

// installed reports whether our HKCU Run value exists.
func installed() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runKeyName)
	return err == nil
}

func uninstallAutostart() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open Run key: %w", err)
	}
	defer k.Close()
	if err := k.DeleteValue(runKeyName); err != nil && err != registry.ErrNotExist {
		return fmt.Errorf("delete Run value: %w", err)
	}
	core.Log("removed login item: HKCU\\%s\\%s", runKeyPath, runKeyName)
	return nil
}
