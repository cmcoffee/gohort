//go:build !(darwin || linux)

package main

import . "github.com/cmcoffee/gohort/core"

// copyStoppedDB copies the database at src to dst. Here it cannot tell
// whether gohort is running, so stop it first: a copy taken while it writes
// may be torn.
func copyStoppedDB(src, dst string) error {
	Warn("cannot check here whether gohort is running: stop it first, or the copy may be torn")
	return copyFile(src, dst)
}
