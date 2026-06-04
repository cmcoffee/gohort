//go:build darwin

package bridge

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
)

const (
	launchdLabel    = "com.gohort.bridge"
	oldLaunchdLabel = "com.gohort.phantom-bridge" // retired; torn down on install
)

// logPath is where the daemon writes its log (launchd also points its
// StandardOut/ErrPath here).
func logPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Logs", "gohort-bridge.log")
}

// openViewer launches (or activates) the separate Gohort viewer app.
// `open -a` respects macOS single-instance semantics, so clicking
// repeatedly activates the existing window instead of spawning
// duplicates. Different bundle than this agent, so no conflict.
func openViewer() error {
	return exec.Command("open", "-a", "Gohort").Start()
}

// promptFolderConsent asks the user to approve read access to a folder
// the filesystem tools want to touch. Default button is Deny — folder
// access is a deliberate yes. Returns true only on "Allow".
func promptFolderConsent(folder string) bool {
	return nativeConfirm("Allow file access?",
		"The gohort server wants to read files in:\n\n"+folder+"\n\nThis choice is remembered.")
}

// promptWriteConsent asks the user to approve WRITE access to a folder.
// Separate, stronger ask than read — writes can create/overwrite files.
func promptWriteConsent(folder string) bool {
	return nativeConfirm("Allow file WRITE access?",
		"The gohort server wants to CREATE and OVERWRITE files in:\n\n"+folder+"\n\nThis choice is remembered.")
}

// promptApproval shows the native 3-way dialog for a server-initiated
// tool call. allow=true on "Allow once" or "Always allow"; always=true
// only on "Always allow" (the caller persists that to the allow-list).
func promptApproval(name string, _ map[string]any) (allow, always bool) {
	switch nativeApprove("Allow tool: "+name,
		"The gohort server wants to run this tool on your Mac.") {
	case 2:
		return true, true
	case 1:
		return true, false
	default:
		return false, false
	}
}

func plistPath(label string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

// installed reports whether our LaunchAgent plist exists.
func installed() bool {
	_, err := os.Stat(plistPath(launchdLabel))
	return err == nil
}

// installAutostart writes a LaunchAgent plist and bootstraps it, after
// tearing down any stale com.gohort.phantom-bridge agent left by the
// pre-consolidation bridge (it points at a now-deleted binary).
func installAutostart() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("determine executable path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("binary not found at %s: %w", exe, err)
	}

	uid := fmt.Sprintf("gui/%d", os.Getuid())

	// Retire the old phantom-bridge LaunchAgent so it doesn't keep
	// failing against a deleted binary.
	oldPlist := plistPath(oldLaunchdLabel)
	exec.Command("launchctl", "bootout", uid, oldPlist).Run()
	os.Remove(oldPlist)

	logFile := logPath()
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
	</array>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, launchdLabel, exe, logFile, logFile)

	path := plistPath(launchdLabel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
		return fmt.Errorf("create Logs dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}
	core.Log("binary: %s", exe)
	core.Log("plist:  %s", path)

	if out, err := exec.Command("plutil", "-lint", path).CombinedOutput(); err != nil {
		return fmt.Errorf("plist validation failed: %s", strings.TrimSpace(string(out)))
	}

	svcID := uid + "/" + launchdLabel
	exec.Command("launchctl", "bootout", uid, path).Run() // in case already loaded
	exec.Command("launchctl", "enable", svcID).Run()      // clear any disabled flag
	if out, err := exec.Command("launchctl", "bootstrap", uid, path).CombinedOutput(); err != nil {
		core.Warn("auto-load failed: %s", strings.TrimSpace(string(out)))
		core.Log("Load manually:\n  launchctl enable %s\n  launchctl bootstrap %s %s", svcID, uid, path)
	} else {
		core.Log("service loaded — gohort-bridge will start at login")
	}
	return nil
}

func uninstallAutostart() error {
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	path := plistPath(launchdLabel)
	exec.Command("launchctl", "bootout", uid, path).Run()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove plist: %w", err)
	}
	core.Log("service removed: %s", path)
	return nil
}
