// repo_backend.go — the Type=="repo" backend: clone a git repository into
// tmpfs, ingest its text files into the dedicated, padlock-encrypted
// RepoFilesDB, and discard the plaintext clone. Nothing but encrypted, derived
// content persists at rest. Search/read decrypt in memory. This is the repo
// target-type's equivalent of the SSH connection + exec path.
package servitor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/bundle"
	"github.com/cmcoffee/gohort/core/textutil"
)

const (
	repoFilesTable    = "repo_files"
	repoHashTable     = "repo_hashes" // path → content hash; drives diff-based re-ingest
	maxIngestFileSize = 1 << 20       // 1 MiB — skip larger (data/generated/minified)
	maxIngestFiles    = 50000         // a folder past this is not a codebase; ingest stops
	binarySniff       = 8000          // bytes checked for NUL to detect binary
)

// repoIngests counts the clones a save starts in the background. Nothing in
// the server waits on it; a test that saves a repo does, so the clone it set
// off cannot still be reading the repo store when the next test swaps it out.
var repoIngests sync.WaitGroup

// hashContent is the change key for a file's ingested content. It only has to
// distinguish "same as last ingest" from "different", so any stable digest works.
func hashContent(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

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
	for _, k := range store.Keys(repoHashTable) {
		store.Unset(repoHashTable, k)
	}
}

// cloneAndIngestRepo clones the repo appliance into tmpfs, ingests its text
// files into the encrypted store, then discards the plaintext clone. Best-effort;
// updates the appliance record's RepoFiles/RepoCloned on success.
func (T *Servitor) cloneAndIngestRepo(ctx context.Context, user string, udb Database, applianceID string) {
	var rec Appliance
	if !udb.Get(applianceTable, applianceID, &rec) || rec.Type != "repo" {
		return
	}
	store := repoFileStore(user, applianceID)
	if store == nil {
		Log("[servitor.repo] RepoFilesDB not initialized; cannot ingest %s", rec.RepoURL)
		return
	}
	// The tree to ingest: a local folder read where it is, or a clone made
	// for this ingest and removed after it.
	var dir string
	if strings.TrimSpace(rec.RepoPath) != "" {
		root, err := repoLocalRoot(rec.RepoPath, UserIsAdmin(user))
		if err != nil {
			Log("[servitor.repo] refusing to read %s: %v", rec.Name, err)
			return
		}
		dir = root
	} else {
		cloned, ok := T.cloneRepoToTmp(ctx, user, rec)
		if !ok {
			return
		}
		defer os.RemoveAll(cloned)
		dir = cloned
	}

	// Diff-based re-ingest: keep the encrypted store in sync with the fresh clone
	// by writing ONLY new/changed files and dropping ones that vanished, instead
	// of wiping and re-encrypting the whole tree every refresh. A repo where most
	// files are unchanged then costs a handful of DB writes rather than one per
	// file — which matters when RepoFilesDB lives on slow (NFS) storage. The clone
	// itself is still full; persisting a plaintext tree to `git pull` against would
	// break this backend's "nothing plaintext persists" model.
	prev := make(map[string]string, len(store.Keys(repoHashTable)))
	for _, p := range store.Keys(repoHashTable) {
		var h string
		if store.Get(repoHashTable, p, &h) {
			prev[p] = h
		}
	}

	seen := map[string]bool{}
	var count, added, changed int
	capped := false
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		// A local folder can be far bigger than a repository: stop when the
		// session is cancelled or the file cap is reached, not at the end.
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		if count >= maxIngestFiles {
			capped = true
			return filepath.SkipAll
		}
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
		// Regular files only. A repo is attacker-authored content, and a
		// symlink in it (to /etc, the gohort data dir, a key file) would
		// otherwise pass the Lstat-based size check and then be FOLLOWED by
		// ReadFile, landing a host file in the store for read_file to return.
		if !d.Type().IsRegular() {
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
		key := filepath.ToSlash(rel)
		seen[key] = true
		count++
		h := hashContent(content)
		if old, ok := prev[key]; ok && old == h {
			return nil // unchanged — skip the encrypt + write
		}
		store.Set(repoFilesTable, key, string(content))
		store.Set(repoHashTable, key, h)
		if _, existed := prev[key]; existed {
			changed++
		} else {
			added++
		}
		return nil
	})

	// A walk that stopped early has not seen the whole tree, so nothing it
	// missed may be taken for deleted.
	if ctx.Err() != nil {
		Log("[servitor.repo] ingest of %s stopped: %v", rec.Name, ctx.Err())
		return
	}
	if capped {
		Log("[servitor.repo] %s has more than %d text files; ingested the first %d (narrow it with Skip Directories)", rec.Name, maxIngestFiles, maxIngestFiles)
	}

	// Drop files that disappeared from the repo since the last ingest.
	removed := 0
	for p := range prev {
		if !seen[p] && !capped {
			store.Unset(repoFilesTable, p)
			store.Unset(repoHashTable, p)
			removed++
		}
	}

	rec.RepoCloned = time.Now().Format(time.RFC3339)
	rec.RepoFiles = count
	udb.Set(applianceTable, applianceID, rec)
	Log("[servitor.repo] ingested %s: %d files (%d new, %d changed, %d removed)",
		rec.Name, count, added, changed, removed)
}

