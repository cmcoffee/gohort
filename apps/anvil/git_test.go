package anvil

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/sandbox"
)

// TestMain keeps the package's temporary folders out of /tmp. Git in a
// project whose .git the run tool can write runs in that tool's sandbox, which
// mounts a fresh /tmp over the host's, so a project under /tmp is not there to
// run in. Projects live in the gohort data directory; the tests' did not.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("/var/tmp", "anvil-test-")
	if err == nil {
		os.Setenv("TMPDIR", dir)
	}
	code := m.Run()
	if err == nil {
		os.RemoveAll(dir)
	}
	os.Exit(code)
}

// planted is a repository with a setting in its own config that runs a
// command, and the file that command would leave outside the project.
func planted(t *testing.T, setting ...string) (dir, marker string) {
	t.Helper()
	base := t.TempDir()
	dir, marker = filepath.Join(base, "project"), filepath.Join(base, "pwned")
	os.MkdirAll(dir, 0o700)
	initRepo(t, dir)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o600)
	commitAll(t, dir, "first")
	for i := 0; i+1 < len(setting); i += 2 {
		if _, err := runGit(dir, "config", setting[i], setting[i+1]); err != nil {
			t.Fatal(err)
		}
	}
	return dir, marker
}

// A program named in a repository's config by a known setting is never run
// by git on the server: fsmonitor runs on every status, and Anvil runs status
// on every chat turn.
func TestARepositorysFsmonitorIsNeverRun(t *testing.T) {
	dir, marker := planted(t)
	if _, err := runGit(dir, "config", "core.fsmonitor", "sh -c 'touch "+marker+"'"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o600)
	runGit(dir, "status", "--short")
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("git ran the repository's fsmonitor command")
	}
}

