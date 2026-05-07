// Persistent shell-mode tool recipes. A persistent tool's deployable
// content (scripts, helpers, fixtures) lives inline on the TempTool
// record as a Recipe — a list of {path, content, mode} entries. At
// dispatch the runtime mints a fresh sandbox dir, writes each recipe
// file into it, runs the command, and tears the dir down.
//
// The recipe is human-readable, diffable in the admin UI, and
// reproducible: deploying twice always produces the same files.
//
// Tools that need state across invocations opt in via TempTool.StatePath
// — that subdirectory is rsync'd between the per-dispatch sandbox and a
// per-tool _state/ directory around each run, surviving the wipe.

package core

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	toolStatesSubdir   = "_state"
	toolDispatchSubdir = "_dispatch"
	maxRecipeFileSize  = 2 * 1024 * 1024  // 2 MB per file
	maxRecipeTotalSize = 10 * 1024 * 1024 // 10 MB across all files in one recipe
)

// MintToolDispatchDir creates a fresh per-invocation directory for a
// tool's recipe deployment. Lives under WorkspacesDir()/_dispatch/
// rather than /tmp because the sandbox overlays a fresh tmpfs at
// /tmp — anything we mint there gets shadowed before bwrap can chdir
// into it. The caller is responsible for os.RemoveAll on cleanup.
func MintToolDispatchDir(prefix string) (string, error) {
	base := WorkspacesDir()
	if base == "" {
		return "", fmt.Errorf("workspaces dir not configured")
	}
	parent := filepath.Join(base, toolDispatchSubdir)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", fmt.Errorf("mkdir dispatch parent: %w", err)
	}
	return os.MkdirTemp(parent, prefix)
}

// validateToolNameForFS rejects names that would escape the per-owner
// state directory. Same rules as workspace IDs — no separators, no
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

// BuildRecipeFromWorkspace walks workspaceDir and captures every
// regular file as a RecipeFile entry. Skips symlinks (security) and
// .fetch_cache (LRU cache, not source). Enforces per-file and total
// size caps so a runaway workspace can't bloat the persisted record.
func BuildRecipeFromWorkspace(workspaceDir string) ([]RecipeFile, error) {
	info, err := os.Stat(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("workspace stat: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace %q is not a directory", workspaceDir)
	}

	var recipe []RecipeFile
	var total int64

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
		// portable recipe.
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		// Skip the fetch cache; it's LRU runtime, not tool source.
		if strings.HasPrefix(filepath.ToSlash(rel), ".fetch_cache/") || rel == ".fetch_cache" {
			return nil
		}
		// Directories are recreated implicitly when files are written;
		// no need to record empty dirs.
		if fi.IsDir() {
			return nil
		}
		if fi.Size() > maxRecipeFileSize {
			return fmt.Errorf("file %q exceeds recipe per-file size cap (%d bytes)", rel, maxRecipeFileSize)
		}
		total += fi.Size()
		if total > maxRecipeTotalSize {
			return fmt.Errorf("recipe exceeds total size cap (%d bytes) — simplify the tool's workspace or split into multiple tools", maxRecipeTotalSize)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %q: %w", rel, err)
		}
		mode := uint32(fi.Mode().Perm())
		if mode == 0 {
			mode = 0700
		}
		recipe = append(recipe, RecipeFile{
			Path:    filepath.ToSlash(rel),
			Content: string(data),
			Mode:    mode,
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return recipe, nil
}

// DeployRecipe writes every RecipeFile into destDir. destDir must
// exist (caller mkdir's it). Refuses entries whose paths escape
// destDir as defense in depth (paths from the recipe are admin-
// reviewed but the FS boundary still re-checks).
func DeployRecipe(recipe []RecipeFile, destDir string) error {
	cleanedRoot := filepath.Clean(destDir)
	for _, f := range recipe {
		rel := filepath.FromSlash(f.Path)
		if filepath.IsAbs(rel) || strings.Contains(rel, "..") {
			return fmt.Errorf("recipe contains unsafe path: %q", f.Path)
		}
		target := filepath.Clean(filepath.Join(cleanedRoot, rel))
		if target != cleanedRoot && !strings.HasPrefix(target, cleanedRoot+string(filepath.Separator)) {
			return fmt.Errorf("recipe entry escapes destDir: %q", f.Path)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return fmt.Errorf("mkdir parent of %q: %w", target, err)
		}
		mode := os.FileMode(f.Mode) & 0700
		if mode == 0 {
			mode = 0700
		}
		if err := os.WriteFile(target, []byte(f.Content), mode); err != nil {
			return fmt.Errorf("write %q: %w", target, err)
		}
	}
	return nil
}

// CopyToolStateInto copies the persistent state dir for a tool into a
// subdirectory of the per-invocation sandbox. Used at dispatch time
// before the command runs. Best-effort — missing state dir is fine
// (first invocation, or stateless tool that recently added StatePath).
func CopyToolStateInto(owner, name, destStateDir string) error {
	src, err := ToolStateDir(owner, name)
	if err != nil {
		return err
	}
	return copyTree(src, destStateDir)
}

// CopyToolStateBack saves the sandbox's state subdir back to the
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

// copyTree recursively copies src into dst. dst is created (or
// reused). Symlinks are skipped — defense in depth at the FS
// boundary even though state dirs shouldn't contain them.
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
