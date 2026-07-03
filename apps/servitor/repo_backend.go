// repo_backend.go — the Type=="repo" backend: clone a git repository into
// tmpfs, ingest its text files into the dedicated, hardware-locked-encrypted
// RepoFilesDB, and discard the plaintext clone. Nothing but encrypted, derived
// content persists at rest. Search/read decrypt in memory. This is the repo
// target-type's equivalent of the SSH connection + exec path.
package servitor

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
	repoFilesTable    = "repo_files"
	maxIngestFileSize = 1 << 20 // 1 MiB — skip larger (data/generated/minified)
	binarySniff       = 8000    // bytes checked for NUL to detect binary
)

// repoFileStore returns the per-(user, appliance) encrypted code store.
func repoFileStore(user, applianceID string) Database {
	if RepoFilesDB == nil {
		return nil
	}
	return RepoFilesDB.Sub("user:" + user).Sub("repo:" + applianceID)
}

// repoFileCount reports how many files are ingested in the store — used to gate
// a session on "the clone has finished" without reading any file content.
func repoFileCount(user, applianceID string) int {
	store := repoFileStore(user, applianceID)
	if store == nil {
		return 0
	}
	return len(store.Keys(repoFilesTable))
}

func wipeRepoFiles(user, applianceID string) {
	store := repoFileStore(user, applianceID)
	if store == nil {
		return
	}
	for _, k := range store.Keys(repoFilesTable) {
		store.Unset(repoFilesTable, k)
	}
}

// cloneAndIngestRepo clones the repo appliance into tmpfs, ingests its text
// files into the encrypted store, then discards the plaintext clone. Best-effort;
// updates the appliance record's RepoFiles/RepoCloned on success.
func (T *Servitor) cloneAndIngestRepo(user string, udb Database, applianceID string) {
	var rec Appliance
	if !udb.Get(applianceTable, applianceID, &rec) || rec.Type != "repo" {
		return
	}
	store := repoFileStore(user, applianceID)
	if store == nil {
		Log("[servitor.repo] RepoFilesDB not initialized; cannot ingest %s", rec.RepoURL)
		return
	}
	base := "/dev/shm"
	if _, err := os.Stat(base); err != nil {
		base = os.TempDir()
	}
	dir, err := os.MkdirTemp(base, "servitor-repo-")
	if err != nil {
		Log("[servitor.repo] tmp dir: %v", err)
		return
	}
	defer os.RemoveAll(dir)

	args := []string{"clone", "--depth", "1", "--single-branch"}
	if b := strings.TrimSpace(rec.RepoBranch); b != "" {
		args = append(args, "--branch", b)
	}
	args = append(args, repoCloneURL(rec), dir)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if _, err := cmd.CombinedOutput(); err != nil {
		Log("[servitor.repo] clone failed for %s: %v", rec.Name, err)
		return
	}

	wipeRepoFiles(user, applianceID)
	count := 0
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Don't abort the whole walk on one bad entry, but log it — a silent
			// skip (e.g. a permission-denied file) otherwise undercounts the repo
			// with no trace.
			Log("[servitor.repo] skip %q: %v", path, err)
			return nil
		}
		if d.IsDir() {
			if skipRepoDir(d.Name(), rec.RepoSkipDirs) {
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

	rec.RepoCloned = time.Now().Format(time.RFC3339)
	rec.RepoFiles = count
	udb.Set(applianceTable, applianceID, rec)
	Log("[servitor.repo] ingested %d files from %s", count, rec.Name)
}

// repoCloneURL injects the access token (if any) for private-repo HTTPS clone.
func repoCloneURL(rec Appliance) string {
	url := strings.TrimSpace(rec.RepoURL)
	tok := strings.TrimSpace(rec.RepoToken)
	if tok == "" || !strings.HasPrefix(url, "https://") {
		return url
	}
	rest := strings.TrimPrefix(url, "https://")
	if strings.Contains(url, "gitlab.") {
		return "https://oauth2:" + tok + "@" + rest
	}
	return "https://" + tok + "@" + rest // github + generic
}

// defaultSkipDirs are directory basenames never worth ingesting: VCS metadata,
// dependency caches, virtualenvs, and common build/output trees. Extended per
// appliance via Appliance.RepoSkipDirs.
var defaultSkipDirs = map[string]bool{
	".git": true, "node_modules": true, ".venv": true, "venv": true,
	"__pycache__": true, ".mypy_cache": true, ".pytest_cache": true,
	".idea": true, ".vscode": true, ".gradle": true,
	"dist": true, "build": true, "target": true, "out": true,
	".next": true, ".nuxt": true, "vendor": true, "coverage": true,
}

func skipRepoDir(name string, extra []string) bool {
	if defaultSkipDirs[name] {
		return true
	}
	for _, e := range extra {
		if name == strings.TrimSpace(e) {
			return true
		}
	}
	return false
}

func isBinary(b []byte) bool {
	n := len(b)
	if n > binarySniff {
		n = binarySniff
	}
	return bytes.IndexByte(b[:n], 0) >= 0
}

func repoNameFromURL(url string) string {
	s := strings.TrimSuffix(strings.TrimSpace(url), ".git")
	s = strings.TrimSuffix(s, "/")
	parts := strings.Split(s, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	if len(parts) >= 1 {
		return parts[len(parts)-1]
	}
	return url
}

// --- read / list / search over the encrypted store (decrypt in memory) ---

func readRepoFile(user, applianceID, path string) (string, bool) {
	store := repoFileStore(user, applianceID)
	if store == nil {
		return "", false
	}
	var content string
	if !store.Get(repoFilesTable, normRepoPath(path), &content) {
		return "", false
	}
	return content, true
}

func listRepoDir(user, applianceID, prefix string) []string {
	store := repoFileStore(user, applianceID)
	if store == nil {
		return nil
	}
	prefix = normRepoPath(prefix)
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
			name = rest[:i] + "/"
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

type repoSearchHit struct {
	Path string
	Line int
	Text string
}

func searchRepo(user, applianceID, query string, maxHits int) []repoSearchHit {
	store := repoFileStore(user, applianceID)
	if store == nil || strings.TrimSpace(query) == "" {
		return nil
	}
	q := strings.ToLower(query)
	var hits []repoSearchHit
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
				hits = append(hits, repoSearchHit{Path: path, Line: i + 1, Text: t})
				if len(hits) >= maxHits {
					return hits
				}
			}
		}
	}
	return hits
}

func normRepoPath(p string) string {
	return strings.Trim(filepath.ToSlash(strings.TrimSpace(p)), "/")
}