// A filter driver has a name of the repository's choosing, so no setting can
// turn it off by name: git in a folder whose .git the run tool can write runs
// in the run tool's sandbox, where a planted command reaches nothing outside
// the project.
func TestARepositorysFilterRunsNoWhereButTheSandbox(t *testing.T) {
	if !sandbox.ScopesReads() {
		t.Skip("no confining sandbox on this host")
	}
	dir, marker := planted(t, "filter.evil.clean", "")
	if _, err := runGit(dir, "config", "filter.evil.clean", "sh -c 'touch "+marker+"; cat'"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("* filter=evil\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("three\n"), 0o600)
	runGit(dir, "status", "--short")
	runGit(dir, "diff", "--no-ext-diff", "--no-textconv")
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a repository's filter command ran outside the sandbox")
	}
}

// The file tools never reach into .git.
func TestTheFileToolsStayOutOfGit(t *testing.T) {
	base := t.TempDir()
	p := Project{}
	for _, rel := range []string{".git/config", ".GIT/hooks/pre-commit", "sub/.git/config", ".git"} {
		if _, err := p.resolvePath(base, rel); err == nil {
			t.Errorf("%s was allowed", rel)
		}
	}
	if _, err := p.resolvePath(base, "src/main.go"); err != nil {
		t.Errorf("an ordinary path: %v", err)
	}
}

// shallowCheckout is an origin with three commits and a shallow checkout of
// it, made as an administrator's clone (a local origin is an administrator's
// to name).
func shallowCheckout(t *testing.T) (origin, dir string, p Project) {
	t.Helper()
	if !gitAvailable() {
		t.Skip("git is not installed")
	}
	origin = t.TempDir()
	initRepo(t, origin)
	for i, body := range []string{"one\n", "two\n", "three\n"} {
		mustWrite(t, filepath.Join(origin, "f.txt"), body)
		commitAll(t, origin, fmt.Sprintf("c%d", i))
	}
	dir = filepath.Join(t.TempDir(), "c")
	p = Project{ID: "s", Name: "shallow", Kind: KindClone, Owner: "u", RepoURL: "file://" + origin}
	if _, err := cloneProject(t.Context(), p, dir, true); err != nil {
		t.Fatalf("clone: %v", err)
	}
	return origin, dir, p
}

// Fetching a checkout's history never runs a program the checkout's own config
// names: a remote's uploadpack, chosen by pointing the current branch at that
// remote, is a setting the run tool can write and gitSafety cannot pin by
// name. The history still arrives. The fresh copy lives under /tmp, which the
// sandbox mounts over with its own, so this also proves the copy is reachable.
func TestFetchingTheHistoryRunsNothingTheCheckoutNamed(t *testing.T) {
	origin, dir, p := shallowCheckout(t)
	marker := filepath.Join(filepath.Dir(dir), "pwned")
	branch, err := runGit(dir, "branch", "--show-current")
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][2]string{
		{"remote.evil.url", origin},
		{"remote.evil.uploadpack", "sh -c 'touch " + marker + "; git-upload-pack \"$@\"' x"},
		{"branch." + strings.TrimSpace(branch) + ".remote", "evil"},
	} {
		if _, err := runGit(dir, "config", kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat("/tmp"); err == nil {
		t.Setenv("TMPDIR", "/tmp")
	}
	if _, err := deepenRepo(t.Context(), p, dir, true); err != nil {
		t.Fatalf("deepen: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("fetching the history ran the program the checkout's config named")
	}
	if isShallowRepo(dir) {
		t.Fatal("the history did not arrive")
	}
}

// Git that reaches a remote is never run in a repository the run tool can
// write, whatever path asks for it.
func TestARemoteGitIsRefusedWhereTheRunToolWrites(t *testing.T) {
	_, dir, _ := shallowCheckout(t)
	if _, err := runGitContext(context.Background(), dir, gitTimeout, "fetch"); err == nil || !strings.Contains(err.Error(), "only through remoteGit") {
		t.Errorf("runGit ran a fetch, or failed for another reason: %v", err)
	}
	if _, err := (remoteGit{dir: dir, admin: true, timeout: gitTimeout}).run(context.Background(), "fetch"); err == nil || !strings.Contains(err.Error(), "run tool can write") {
		t.Errorf("remoteGit ran a fetch in a checkout the run tool writes, or failed for another reason: %v", err)
	}
}

// Anyone but an administrator clones only from https: a path or file:// clones
// any repository this server can read, and ssh signs in with its keys. Nobody
// names an address git would read as an option.
func TestARepositoryAddressMustBeRemote(t *testing.T) {
	for _, raw := range []string{"/srv/repo", "file:///srv/repo", "git@host.example:o/r.git", "ssh://host.example/r",
		"http://host.example/r", "https://user:pw@host.example/r", "https:///r", "--upload-pack=x", "repo"} {
		if checkRepoURL(raw, false) == nil {
			t.Errorf("%q was allowed for a user", raw)
		}
	}
	if err := checkRepoURL("https://host.example/o/r.git", false); err != nil {
		t.Errorf("an https address: %v", err)
	}
	if err := checkRepoURL("/srv/repo", true); err != nil {
		t.Errorf("an administrator's local path: %v", err)
	}
	if checkRepoURL("-c", true) == nil || checkBranch("--upload-pack=x") == nil {
		t.Error("an address or branch read as an option was allowed")
	}
}

// The clone itself holds to it, and so does git: a user's clone of a local
// repository is refused before anything is copied, and the transports a user's
// clone may use are https alone even if an address got past the check.
func TestAUsersCloneOfALocalRepositoryIsRefused(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git is not installed")
	}
	origin := t.TempDir()
	initRepo(t, origin)
	mustWrite(t, filepath.Join(origin, "secret.txt"), "x\n")
	commitAll(t, origin, "c")
	for _, raw := range []string{origin, "file://" + origin} {
		dir := filepath.Join(t.TempDir(), "c")
		p := Project{ID: "s", Name: "s", Kind: KindClone, Owner: "u", RepoURL: raw}
		if _, err := cloneProject(t.Context(), p, dir, false); err == nil {
			t.Errorf("a user cloned %s", raw)
		}
		if _, err := os.Stat(filepath.Join(dir, "secret.txt")); err == nil {
			t.Errorf("%s was copied", raw)
		}
		into := filepath.Join(t.TempDir(), "d")
		if _, err := (remoteGit{dir: filepath.Dir(into), timeout: gitTimeout}).run(t.Context(), "clone", "--", raw, into); err == nil {
			t.Errorf("git let a user's clone use %s", raw)
		}
	}
}

// A user's https address must name a public host. One on this server's own
// network is refused before git runs, and a public one is pinned to the
// address that was checked, with no redirects, so neither a second lookup nor
// the server can send git somewhere internal. An administrator's is theirs.
func TestAUsersCloneReachesOnlyAPublicHost(t *testing.T) {
	if pin, err := remotePin(t.Context(), "https://10.0.0.5/x.git", true); err != nil || pin != nil {
		t.Errorf("an administrator's address was pinned: %v %v", pin, err)
	}
	for _, raw := range []string{"https://127.0.0.1/x.git", "https://10.0.0.5/x.git", "https://169.254.169.254/x", "https://[::1]/x.git", "https://100.100.1.1/x.git"} {
		if _, err := remotePin(t.Context(), raw, false); err == nil || !strings.Contains(err.Error(), "internal network") {
			t.Errorf("%s: %v", raw, err)
		}
	}
	pin, err := remotePin(t.Context(), "https://93.184.216.34:8443/x.git", false)
	if err != nil || strings.Join(pin, " ") != "-c http.curloptResolve=93.184.216.34:8443:93.184.216.34 -c http.followRedirects=false" {
		t.Errorf("a public address: %v %v", pin, err)
	}

	prev := lookupHost
	t.Cleanup(func() { lookupHost = prev })
	answer := []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}, {IP: net.ParseIP("10.0.0.9")}}
	lookupHost = func(context.Context, string) ([]net.IPAddr, error) { return answer, nil }
	if _, err := remotePin(t.Context(), "https://git.example.com/x.git", false); err == nil {
		t.Error("a host with one internal address was let through")
	}
	answer = answer[:1]
	if pin, err := remotePin(t.Context(), "https://git.example.com/x.git", false); err != nil || pin[1] != "http.curloptResolve=git.example.com:443:93.184.216.34" {
		t.Errorf("a public host was not pinned to what was checked: %v %v", pin, err)
	}
	lookupHost = func(context.Context, string) ([]net.IPAddr, error) { return nil, fmt.Errorf("no such host") }
	if _, err := remotePin(t.Context(), "https://git.example.com/x.git", false); err == nil {
		t.Error("a host that could not be found was let through")
	}

	p := Project{ID: "i", Name: "i", Kind: KindClone, Owner: "u", RepoURL: "https://127.0.0.1:1/x.git"}
	if _, err := cloneProject(t.Context(), p, filepath.Join(t.TempDir(), "c"), false); err == nil || !strings.Contains(err.Error(), "internal network") {
		t.Errorf("a user's clone of an internal host: %v", err)
	}
	_, dir, shallow := shallowCheckout(t)
	shallow.RepoURL = "https://10.0.0.5/x.git"
	if _, err := deepenRepo(t.Context(), shallow, dir, false); err == nil || !strings.Contains(err.Error(), "internal network") {
		t.Errorf("a user's history fetch from an internal host: %v", err)
	}
}

