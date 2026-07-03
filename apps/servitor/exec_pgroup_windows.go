//go:build !unix

package servitor

import "os/exec"

// set_process_group is a no-op on non-unix platforms: CommandContext's
// default single-process kill applies, and WaitDelay still bounds the
// post-kill wait on the output pipes.
func set_process_group(c *exec.Cmd) {}
