// Git, as a subprocess.
//
// Anvil shells out to the git binary rather than linking a Go implementation.
// A real checkout carries the user's own config, hooks, credential helpers,
// worktrees and submodules, and a reimplementation that gets any of those
// subtly wrong is worse than not offering git at all — the answers would look
// right. The cost is a dependency on git being installed, which is announced
// once, clearly, instead of surfacing as a confusing empty result.
package anvil

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/core/sandbox"
	"github.com/cmcoffee/gohort/core/sourcehooks"
)

// gitTimeout bounds an ordinary git invocation. Clone gets its own, longer,
// budget — see cloneTimeout.
const gitTimeout = 60 * time.Second

// cloneTimeout bounds an initial clone. Generous because a first clone of a
// large repository over a slow link is legitimately slow, and a timeout that
// fires mid-clone leaves a half-tree that looks like a broken project.
const cloneTimeout = 30 * time.Minute

// gitAvailable reports whether the git binary is on PATH, so callers can say
// so plainly rather than reporting each git action as an obscure failure.
func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// runGit runs git in dir and returns its combined output.
func runGit(dir string, args ...string) (string, error) {
	return runGitContext(context.Background(), dir, gitTimeout, args...)
}

func runGitContext(ctx context.Context, dir string, timeout time.Duration, args ...string) (string, error) {
	if !gitAvailable() {
		return "", fmt.Errorf("git is not installed on this host, so git actions are unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// A command that reaches a remote has to run on the server, where the
	// sandbox's missing network is not in the way, and so it goes through
	// remoteGit, which runs only where the run tool cannot write.
	if gitNeedsNetwork(args) {
		return "", fmt.Errorf("git %s reaches a remote and runs only through remoteGit", args[0])
	}
	var out []byte
	var err error
	if gitWritableByRun(dir) {
		out, err = runGitSandboxed(ctx, dir, nil, args)
	} else {
		out, err = serverGit(ctx, dir, args)
	}
	return gitResult(ctx, timeout, args, out, err)
}

// serverGit runs git on the server, with gitSafety and without stopping for
// input.
func serverGit(ctx context.Context, dir string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append(append([]string(nil), gitSafety...), args...)...)
	cmd.Dir = dir
	// Never let git stop for input. Without this a repository needing
	// credentials hangs until the timeout instead of failing with something
	// the operator can read.
	cmd.Env = append(os.Environ(), gitQuietEnv...)
	return cmd.CombinedOutput()
}

// gitResult turns git's output and error into what the callers report.
func gitResult(ctx context.Context, timeout time.Duration, args []string, out []byte, err error) (string, error) {
	text := strings.TrimRight(string(out), "\n")
	if ctx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("git %s timed out after %s", args[0], timeout)
	}
	if err != nil {
		if text != "" {
			return text, fmt.Errorf("git %s failed: %s", args[0], gitFailure(text, nil))
		}
		return text, fmt.Errorf("git %s failed: %w", args[0], err)
	}
	return text, nil
}

// remoteGit is a git command that reaches a remote: a clone, or the fetch
// behind a checkout's full history.
//
// It runs on the server, because the sandbox has no network, and so it never
// runs in a repository the run tool can write. A repository's config decides
// which program git starts to talk to a remote (a remote's uploadpack, chosen
// by naming the remote in a branch's settings), and gitSafety can only pin
// the ones known by name. So a remote command runs in a folder the server
// made for it: an empty one a clone fills, or a fresh copy fetched from the
// stored address, which the checkout then fetches from inside the sandbox.
type remoteGit struct {
	dir     string
	admin   bool // an administrator's: any address git takes; anyone else's: https only
	pin     []string
	timeout time.Duration
}

func (g remoteGit) run(ctx context.Context, args ...string) (string, error) {
	if !gitAvailable() {
		return "", fmt.Errorf("git is not installed on this host, so git actions are unavailable")
	}
	if gitWritableByRun(g.dir) {
		return "", fmt.Errorf("git %s is not run in a repository the run tool can write", args[0])
	}
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	out, err := serverGit(ctx, g.dir, append(append(transportFor(g.admin), g.pin...), args...))
	return gitResult(ctx, g.timeout, args, out, err)
}

