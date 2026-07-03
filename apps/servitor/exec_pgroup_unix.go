//go:build unix

package servitor

import (
	"os/exec"
	"syscall"
)

// set_process_group places the command in its own process group and installs
// a Cancel that kills the whole group on timeout. CommandContext's default
// cancel kills only the direct child (the sh -c leader) — a grandchild the
// shell spawned survives as an orphan, keeps running on the host, and holds
// the inherited output pipes open (observed: a hung kitebroker invocation
// inside a probe shell loop outlived the killed sh and wedged the session).
// Killing the negative pid reaps the entire tree.
func set_process_group(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
}