// cloneRepoToTmp clones rec's git remote into a fresh tmpfs folder, which
// the caller removes. False when it may not be cloned or the clone failed.
func (T *Servitor) cloneRepoToTmp(ctx context.Context, user string, rec Appliance) (string, bool) {
	base := "/dev/shm"
	if _, err := os.Stat(base); err != nil {
		base = os.TempDir()
	}
	dir, err := os.MkdirTemp(base, "servitor-repo-")
	if err != nil {
		Log("[servitor.repo] tmp dir: %v", err)
		return "", false
	}

	// Checked again here, not only when the record was saved: a record from
	// before the check, an import, or a store edited some other way reaches
	// this line too, and this is the line that runs git on the server.
	if err := validateRepoSource(rec.RepoURL, rec.RepoBranch, UserIsAdmin(user)); err != nil {
		Log("[servitor.repo] refusing to clone %s: %v", rec.Name, err)
		os.RemoveAll(dir)
		return "", false
	}
	args := []string{"clone", "--depth", "1", "--single-branch"}
	if b := strings.TrimSpace(rec.RepoBranch); b != "" {
		args = append(args, "--branch", b)
	}
	// "--" so neither operand can ever be read as an option, whatever it
	// starts with.
	args = append(args, "--", repoCloneURL(rec), dir)

	// Derive the timeout from the caller's ctx so a session Cancel aborts the
	// clone mid-flight, not just the surrounding work.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	// GIT_ALLOW_PROTOCOL also binds what git reaches on its own behalf
	// (submodules, redirects), not just the URL checked above.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https:ssh")
	if _, err := cmd.CombinedOutput(); err != nil {
		Log("[servitor.repo] clone failed for %s: %v", rec.Name, err)
		os.RemoveAll(dir)
		return "", false
	}
	return dir, true
}

// scpLikeRepo is the "user@host:path" form git accepts for ssh.
var scpLikeRepo = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9][A-Za-z0-9.-]*:[A-Za-z0-9._~/-][^\s]*$`)

// repoBranchRe is what a branch name may contain here: git's ref characters,
// minus anything that could be read as an option or a revision expression.
var repoBranchRe = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._/-]*$`)

// validateRepoSource decides whether a repo appliance may be cloned from url.
//
// The clone runs git ON THE GOHORT SERVER, as the gohort process, with a URL
// any user typed. Unchecked, that was a way to run a program there ("ext::"
// transport, "--upload-pack=..." read as an option), to read the server's own
// repositories ("file://", a bare local path) into a store the user can then
// search, or to clone with the service account's ssh keys. So:
//   - https:// always; ssh:// or the scp form "git@host:path" only for an
//     admin-owned appliance, because ssh authenticates with the server's own
//     keys - the same line the ssh appliance draws for a non-admin owner.
//   - nothing that starts with "-", no whitespace or control characters, and a
//     host that could not be taken for an option by ssh.
//   - a branch made of ref characters only.
func validateRepoSource(rawURL, branch string, ownerIsAdmin bool) error {
	u := strings.TrimSpace(rawURL)
	if u == "" {
		return fmt.Errorf("repo url required")
	}
	if strings.HasPrefix(u, "-") || strings.IndexFunc(u, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
		return fmt.Errorf("repo url may not start with '-' or contain spaces or control characters")
	}
	if b := strings.TrimSpace(branch); b != "" && (!repoBranchRe.MatchString(b) || strings.Contains(b, "..")) {
		return fmt.Errorf("branch %q is not a plain branch name", b)
	}
	lower := strings.ToLower(u)
	switch {
	case strings.HasPrefix(lower, "https://"):
		p, err := url.Parse(u)
		if err != nil || p.Host == "" || strings.HasPrefix(p.Hostname(), "-") {
			return fmt.Errorf("repo url is not a valid https:// address")
		}
		return nil
	case strings.HasPrefix(lower, "ssh://"):
		p, err := url.Parse(u)
		if err != nil || p.Host == "" || strings.HasPrefix(p.Hostname(), "-") || strings.HasPrefix(p.User.Username(), "-") {
			return fmt.Errorf("repo url is not a valid ssh:// address")
		}
	case scpLikeRepo.MatchString(u):
	default:
		return fmt.Errorf("repo url must be https:// (or, for an admin, ssh:// or git@host:path); other transports such as file://, ext:: or a local path are not allowed")
	}
	if !ownerIsAdmin {
		return fmt.Errorf("an ssh repo url clones with the gohort server's own ssh keys, so only an admin-owned repo may use one: use an https:// url with an access token instead")
	}
	return nil
}

