//go:build darwin || linux

package main

import (
	"fmt"
	"os"
	"syscall"
)

// copyStoppedDB copies the database at src to dst, holding the file lock a
// running gohort takes while it does: when gohort holds it, it is running and
// the copy is refused; holding it keeps gohort from starting mid-copy.
func copyStoppedDB(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("%s is in use: stop gohort, then run this again", src)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return copyFile(src, dst)
}
