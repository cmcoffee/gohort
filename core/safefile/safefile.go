// Package safefile reads and writes files in folders a sandboxed script can
// also write: the agents' workspaces.
//
// The server process is not sandboxed. A script that leaves a symbolic link
// where the server expects its own file (the script it redeploys, a file it
// hands to the next agent, a download's destination) turns the server's next
// read or write of that name into one of wherever the link points: another
// user's workspace, the server's own data. A check on the path beforehand is
// not enough, because the script can plant the link after the check. These
// open the name itself, refusing to follow a link at it, and refuse anything
// that is not a regular file (a FIFO would hang the server on open).
//
// Only the last component of the path is held to that. A caller that takes a
// path with folders in it from a workspace still resolves it with
// core.ResolveWorkspacePath, which refuses links along the way.
//
// A leaf package rather than a file in core for the reason core/notices is:
// core sits at its export ceiling.
package safefile

import (
	"fmt"
	"io"
	"os"
)

// Open opens path for reading when it is a regular file, never following a
// link at it.
func Open(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|noFollow, 0)
	if err != nil {
		return nil, err
	}
	if err := regular(f, path); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// ReadFile is os.ReadFile through Open.
func ReadFile(path string) ([]byte, error) {
	f, err := Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// Create opens path for writing, truncated, as os.Create does, but writes the
// file at path and never one a link there points to. Something other than a
// regular file already at path (a link, a FIFO) is removed first, the link
// itself and not what it names: these are names the server writes, and a
// workspace owner who swapped one for a link gets the file back.
func Create(path string, perm os.FileMode) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		if info.IsDir() {
			return nil, fmt.Errorf("%s is a folder", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|noFollow, perm)
	if err != nil {
		return nil, err
	}
	if err := regular(f, path); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// WriteFile is os.WriteFile through Create.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	f, err := Create(path, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func regular(f *os.File, path string) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	return nil
}