// repoLocalRoot decides whether a repo appliance may read the local folder p,
// and returns the folder to walk (symlinks resolved).
//
// The folder is on the gohort server and is read as the gohort process, so
// this is the line validateRepoSource draws against "file://" and bare local
// paths, moved for an admin: only an admin-owned appliance may name one (the
// save also requires the person saving it to be an admin). Even then it may
// not be the root of the filesystem, nor overlap gohort's own data or install
// folders: the databases, keys and configuration are not a codebase, and an
// investigation of a folder is not the place to read them.
func repoLocalRoot(p string, ownerIsAdmin bool) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("a local folder is required")
	}
	if strings.IndexFunc(p, func(r rune) bool { return r < ' ' || r == 0x7f }) >= 0 {
		return "", fmt.Errorf("the folder path may not contain control characters")
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("the folder must be an absolute path on the gohort server, such as /srv/app")
	}
	if !ownerIsAdmin {
		return "", fmt.Errorf("a local folder is read from the gohort server's own disk, so only an admin-owned repo may use one")
	}
	root, err := filepath.EvalSymlinks(filepath.Clean(p))
	if err != nil {
		return "", fmt.Errorf("the folder cannot be read: %v", err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s is not a folder", p)
	}
	if root == string(filepath.Separator) {
		return "", fmt.Errorf("the root of the filesystem is not a folder to ingest: name the project folder")
	}
	for _, guarded := range gohortOwnFolders() {
		if overlaps(root, guarded) {
			return "", fmt.Errorf("%s overlaps gohort's own folder %s, which holds its databases and configuration", p, guarded)
		}
	}
	return root, nil
}

// localRepoAdminOnly is the refusal for a save of a local-folder repo by
// someone who is not an admin.
const localRepoAdminOnly = "a local folder is read from the gohort server's own disk, so only an admin can add or change one"

// gohortOwnFolders are the folders a local repo may not overlap: the data
// folder (the workspaces folder's parent) and the folder gohort runs from.
func gohortOwnFolders() []string {
	var out []string
	add := func(d string) {
		if d == "" {
			return
		}
		if r, err := filepath.EvalSymlinks(d); err == nil {
			d = r
		}
		out = append(out, filepath.Clean(d))
	}
	if ws := WorkspacesDir(); ws != "" {
		add(filepath.Dir(ws))
	}
	if exe, err := os.Executable(); err == nil {
		add(filepath.Dir(exe))
	}
	return out
}