// transportFor is the settings limiting which ways git may reach a remote: for
// anyone but an administrator, https alone. A local path, file:// or ssh
// reaches this server's own files and its keys rather than a remote.
func transportFor(admin bool) []string {
	if admin {
		return nil
	}
	return []string{"-c", "protocol.allow=never", "-c", "protocol.https.allow=always"}
}

// checkRepoURL refuses a repository address that is not a remote one. For
// anyone but an administrator that is everything but https: a plain path or a
// file:// address clones any repository this server can read, and an ssh one
// signs in with this server's keys. An address starting with "-" is refused
// for everyone, since git would read it as an option.
func checkRepoURL(raw string, admin bool) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("this project has no repository URL")
	}
	if strings.HasPrefix(raw, "-") {
		return fmt.Errorf("a repository URL cannot start with \"-\"")
	}
	if admin {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fmt.Errorf("a repository URL has to be an https address, such as https://host/owner/repo.git, with any token in the token field. " +
			"A local path, file:// or ssh address reaches this server's own files or keys, so only an administrator may use one")
	}
	return nil
}

// lookupHost resolves a repository host; a test swaps it.
var lookupHost = net.DefaultResolver.LookupIPAddr

// remotePin is the settings that hold a non-administrator's clone to a public
// address. An https address can still name a host on this server's own
// network, and git's first error line would tell the user what answered
// there. So the host is resolved once and refused when any address it has is
// private, loopback, link-local or otherwise internal; git is then pinned to
// the address that was checked, so a second lookup cannot answer differently,
// and follows no redirect, which could send it anywhere. An administrator's
// address is theirs to choose: nil.
func remotePin(ctx context.Context, raw string, admin bool) ([]string, error) {
	if admin {
		return nil, nil
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("the repository URL is not parseable")
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = "443"
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		addrs, err := lookupHost(lctx, host)
		if err != nil || len(addrs) == 0 {
			return nil, fmt.Errorf("the repository host %s could not be found", host)
		}
		for _, a := range addrs {
			ips = append(ips, a.IP)
		}
	}
	for _, ip := range ips {
		if sourcehooks.NonPublicIP(ip) {
			return nil, fmt.Errorf("the repository host %s is on a private or internal network, and only an administrator may clone from one", host)
		}
	}
	addr := ips[0].String()
	if ips[0].To4() == nil {
		addr = "[" + addr + "]"
	}
	return []string{"-c", "http.curloptResolve=" + host + ":" + port + ":" + addr, "-c", "http.followRedirects=false"}, nil
}

// checkBranch refuses a branch name git would read as an option.
func checkBranch(b string) error {
	if strings.HasPrefix(strings.TrimSpace(b), "-") {
		return fmt.Errorf("a branch name cannot start with \"-\"")
	}
	return nil
}

// A repository's own config can make git run programs: core.fsmonitor on
// every status, a filter driver on every changed file, a diff driver's
// textconv, hooks. Anvil's run tool writes the project folder, so when the
// repository is that folder its .git is the run tool's to write too, and git
// run on the server would have run whatever was planted there, as gohort,
// outside any sandbox, on the next chat turn. So:
//
//   - git in a folder whose .git the run tool can write runs in the same
//     sandbox as run (runGitSandboxed): anything the config can make it do,
//     the run tool could already do;
//   - every git on the server is given gitSafety, which turns off the knobs
//     that are known by name; a filter or diff driver has a name of the
//     repository's choosing, which is why the first rule exists;
//   - the file tools refuse paths under .git (resolvePath);
//   - git that reaches a remote runs only through remoteGit, never in a
//     repository the run tool can write.

// gitSafety settles, on every git run on the server, each setting that would
// otherwise run a program a repository's config named.
var gitSafety = []string{
	"-c", "core.fsmonitor=false",
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.pager=cat",
	"-c", "core.editor=true",
	"-c", "sequence.editor=true",
	"-c", "core.sshCommand=ssh",
	"-c", "core.gitProxy=",
	"-c", "credential.helper=",
	"-c", "protocol.ext.allow=never",
	"-c", "remote.origin.uploadpack=git-upload-pack",
	"-c", "remote.origin.receivepack=git-receive-pack",
}

