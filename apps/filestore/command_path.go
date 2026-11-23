package filestore

// What a registered command's path leads to.
//
// An admin gives a command as an absolute path, and that path is often a
// link: /usr/bin/tshark to wherever the package put it, /usr/local/bin/x to
// a versioned install. The link is resolved when the command is saved, and
// the file it led to is what runs. Before each run the link is resolved
// again: if it now leads somewhere else (an upgrade moved it, or somebody
// pointed it at another file), the command does not run until an admin
// saves it again, so what runs on this host is never changed by a link
// nobody here looked at.
//
// The command line keeps the path as the admin gave it, both in mapped lines
// (which match it as written) and as the program's argv[0], which multi-call
// binaries (busybox and the like) read to decide what to be.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// resolveCommandPath is the real file an absolute command path leads to, which
// must exist, be a regular file, and be executable. A variable so the
// package's tests can register commands at paths that are not on this host.
var resolveCommandPath = resolveOnDisk

func resolveOnDisk(path string) (string, error) {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return "", Error("the command must be an absolute path: a relative one resolves against whatever directory the server happens to be in")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", Error(fmt.Sprintf("nothing is installed at %s (or a link on the way leads nowhere): install the program on this server first, or give the path it is installed at", path))
		}
		return "", Error(fmt.Sprintf("%s could not be followed: %v", path, err))
	}
	fi, err := os.Stat(real)
	switch {
	case err != nil:
		return "", Error(fmt.Sprintf("%s could not be read: %v", real, err))
	case fi.IsDir():
		return "", Error(fmt.Sprintf("%s is a folder, not a program", real))
	case !fi.Mode().IsRegular():
		return "", Error(fmt.Sprintf("%s is not a program file", real))
	case fi.Mode().Perm()&0o111 == 0:
		return "", Error(fmt.Sprintf("%s is not executable", real))
	}
	return real, nil
}

// binary is the file a run of a executes: the path as resolved now, refused
// when it no longer leads where it did when the command was saved. A command
// saved before paths were resolved has no Resolved, and runs what its path
// leads to now, as it always did.
func (a StoreCommand) binary() (string, error) {
	now, err := resolveCommandPath(a.Command)
	if err != nil {
		return "", err
	}
	if a.Resolved != "" && now != a.Resolved {
		return "", Error(fmt.Sprintf("%s now leads to %s, not to %s as it did when the command was saved, so it was not run: an admin opens the command under Admin, Files and saves it again to run the new file", a.Command, now, a.Resolved))
	}
	return now, nil
}

// runsLabel is the command as an admin's list shows it: the path, and where it
// leads when that is somewhere else.
func (a StoreCommand) runsLabel() string {
	if a.Resolved != "" && a.Resolved != a.Command {
		return a.Command + " (leads to " + a.Resolved + ")"
	}
	return a.Command
}