// An administrator's project can be cloned from a git bundle on this host:
// git reads a bundle file as a repository, and the checkout arrives with the
// history the bundle carries.
func TestAnAdministratorClonesFromABundle(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git is not installed")
	}
	origin := t.TempDir()
	initRepo(t, origin)
	for i, body := range []string{"one\n", "two\n"} {
		mustWrite(t, filepath.Join(origin, "f.txt"), body)
		commitAll(t, origin, fmt.Sprintf("c%d", i))
	}
	bundle := filepath.Join(t.TempDir(), "repo.bundle")
	if out, err := exec.Command("git", "-C", origin, "bundle", "create", bundle, "--all").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	dir := filepath.Join(t.TempDir(), "c")
	p := Project{ID: "b", Name: "b", Kind: KindClone, Owner: "admin", RepoURL: bundle}
	if _, err := cloneProject(t.Context(), p, dir, true); err != nil {
		t.Fatalf("clone from a bundle: %v", err)
	}
	if log, err := runGit(dir, "log", "--oneline"); err != nil || strings.Count(strings.TrimSpace(log), "\n") != 1 {
		t.Errorf("history from the bundle: %q %v", log, err)
	}
	if _, err := cloneProject(t.Context(), p, filepath.Join(t.TempDir(), "u"), false); err == nil {
		t.Error("a user cloned from a bundle on this host")
	}
}
