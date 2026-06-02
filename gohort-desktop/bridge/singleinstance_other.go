//go:build !darwin && !linux

package bridge

import "os"

// acquireSingleInstance is a no-op off unix (the Windows Run key doesn't
// double-launch the way launchd + open do). Always proceeds.
func acquireSingleInstance() (*os.File, bool) { return nil, true }
