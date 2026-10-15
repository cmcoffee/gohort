//go:build !unix

package filestore

import "os/exec"

// setCommandGroup leaves the default single-process kill where there are no
// Unix process groups; WaitDelay still bounds the wait for the output pipes
// (runRegisteredCommand).
func setCommandGroup(c *exec.Cmd) {}
