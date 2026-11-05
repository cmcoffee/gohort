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
// Open, Create and the rest hold only the last component of the path to that.
// The folders on the way were checked by the caller (core.ResolveWorkspacePath)
// before the open, and a script running in the workspace could swap one for a
// link in between: the server then wrote where the link pointed, its own data
// folder included. OpenIn, CreateIn and the others take the workspace as a
// root (os.Root) and walk the path a folder at a time from it, refusing any
// step out of it, so a swap made at any moment has nowhere to lead. Use them
// wherever the server reads or writes in a folder a script can write.
//
// A leaf package rather than a file in core for the reason core/notices is:
// core sits at its export ceiling.
package safefile

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

// inRoot names path inside root: relative to it as given, or the part of an
// absolute path under it. A path that leaves root is refused.
func inRoot(root, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is not inside %s", path, root)
	}
	return rel, nil
}

// within opens root and runs fn with path inside it.
func within(root, path string, fn func(r *os.Root, name string) error) error {
	name, err := inRoot(root, path)
	if err != nil {
		return err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	return fn(r, name)
}

// OpenIn is Open for a file under root: no step of the path, the file's own
// name included, may lead out of it. A link that stays inside is followed, as
// it leads only where the script could write anyway.
func OpenIn(root, path string) (*os.File, error) {
	var f *os.File
	err := within(root, path, func(r *os.Root, name string) error {
		var err error
		if f, err = r.OpenFile(name, os.O_RDONLY|noFollow, 0); err != nil {
			return err
		}
		if err := regular(f, path); err != nil {
			f.Close()
			f = nil
			return err
		}
		return nil
	})
	return f, err
}

// ReadFileIn is ReadFile through OpenIn.
func ReadFileIn(root, path string) ([]byte, error) {
	f, err := OpenIn(root, path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// CreateIn is Create for a file under root: no step of the path may lead out
// of it, and a link or other non-file at the file itself is replaced.
func CreateIn(root, path string, perm os.FileMode) (*os.File, error) {
	var f *os.File
	err := within(root, path, func(r *os.Root, name string) error {
		if info, err := r.Lstat(name); err == nil && !info.Mode().IsRegular() {
			if info.IsDir() {
				return fmt.Errorf("%s is a folder", path)
			}
			if err := r.Remove(name); err != nil {
				return err
			}
		}
		var err error
		if f, err = r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|noFollow, perm); err != nil {
			return err
		}
		if err := regular(f, path); err != nil {
			f.Close()
			f = nil
			return err
		}
		return nil
	})
	return f, err
}

// OpenFileIn is os.OpenFile for a file under root: no step of the path may
// lead out of it, a link at the file itself is not followed, and what opens
// has to be a regular file. For the opens CreateIn does not cover: a create
// that must not replace anything (O_EXCL), an append.
func OpenFileIn(root, path string, flag int, perm os.FileMode) (*os.File, error) {
	var f *os.File
	err := within(root, path, func(r *os.Root, name string) error {
		var err error
		if f, err = r.OpenFile(name, flag|noFollow, perm); err != nil {
			return err
		}
		if err := regular(f, path); err != nil {
			f.Close()
			f = nil
			return err
		}
		return nil
	})
	return f, err
}

// WriteFileIn is WriteFile through CreateIn.
func WriteFileIn(root, path string, data []byte, perm os.FileMode) error {
	f, err := CreateIn(root, path, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// MkdirAllIn is os.MkdirAll for a folder under root, which follows no link
// out of it: a folder swapped for a link would otherwise have the server
// make folders wherever it pointed.
func MkdirAllIn(root, dir string, perm os.FileMode) error {
	return within(root, dir, func(r *os.Root, name string) error {
		if name == "." {
			return nil
		}
		return r.MkdirAll(name, perm)
	})
}

// RemoveIn removes a file under root, the name itself and never what a link
// there points to, through no link out of root on the way: a cleanup after a
// failed write would otherwise delete wherever a swapped folder led.
func RemoveIn(root, path string) error {
	return within(root, path, func(r *os.Root, name string) error {
		return r.Remove(name)
	})
}
