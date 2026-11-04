//go:build unix

package filestore

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// access(2) modes. POSIX fixes these values, and syscall does not export them
// on every Unix.
const (
	accessR = 0x4
	accessW = 0x2
	accessX = 0x1
)

// cleanupBlocker says why this server's user cannot remove folders from a
// store's root at all, or "" when it can. Removing an entry takes write and
// search permission on the directory holding it.
func cleanupBlocker(root string) string {
	if syscall.Access(root, accessW|accessX) != nil {
		return "this server's user has no permission to delete in " + root
	}
	return ""
}

// removeBlocker says why this server's user could not remove the folder at dir
// with everything in it, or "" when it could. Checked BEFORE anything is
// removed, so a folder goes whole or stays whole: os.RemoveAll on a tree it
// can only partly delete empties what it can and then fails, leaving a folder
// with some of its files gone and nothing to say which.
func removeBlocker(dir string) string {
	parent := filepath.Dir(dir)
	if why := cleanupBlocker(parent); why != "" {
		return why
	}
	// A sticky directory (/tmp's kind) lets each user remove only what they
	// own, whatever the directory's own permissions say.
	if pi, err := os.Stat(parent); err == nil && pi.Mode()&os.ModeSticky != 0 {
		if fi, err := os.Lstat(dir); err == nil && !ownedByThisUser(fi) {
			return parent + " lets each user remove only their own folders, and " + filepath.Base(dir) + " is not this server's"
		}
	}
	why := ""
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			why = "this server's user cannot read " + p
			return fs.SkipAll
		}
		if d.IsDir() && syscall.Access(p, accessR|accessW|accessX) != nil {
			why = "this server's user has no permission to delete inside " + p
			return fs.SkipAll
		}
		return nil
	})
	return why
}

// ownedByThisUser reports whether the file belongs to the user this server
// runs as.
func ownedByThisUser(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}
