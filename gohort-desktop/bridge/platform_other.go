//go:build !darwin && !windows

package bridge

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
)

// logPath puts the log under the user config dir on other platforms
// (mainly Linux dev hosts).
func logPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	d := filepath.Join(dir, "gohort-desktop")
	os.MkdirAll(d, 0o755)
	return filepath.Join(d, "gohort-bridge.log")
}

// openViewer tries the viewer binary next to the agent, then PATH.
func openViewer() error {
	name := "gohort-desktop"
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(cand); err == nil {
			return exec.Command(cand).Start()
		}
	}
	return exec.Command(name).Start()
}

// promptFolderConsent denies by default; no native dialog here.
func promptFolderConsent(folder string) bool {
	core.Warn("[fs] denying read of %s — no folder-consent prompt on this platform", folder)
	return false
}

func promptWriteConsent(folder string) bool {
	core.Warn("[fs] denying write to %s — no folder-consent prompt on this platform", folder)
	return false
}

// promptApproval denies by default; no native dialog on these platforms.
func promptApproval(name string, _ map[string]any) bool {
	core.Warn("[approval] denying %q — no prompt on this platform; set auto_approve to allow", name)
	return false
}

func installAutostart() error {
	return errors.New("autostart not supported on this platform (use launchd/macOS or Run key/Windows)")
}

func uninstallAutostart() error {
	return errors.New("autostart not supported on this platform")
}

func installed() bool { return false }
