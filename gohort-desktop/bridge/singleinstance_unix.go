//go:build darwin || linux

package bridge

import (
	"os"
	"path/filepath"
	"syscall"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
)

// acquireSingleInstance becomes the sole running bridge by holding an
// exclusive flock on a lock file. If another instance already holds it,
// ok is false and the caller exits — this prevents the duplicate
// menu-bar icons that appear when launchd's copy and a hand-launched
// copy (or a rebuild's build/bin copy) run at once. The returned file
// MUST stay open for the process lifetime; closing it releases the lock.
func acquireSingleInstance() (*os.File, bool) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, true // can't determine a lock path — don't block startup
	}
	d := filepath.Join(dir, core.SETTINGS_DIR_NAME)
	os.MkdirAll(d, 0o700)
	f, err := os.OpenFile(filepath.Join(d, "bridge.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, false // another instance holds the lock
	}
	return f, true
}
