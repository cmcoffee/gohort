// Persistent shell-mode tool archives. Code that backs a persistent
// tool (scripts, helpers, fixtures) is captured at create time as a
// tar.gz under <workspaces-dir>/_tools/<owner>/<name>.tar.gz. The DB
// holds only a path + size + hash; the canonical content lives on
// disk where it can be inspected, backed up, and version-controlled
// using standard tools.
//
// At dispatch, the runtime extracts the archive into a per-invocation
// tmpdir, runs the command with that tmpdir as the workspace, then
// tears it down. Tools that need persistent runtime state opt into
// it via TempTool.StatePath, which gets rsync'd between the tmpdir
// and a per-tool _state/ directory around each run.

package core

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	toolArchivesSubdir = "_tools"
	toolStatesSubdir   = "_state"
	maxToolArchiveSize = 10 * 1024 * 1024 // 10 MB packed
)

// validateToolNameForFS rejects names that would escape the per-owner
// archive directory. Same rules as workspace IDs — no separators, no
// `..`, non-empty.
func validateToolNameForFS(name string) error {
	if name == "" {
		return fmt.Errorf("tool name required")
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return fmt.Errorf("invalid tool name for filesystem: %q", name)
	}
	return nil
}

// validateOwnerForFS — same constraints as tool name. Owner is a
// username; should already be safe but we re-check at the FS boundary.
func validateOwnerForFS(owner string) error {
	if owner == "" {
		return fmt.Errorf("owner required")
	}
	if strings.ContainsAny(owner, `/\`) || strings.Contains(owner, "..") {
		return fmt.Errorf("invalid owner for filesystem: %q", owner)
	}
	return nil
}

// ToolArchivePath returns the absolute path to where this tool's
// archive should live. Does not check whether it exists.
func ToolArchivePath(owner, name string) (string, error) {
	if err := validateOwnerForFS(owner); err != nil {
		return "", err
	}
	if err := validateToolNameForFS(name); err != nil {
		return "", err
	}
	base := WorkspacesDir()
	if base == "" {
		return "", fmt.Errorf("workspaces dir not configured")
	}
	return filepath.Join(base, toolArchivesSubdir, owner, name+".tar.gz"), nil
}

// ToolStateDir returns the absolute path to where this tool's
// persistent state lives between invocations. Created on first
// access. Empty when StatePath isn't set on the tool.
func ToolStateDir(owner, name string) (string, error) {
	if err := validateOwnerForFS(owner); err != nil {
		return "", err
	}
	if err := validateToolNameForFS(name); err != nil {
		return "", err
	}
	base := WorkspacesDir()
	if base == "" {
		return "", fmt.Errorf("workspaces dir not configured")
	}
	dir := filepath.Join(base, toolStatesSubdir, owner, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("mkdir state: %w", err)
	}
	return dir, nil
}

// PackToolArchive creates a tar.gz of every regular file under
// workspaceDir, excluding symlinks (security) and .fetch_cache (LRU
// cache, not source). Writes the result to ToolArchivePath(owner,name)
// after verifying it fits under maxToolArchiveSize.
//
// Returns absolute archive path, packed size, and sha256 hex of the
// tarball contents.
func PackToolArchive(workspaceDir, owner, name string) (string, int64, string, error) {
	archivePath, err := ToolArchivePath(owner, name)
	if err != nil {
		return "", 0, "", err
	}
	info, err := os.Stat(workspaceDir)
	if err != nil {
		return "", 0, "", fmt.Errorf("workspace stat: %w", err)
	}
	if !info.IsDir() {
		return "", 0, "", fmt.Errorf("workspace %q is not a directory", workspaceDir)
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	walkErr := filepath.Walk(workspaceDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(workspaceDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Skip symlinks — refuse to bake host-pointer links into a
		// portable archive.
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		// Skip the fetch cache; it's LRU runtime, not tool source.
		if strings.HasPrefix(filepath.ToSlash(rel), ".fetch_cache/") || rel == ".fetch_cache" {
			return nil
		}
		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, f)
		f.Close()
		if copyErr != nil {
			return copyErr
		}
		// Cap check after each file so a runaway loop doesn't blow
		// up RAM before the size error fires.
		if buf.Len() > maxToolArchiveSize {
			return fmt.Errorf("archive exceeds %d bytes — simplify the tool's workspace or split into multiple tools", maxToolArchiveSize)
		}
		return nil
	})
	if walkErr != nil {
		return "", 0, "", walkErr
	}
	if err := tw.Close(); err != nil {
		return "", 0, "", fmt.Errorf("tar close: %w", err)
	}
	if err := gz.Close(); err != nil {
		return "", 0, "", fmt.Errorf("gzip close: %w", err)
	}

	data := buf.Bytes()
	if int64(len(data)) > maxToolArchiveSize {
		return "", 0, "", fmt.Errorf("archive size %d exceeds limit %d", len(data), maxToolArchiveSize)
	}

	if err := os.MkdirAll(filepath.Dir(archivePath), 0700); err != nil {
		return "", 0, "", fmt.Errorf("mkdir archive dir: %w", err)
	}
	if err := os.WriteFile(archivePath, data, 0600); err != nil {
		return "", 0, "", fmt.Errorf("write archive: %w", err)
	}
	sum := sha256.Sum256(data)
	return archivePath, int64(len(data)), hex.EncodeToString(sum[:]), nil
}

// UnpackToolArchive extracts archivePath into destDir. destDir must
// exist (caller mkdir's it). Refuses entries whose paths escape
// destDir; refuses symlinks; refuses entries larger than
// maxToolArchiveSize as a tar-bomb guard.
func UnpackToolArchive(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	cleanedRoot := filepath.Clean(destDir)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar next: %w", err)
		}
		rel := filepath.FromSlash(hdr.Name)
		// Refuse absolute and traversal entries.
		if filepath.IsAbs(rel) || strings.Contains(rel, "..") {
			return fmt.Errorf("archive contains unsafe path: %q", hdr.Name)
		}
		target := filepath.Clean(filepath.Join(cleanedRoot, rel))
		if target != cleanedRoot && !strings.HasPrefix(target, cleanedRoot+string(filepath.Separator)) {
			return fmt.Errorf("archive entry escapes destDir: %q", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0700); err != nil {
				return fmt.Errorf("mkdir %q: %w", target, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return fmt.Errorf("mkdir parent of %q: %w", target, err)
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0700)
			if err != nil {
				return fmt.Errorf("create %q: %w", target, err)
			}
			if _, err := io.CopyN(out, tr, maxToolArchiveSize); err != nil && err != io.EOF {
				out.Close()
				return fmt.Errorf("write %q: %w", target, err)
			}
			out.Close()
		default:
			// Skip symlinks, devices, fifos — anything not a regular
			// file or directory. Defense in depth even though Pack
			// shouldn't write them.
			continue
		}
	}
	return nil
}

// CopyToolStateInto copies the persistent state dir for a tool into a
// subdirectory of the per-invocation workspace. Used at dispatch time
// before the command runs. Best-effort — missing state dir is fine
// (first invocation, or stateless tool that recently added StatePath).
func CopyToolStateInto(owner, name, destStateDir string) error {
	src, err := ToolStateDir(owner, name)
	if err != nil {
		return err
	}
	return copyTree(src, destStateDir)
}

// CopyToolStateBack saves the workspace's state subdir back to the
// persistent state dir. Called after a successful dispatch.
func CopyToolStateBack(owner, name, srcStateDir string) error {
	dst, err := ToolStateDir(owner, name)
	if err != nil {
		return err
	}
	// Wipe existing state to avoid stale files lingering when the
	// tool removes one. Then copy the new state in.
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("clear state: %w", err)
	}
	if err := os.MkdirAll(dst, 0700); err != nil {
		return fmt.Errorf("mkdir state: %w", err)
	}
	return copyTree(srcStateDir, dst)
}

// DeleteToolArchive removes the on-disk archive (idempotent).
func DeleteToolArchive(owner, name string) error {
	p, err := ToolArchivePath(owner, name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// DeleteToolState removes the on-disk state dir for a tool. Called
// when an operator wants to fully purge a stateful tool's history.
func DeleteToolState(owner, name string) error {
	p, err := ToolStateDir(owner, name)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// copyTree recursively copies src to dst, preserving file modes.
// Symlinks are skipped. Used for state preservation around dispatch;
// not a general-purpose util because it's tuned for small state dirs.
func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to copy
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("copyTree: %q is not a directory", src)
	}
	if err := os.MkdirAll(dst, 0700); err != nil {
		return err
	}
	return filepath.Walk(src, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if fi.IsDir() {
			return os.MkdirAll(target, fi.Mode().Perm()&0700)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm()&0700)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}
