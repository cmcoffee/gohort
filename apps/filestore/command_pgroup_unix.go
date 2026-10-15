//go:build unix

package filestore

import (
	"os/exec"
	"syscall"
)

// setCommandGroup runs a registered command in its own process group, and
// makes stopping it kill the whole group: the default stop kills only the
// command itself, and a helper it started lives on, holding the output pipe
// the run reads.
func setCommandGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
}
