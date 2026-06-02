package bridge

// Exported entry points the root package's main() dispatches to by
// flag. Setup/Install/Uninstall/Test are the management actions; Run
// (in daemon.go) is the normal always-on mode. Keeping these here as
// thin portable wrappers lets the per-OS implementations stay
// unexported in the platform_*.go files.

// Install registers the Gohort binary to run at login in --bridge mode
// (launchd on macOS, HKCU Run key on Windows). It also tears down the
// retired com.gohort.phantom-bridge agent on macOS.
func Install() error { return installAutostart() }

// Uninstall removes the login item.
func Uninstall() error { return uninstallAutostart() }

// Test prints recent Messages and exits (macOS; verifies Full Disk
// Access). A no-op-with-notice on other platforms.
func Test() { runTest() }

// Installed reports whether the bridge is registered to run at login
// (launchd plist on macOS, HKCU Run key on Windows). Lets the viewer's
// menu reflect current state.
func Installed() bool { return installed() }