// overlaps reports whether folder a is b, inside b, or holds b.
func overlaps(a, b string) bool {
	within := func(x, y string) bool {
		rel, err := filepath.Rel(y, x)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	return within(a, b) || within(b, a)
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
	// Version-control metadata first, and whatever the appliance's own list
	// says: a clone's .git/config holds its token (core/bundle vcs.go).
	if bundle.VCSDir(name) || defaultSkipDirs[name] {
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

// repoSearchHit is one matching line of code, as a workspace's scout keeps it.
type repoSearchHit struct {
	Path string
	Line int
	Text string
}

// repoQuery is one search over a repo's encrypted store.
type repoQuery struct {
	Text string // the text to find; a regular expression when Regex is set
	// Patterns are further texts looked for in the same pass, each answered
	// as if searched alone (core/bundle lanes.go); literal unless Regex.
	Patterns []string
	Regex    bool
	Exclude  string // optional regular expression: a line Text matched is dropped if this matches it too
	Glob     string // optional path filter, matched as a bundle's is (bundle.MatchGlob)
	MaxHits  int    // repoMaxSearchHits when unset
	PerFile  int    // one file's share of MaxHits; 0 for none
	Context  int    // lines either side of each hit
	Count    bool   // per-file totals instead of lines
	Group    bool   // the matching lines by shape instead of listed
}

// texts is the query's texts, blanks and repeats dropped, as given.
func (q repoQuery) texts() ([]string, error) {
	texts, err := bundle.Patterns(q.Text, q.Patterns)
	if err != nil {
		return nil, fmt.Errorf("query is required")
	}
	return texts, nil
}

// bundleQuery is the query as the shared search takes it, with texts as its
// patterns: quoted when literal.
func (q repoQuery) bundleQuery(texts []string) bundle.Query {
	pats := append([]string(nil), texts...)
	if !q.Regex {
		for i, t := range pats {
			pats[i] = regexp.QuoteMeta(t)
		}
	}
	return bundle.Query{
		Pattern: pats[0], Patterns: pats[1:], Exclude: q.Exclude, Glob: q.Glob,
		MaxHits: q.MaxHits, PerFile: q.PerFile, Before: q.Context, After: q.Context,
		CountOnly: q.Count, Group: q.Group,
	}
}

// repoSource is a repo's encrypted store as a source the shared search reads
// (core/bundle source.go). A file is kept whole, so it is one slice, of a
// height no file reaches. Code carries no timestamps: no time window or
// record applies.
type repoSource struct{ store Database }

func (s repoSource) Files() []bundle.File {
	paths := s.store.Keys(repoFilesTable)
	sort.Strings(paths)
	out := make([]bundle.File, len(paths))
	for i, p := range paths {
		out[i] = bundle.File{Path: p, Key: p, Slices: 1, Format: bundle.FormatText}
	}
	return out
}

func (s repoSource) Slice(bf bundle.File, idx int) string {
	var content string
	if idx == 0 {
		s.store.Get(repoFilesTable, bf.Path, &content)
	}
	return content
}

func (s repoSource) Height() int { return 1 << 30 }

// searchRepo finds lines in a repo's ingested files.
//
// The search is core/bundle's, the one a log search uses, reading the repo's
// store through repoSource, so a query means the same thing in code as in a
// log and has the same options: case-insensitive, several texts at once, an
// exclude pattern, a path filter, context lines, a per-file share, counts and
// grouping, and a reply that says where it was cut. Literal is the default
// because it is what this searched before it shared the search, and an
// identifier with a dot or a bracket in it has to find itself. The store
// itself is the repo's own: whole files, synced by hash, and nothing under a
// skipped folder (.git, with the clone's credentials, above all) in it.
func searchRepo(ctx context.Context, user, applianceID string, q repoQuery) (bundle.SearchResult, error) {
	var res bundle.SearchResult
	store := repoFileStore(user, applianceID)
	if store == nil {
		return res, nil
	}
	texts, err := q.texts()
	if err != nil {
		return res, err
	}
	if q.MaxHits <= 0 {
		q.MaxHits = repoMaxSearchHits
	}
	res, err = bundle.SearchSource(ctx, repoSource{store}, q.bundleQuery(texts))
	if err != nil {
		return res, fmt.Errorf("query: %w", err)
	}
	// A reply names each text as it was asked for, not as it was quoted.
	for i := range res.Lanes {
		res.Lanes[i].Pattern = texts[i]
		clipRepoHits(res.Lanes[i].Hits)
	}
	clipRepoHits(res.Hits)
	return res, nil
}

// clipRepoHits bounds what a hit shows of its line: a line of minified code
// can be the whole file.
func clipRepoHits(hits []bundle.Hit) {
	for i := range hits {
		hits[i].Text = textutil.Clip(strings.TrimSpace(hits[i].Text), 240)
		for j, c := range hits[i].Context {
			hits[i].Context[j] = textutil.Clip(c, 250)
		}
	}
}

func normRepoPath(p string) string {
	return strings.Trim(filepath.ToSlash(strings.TrimSpace(p)), "/")
}
