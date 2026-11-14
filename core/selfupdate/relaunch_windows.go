//go:build windows

package selfupdate

import "errors"

// CanRelaunch is whether this platform starts the new binary in place of
// the old one by itself. Windows has no exec, and a new process started
// before this one exits cannot take its port, so an update there is
// installed and the program is restarted by whoever runs it.
const CanRelaunch = false

func relaunch(exe string) error {
	return errors.New("restart gohort to run the new version")
}
