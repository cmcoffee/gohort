//go:build !windows

package selfupdate

import (
	"os"
	"syscall"
)

// CanRelaunch is whether this platform starts the new binary in place of
// the old one by itself.
const CanRelaunch = true

// relaunch replaces this process with exe, with the same arguments and
// environment: the process ID, the terminal and the parent stay, so it works
// under tmux, under systemd and under nothing. It returns only on failure.
func relaunch(exe string) error {
	return syscall.Exec(exe, os.Args, os.Environ())
}