// gitQuietEnv keeps git from stopping for input.
var gitQuietEnv = []string{"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "GCM_INTERACTIVE=never"}

// gitWritableByRun reports whether dir holds a .git the run tool can write: a
// folder, or a file pointing git somewhere else, both of which a command in
// the project folder can make or change.
func gitWritableByRun(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

// gitNeedsNetwork reports a git command that has to reach a remote, which the
// sandbox does not offer. Those run through remoteGit.
func gitNeedsNetwork(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "clone", "fetch", "pull", "push", "ls-remote":
		return true
	}
	return false
}

// runGitSandboxed runs git in dir inside the run tool's sandbox; reach are
// other folders it may read.
func runGitSandboxed(ctx context.Context, dir string, reach []string, args []string) ([]byte, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	var cmd strings.Builder
	cmd.WriteString("git")
	for _, a := range append(append([]string(nil), gitSafety...), args...) {
		cmd.WriteString(" '")
		cmd.WriteString(strings.ReplaceAll(a, "'", `'\''`))
		cmd.WriteString("'")
	}
	env := map[string]string{}
	for _, kv := range gitQuietEnv {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	res := sandbox.RunSandboxedShellIn(ctx, sandbox.ShellRun{Command: cmd.String(), WorkspaceDir: abs, Env: env, Reach: reach})
	return []byte(res.Output), res.Err
}

// isGitRepo reports whether dir is inside a git working tree.
func isGitRepo(dir string) bool {
	out, err := runGit(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

// cloneProject checks out a clone project into its base directory.
//
// The token, when present, is injected into the URL for the duration of the
// clone and then removed from the stored remote, so the credential does not
// end up sitting in .git/config where anything reading the checkout can find
// it. That is the same reasoning as blanking it on read: a token is worth
// exactly as much as the places it is written down.
//
// admin is whether the project's owner is an administrator, which decides
// which addresses it may clone from (checkRepoURL).
func cloneProject(ctx context.Context, p Project, base string, admin bool) (string, error) {
	repoURL := strings.TrimSpace(p.RepoURL)
	if err := checkRepoURL(repoURL, admin); err != nil {
		return "", err
	}
	if err := checkBranch(p.Branch); err != nil {
		return "", err
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", fmt.Errorf("create checkout directory: %w", err)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", err
	}
	if len(entries) > 0 {
		return "", fmt.Errorf("the checkout directory is not empty; delete the project and re-add it to re-clone")
	}

	pin, err := remotePin(ctx, repoURL, admin)
	if err != nil {
		return "", err
	}
	authURL, err := urlWithToken(repoURL, p.Token)
	if err != nil {
		return "", err
	}
	// Shallow and single-branch by default. A first clone of a large
	// repository is the slowest thing Anvil does, and the overwhelmingly
	// common case — read this code, change it, build it — needs none of the
	// history. FullHistory is the opt-out, and it exists because two of the
	// git tool's actions genuinely require the history to mean anything: see
	// shallowHistoryNote.
	args := []string{"clone"}
	if !p.FullHistory {
		args = append(args, "--depth", "1")
	}
	if b := strings.TrimSpace(p.Branch); b != "" {
		args = append(args, "--branch", b)
	}
	args = append(args, "--single-branch", "--", authURL, base)
	out, err := remoteGit{dir: filepath.Dir(base), admin: admin, pin: pin, timeout: cloneTimeout}.run(ctx, args...)
	if err != nil {
		// Never let a token reach a log line or an error card.
		return "", fmt.Errorf("%s", scrubToken(err.Error(), p.Token))
	}
	if p.Token != "" {
		// Put the clean URL back so the credential isn't persisted in the
		// checkout's own config.
		if _, err := runGit(base, "remote", "set-url", "origin", repoURL); err != nil {
			return "", fmt.Errorf("clone succeeded but the remote URL could not be cleaned of its token; remove the checkout and retry: %w", err)
		}
	}
	return scrubToken(out, p.Token), nil
}

// urlWithToken embeds a token into an https git URL as its userinfo.
func urlWithToken(raw, token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("repository URL is not parseable: %w", err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("a token can only be used with an https URL; %q is %q", raw, u.Scheme)
	}
	u.User = url.UserPassword("x-access-token", token)
	return u.String(), nil
}

// scrubToken removes a token from text that may be shown or logged.
func scrubToken(s, token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "***")
}

// gitFailure is what a failed git command said went wrong: its fatal: and
// error: lines, else the last line it printed, else err. Git starts with
// what it is doing ("Cloning into '.'...") and ends with why it stopped, so
// the first line of a failure is the one that says nothing: an unpack that
// failed on a Mac was reported, and logged, as "Cloning into '.'...".
func gitFailure(out string, err error) string {
	var why, last []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		last = append(last[:0], l)
		if strings.HasPrefix(l, "fatal:") || strings.HasPrefix(l, "error:") {
			why = append(why, l)
		}
	}
	switch {
	case len(why) > 0:
		if len(why) > 3 {
			why = why[len(why)-3:]
		}
		return strings.Join(why, " ")
	case len(last) > 0 && err != nil:
		return last[0] + " (" + err.Error() + ")"
	case len(last) > 0:
		return last[0]
	case err != nil:
		return err.Error()
	}
	return "git gave no reason"
}

// --- what git knows about a directory ----------------------------------------

// RepoInfo is what git says about the directory a project points at.
//
// Reported for BOTH project kinds. A registered directory on this host is very
// often a working checkout — that is the whole point of one — so treating
// git as a property of clone projects only would leave the most interesting
// projects the least informed. Nothing here assumes the project directory is
// the repository ROOT: a project can legitimately be one package inside a
// larger tree, which is why Prefix exists and why the status query is scoped.
type RepoInfo struct {
	IsRepo bool   `json:"is_repo"`
	Root   string `json:"root,omitempty"`   // absolute repository root
	Prefix string `json:"prefix,omitempty"` // project dir relative to Root; "" at the root
	Branch string `json:"branch,omitempty"` // "" when the head is detached
	Head   string `json:"head,omitempty"`   // short commit, for the detached case
	Dirty  bool   `json:"dirty"`            // uncommitted changes WITHIN the project dir
	// Changed is how many paths under the project dir differ from HEAD, so a
	// caller can say "3 changes" rather than only "dirty".
	Changed int `json:"changed"`
}

// inspectRepo asks git about base. Every field is best-effort: a directory
// that is not a repository, or a host with no git, yields IsRepo false rather
// than an error, because "this project is not under version control" is a
// normal state and not a failure to report.
func inspectRepo(base string) RepoInfo {
	var info RepoInfo
	if !gitAvailable() {
		return info
	}
	// Two git runs, not one per fact: for a checkout each run starts a
	// sandbox (about 45ms), and the home page asks this for every project.
	out, err := runGit(base, "rev-parse", "--is-inside-work-tree", "--show-toplevel", "--show-prefix")
	if err != nil {
		return info
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if strings.TrimSpace(lines[0]) != "true" {
		return info
	}
	info.IsRepo = true
	if len(lines) > 1 {
		info.Root = strings.TrimSpace(lines[1])
	}
	if len(lines) > 2 {
		info.Prefix = strings.TrimSuffix(strings.TrimSpace(lines[2]), "/")
	}
	// Scoped to the project directory with `-- .`. Unscoped, git reports the
	// whole repository, so a project pointing at one package would call itself
	// dirty because of an edit three directories away — true of the repo,
	// misleading about the thing on screen. --branch puts the branch on the
	// first line.
	if st, err := runGit(base, "status", "--porcelain", "--branch", "--", "."); err == nil {
		for _, line := range strings.Split(st, "\n") {
			if strings.HasPrefix(line, "## ") {
				info.Branch = branchOfStatus(line[3:])
				continue
			}
			if strings.TrimSpace(line) != "" {
				info.Changed++
			}
		}
		info.Dirty = info.Changed > 0
	}
	if info.Branch == "" {
		// Detached head: the branch name is empty and the commit is the only
		// honest thing to show. Saying "no branch" would read as an error.
		if head, err := runGit(base, "rev-parse", "--short", "HEAD"); err == nil {
			info.Head = strings.TrimSpace(head)
		}
	}
	return info
}

// branchOfStatus is the branch named by the "## " line of git status
// --branch: "main...origin/main [ahead 1]", "main", "No commits yet on main"
// (older git: "Initial commit on main"); "" for a detached head.
func branchOfStatus(s string) string {
	s = strings.TrimSpace(s)
	for _, unborn := range []string{"No commits yet on ", "Initial commit on "} {
		if strings.HasPrefix(s, unborn) {
			return strings.TrimSpace(strings.TrimPrefix(s, unborn))
		}
	}
	if strings.HasPrefix(s, "HEAD (no branch)") {
		return ""
	}
	if i := strings.Index(s, "..."); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, ' '); i >= 0 {
		s = s[:i]
	}
	return s
}

// ignoredEntries asks git which paths under base it would ignore.
//
// Asking git rather than parsing .gitignore, for the same reason the rest of
// this file shells out: the real rules are nested files, negation, anchoring,
// directory-only patterns, .git/info/exclude, and a global excludesFile, and a
// reimplementation that gets any of it subtly wrong is worse than none —
// it would hide files the user can see in their editor, or show generated ones
// while claiming to respect their ignores.
//
// `--directory` collapses a wholly-ignored directory to a single entry, so the
// result stays small (19 entries on gohort itself) and the walk can skip an
// entire node_modules without enumerating it.
//
// Paths come back relative to base, which is what the walk compares against.
// Returns nothing when base is not a repository — the caller then falls back
// to its built-in skip list.
func ignoredEntries(base string) (files map[string]bool, dirs []string) {
	files = map[string]bool{}
	if !gitAvailable() || !isGitRepo(base) {
		return files, nil
	}
	out, err := runGit(base, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		return files, nil
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasSuffix(line, "/") {
			dirs = append(dirs, strings.TrimSuffix(line, "/"))
			continue
		}
		files[line] = true
	}
	return files, dirs
}

// isShallowRepo reports whether base was cloned with a truncated history.
func isShallowRepo(base string) bool {
	out, err := runGit(base, "rev-parse", "--is-shallow-repository")
	return err == nil && strings.TrimSpace(out) == "true"
}

// shallowHistoryNote is what a history-dependent answer says instead of
// pretending.
//
// The two actions differ in HOW they fail, which is why only one of them is
// refused outright. `log` on a shallow clone is short but true: one commit,
// correctly described. `blame` is neither — every line comes back attributed
// to the single boundary commit, with that commit's author and date, for code
// written by other people years apart. It does not error, it answers wrongly,
// and an answer about why code looks the way it does is exactly the kind a
// reader has no way to check.
const shallowHistoryNote = "This project was cloned without history (shallow), so there is almost nothing to go on. " +
	"Fetch the full history from Projects if the history is what you need."

// deepenRepo turns a shallow checkout into a complete one.
//
// The fetch is not run in the checkout: its .git is the run tool's to write,
// and a fetch run there on the server would start whatever program its config
// names for the remote (remoteGit). So the history is fetched into a fresh
// copy the server makes outside the checkout, from the stored address, and the
// checkout then fetches from that copy inside the sandbox. The token goes only
// into the fresh copy, which is removed afterwards.
func deepenRepo(ctx context.Context, p Project, base string, admin bool) (string, error) {
	if !isGitRepo(base) {
		return "", fmt.Errorf("%s is not a git repository", p.Name)
	}
	if !isShallowRepo(base) {
		return "already has its full history", nil
	}
	repoURL := strings.TrimSpace(p.RepoURL)
	if err := checkRepoURL(repoURL, admin); err != nil {
		return "", err
	}
	if err := checkBranch(p.Branch); err != nil {
		return "", err
	}
	pin, err := remotePin(ctx, repoURL, admin)
	if err != nil {
		return "", err
	}
	authURL, err := urlWithToken(repoURL, p.Token)
	if err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp("", "anvil-history-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	history := filepath.Join(tmp, "history.git")
	args := []string{"clone", "--bare", "--single-branch"}
	if b := strings.TrimSpace(p.Branch); b != "" {
		args = append(args, "--branch", b)
	}
	if _, err := (remoteGit{dir: tmp, admin: admin, pin: pin, timeout: cloneTimeout}).run(ctx, append(args, "--", authURL, history)...); err != nil {
		return "", fmt.Errorf("%s", scrubToken(err.Error(), p.Token))
	}
	// The copy's remote holds the token; the checkout's fetch reads the copy.
	if _, err := runGit(history, "remote", "remove", "origin"); err != nil {
		return "", fmt.Errorf("could not clear the token from the fetched history: %w", err)
	}
	fctx, cancel := context.WithTimeout(ctx, cloneTimeout)
	defer cancel()
	fargs := []string{"fetch", "--unshallow", "--", history}
	out, err := runGitSandboxed(fctx, base, []string{tmp}, fargs)
	text, err := gitResult(fctx, cloneTimeout, fargs, out, err)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text) + " (full history fetched)", nil
}

// --- the working tree --------------------------------------------------------

// WorkingChange is one path git reports as differing from HEAD.
type WorkingChange struct {
	Path string `json:"path"`
	// Code is git's two-letter porcelain status ("??", " M", "A ", ...), kept
	// raw so the caller can render whatever vocabulary suits it.
	Code string `json:"code"`
	// State is that code said in words, because "R " means nothing to anyone
	// reading it for the first time.
	State     string `json:"state"`
	Untracked bool   `json:"untracked,omitempty"`
	Staged    bool   `json:"staged,omitempty"`
}

// statusWords turns a porcelain code into something readable. The two columns
// are index and work-tree respectively; we report the one that is set, and say
// "staged" separately rather than folding it into the word.
func statusWords(code string) (state string, staged bool) {
	if len(code) < 2 {
		return "changed", false
	}
	index, tree := code[0], code[1]
	staged = index != ' ' && index != '?'
	letter := tree
	if letter == ' ' {
		letter = index
	}
	switch letter {
	case 'M':
		return "modified", staged
	case 'A':
		return "added", staged
	case 'D':
		return "deleted", staged
	case 'R':
		return "renamed", staged
	case 'C':
		return "copied", staged
	case '?':
		return "untracked", false
	case 'U':
		return "conflicted", staged
	default:
		return "changed", staged
	}
}

// workingChanges lists what differs from HEAD under base.
//
// Scoped with `-- .` for the same reason inspectRepo is: a project pointing at
// one package should not report edits from elsewhere in the repository as its
// own. Untracked files are included — a file the agent just created is a
// change by every meaning of the word except git's narrow one, and leaving it
// out is how "I added the file" and "no changes" end up on screen together.
func workingChanges(base string) ([]WorkingChange, error) {
	if !isGitRepo(base) {
		return nil, fmt.Errorf("not a git repository")
	}
	out, err := runGit(base, "status", "--porcelain", "--untracked-files=all", "--", ".")
	if err != nil {
		return nil, err
	}
	var changes []WorkingChange
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		code := line[:2]
		path := strings.TrimSpace(line[3:])
		// A rename reads "old -> new"; the new name is the one to act on.
		if i := strings.Index(path, " -> "); i >= 0 {
			path = path[i+4:]
		}
		path = strings.Trim(path, "\"")
		if path == "" {
			continue
		}
		state, staged := statusWords(code)
		changes = append(changes, WorkingChange{
			Path: path, Code: code, State: state,
			Untracked: strings.HasPrefix(code, "??"), Staged: staged,
		})
	}
	return changes, nil
}

// workingDiff returns a unified diff for one path in the working tree. abs is
// that path as the project's resolvePath answered it, which is what an
// untracked file is read from: joining rel to base again would follow a
// symlink the run tool planted in the checkout out of the tree.
//
// An untracked file has nothing to diff against, and git says so by printing
// nothing, which renders as "no changes" for a file that is entirely new.
// So it is diffed against emptiness here instead, using the same renderer the
// edit journal uses, and comes back as the all-additions patch it actually is.
func workingDiff(base, rel, abs string, untracked bool) (string, error) {
	if untracked {
		b, err := os.ReadFile(abs)
		if err != nil {
			return "", err
		}
		if isBinary(b) {
			return "", fmt.Errorf("%s is a binary file", rel)
		}
		diff, _, _ := unifiedDiff(rel, "", string(b))
		return diff, nil
	}
	// Both halves: a partially staged file differs from HEAD in two places,
	// and showing only the unstaged half would hide the rest of the change.
	staged, _ := runGit(base, "diff", "--no-ext-diff", "--no-textconv", "--cached", "--", rel)
	unstaged, err := runGit(base, "diff", "--no-ext-diff", "--no-textconv", "--", rel)
	if err != nil && strings.TrimSpace(staged) == "" {
		return "", err
	}
	var parts []string
	if s := strings.TrimSpace(staged); s != "" {
		parts = append(parts, "# staged\n"+s)
	}
	if u := strings.TrimSpace(unstaged); u != "" {
		parts = append(parts, "# not staged\n"+u)
	}
	if len(parts) == 0 {
		return "", nil
	}
	return strings.Join(parts, "\n"), nil
}
