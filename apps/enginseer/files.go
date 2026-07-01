package enginseer

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	repoFilesTable    = "f"
	maxIngestFileSize = 1 << 20 // 1 MiB — skip larger (data/generated/minified)
	binarySniff       = 8000    // bytes checked for NUL to detect binary
)

// repoFileStore returns the per-(user, repo) encrypted file store — a sub of
// RepoFilesDB, whose whole file is hardware-locked encrypted at rest.
func repoFileStore(user, repoID string) Database {
	if RepoFilesDB == nil {
		return nil
	}
	return RepoFilesDB.Sub("user:" + user).Sub("repo:" + repoID)
}

func wipeRepoFiles(user, repoID string) {
	store := repoFileStore(user, repoID)
	if store == nil {
		return
	}
	for _, k := range store.Keys(repoFilesTable) {
		store.Unset(repoFilesTable, k)
	}
}

// cloneAndIngest clones the repo into tmpfs, ingests its text files into the
// encrypted store, then discards the plaintext clone — so nothing but encrypted,
// derived content persists at rest. Best-effort; updates the record on success.
func (T *Enginseer) cloneAndIngest(user string, udb Database, repoID string) {
	rec, ok := loadRepo(udb, repoID)
	if !ok {
		return
	}
	store := repoFileStore(user, repoID)
	if store == nil {
		Log("[enginseer] RepoFilesDB not initialized; cannot ingest %s", rec.URL)
		return
	}

	// Clone into tmpfs so the plaintext working tree lives only in RAM.
	base := "/dev/shm"
	if _, err := os.Stat(base); err != nil {
		base = os.TempDir()
	}
	dir, err := os.MkdirTemp(base, "enginseer-")
	if err != nil {
		Log("[enginseer] tmp dir: %v", err)
		return
	}
	defer os.RemoveAll(dir)

	args := []string{"clone", "--depth", "1", "--single-branch"}
	if b := strings.TrimSpace(rec.Branch); b != "" {
		args = append(args, "--branch", b)
	}
	args = append(args, cloneURL(rec), dir)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0") // never block on a cred prompt
	if _, err := cmd.CombinedOutput(); err != nil {
		Log("[enginseer] clone failed for %s: %v", rec.Name, err)
		return
	}

	// Fresh ingest — replace any prior snapshot.
	wipeRepoFiles(user, repoID)
	count := 0
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() == 0 || info.Size() > maxIngestFileSize {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil || isBinary(content) {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		store.Set(repoFilesTable, filepath.ToSlash(rel), string(content))
		count++
		return nil
	})

	rec.Cloned = time.Now().Format(time.RFC3339)
	rec.Files = count
	udb.Set(repoTable, repoID, rec)
	Log("[enginseer] ingested %d files from %s", count, rec.Name)
}

// cloneURL injects the access token (if any) for private-repo HTTPS clone.
func cloneURL(rec Repo) string {
	url := strings.TrimSpace(rec.URL)
	tok := strings.TrimSpace(rec.Token)
	if tok == "" || !strings.HasPrefix(url, "https://") {
		return url
	}
	rest := strings.TrimPrefix(url, "https://")
	if rec.Provider == "gitlab" {
		return "https://oauth2:" + tok + "@" + rest
	}
	return "https://" + tok + "@" + rest // github + generic
}

func skipDir(name string) bool {
	switch name {
	case ".git", "node_modules", ".venv", "__pycache__", ".mypy_cache", ".pytest_cache":
		return true
	}
	return false
}

// isBinary treats content with a NUL byte in its first chunk as binary.
func isBinary(b []byte) bool {
	n := len(b)
	if n > binarySniff {
		n = binarySniff
	}
	return bytes.IndexByte(b[:n], 0) >= 0
}

// --- read / list / search over the encrypted store (decrypt in memory) ---

// readRepoFile returns a stored file's content.
func readRepoFile(user, repoID, path string) (string, bool) {
	store := repoFileStore(user, repoID)
	if store == nil {
		return "", false
	}
	var content string
	if !store.Get(repoFilesTable, normPath(path), &content) {
		return "", false
	}
	return content, true
}

// listRepoDir returns the immediate entries (files + "subdir/") under a prefix.
func listRepoDir(user, repoID, prefix string) []string {
	store := repoFileStore(user, repoID)
	if store == nil {
		return nil
	}
	prefix = normPath(prefix)
	if prefix != "" {
		prefix += "/"
	}
	seen := map[string]bool{}
	var out []string
	for _, path := range store.Keys(repoFilesTable) {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		rest := strings.TrimPrefix(path, prefix)
		if rest == "" {
			continue
		}
		name := rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			name = rest[:i] + "/" // a subdirectory
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

type searchHit struct {
	Path string
	Line int
	Text string
}

// searchRepo greps every stored file (case-insensitive substring) — the
// decrypt-in-memory replacement for ripgrep-on-disk.
func searchRepo(user, repoID, query string, maxHits int) []searchHit {
	store := repoFileStore(user, repoID)
	if store == nil || strings.TrimSpace(query) == "" {
		return nil
	}
	q := strings.ToLower(query)
	var hits []searchHit
	paths := store.Keys(repoFilesTable)
	sort.Strings(paths)
	for _, path := range paths {
		var content string
		if !store.Get(repoFilesTable, path, &content) {
			continue
		}
		if !strings.Contains(strings.ToLower(content), q) {
			continue
		}
		for i, line := range strings.Split(content, "\n") {
			if strings.Contains(strings.ToLower(line), q) {
				t := strings.TrimSpace(line)
				if len(t) > 240 {
					t = t[:240] + "…"
				}
				hits = append(hits, searchHit{Path: path, Line: i + 1, Text: t})
				if len(hits) >= maxHits {
					return hits
				}
			}
		}
	}
	return hits
}

func normPath(p string) string {
	return strings.Trim(filepath.ToSlash(strings.TrimSpace(p)), "/")
}
