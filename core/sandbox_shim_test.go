package core

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cmcoffee/gohort/core/filenotes"
	"github.com/cmcoffee/gohort/core/netgate"
	"github.com/cmcoffee/gohort/core/sandbox"
)

// EnsureGohortLibDir must drop the executable fetch-family shims into the
// <lib>/bin dir so a sandboxed script can call fetch_url / fetch_via /
// browse_page as ordinary commands instead of subprocessing an LLM tool name
// (the FileNotFoundError footgun). The dir is a subpath of the RO lib mount,
// so the existing bind covers it — no separate mount needed.
func TestEnsureGohortLibDirWritesShims(t *testing.T) {
	SetWorkspacesDir(filepath.Join(t.TempDir(), "workspaces"))
	// Reset the process-level cache so this test controls the lib dir rather
	// than inheriting whatever an earlier test in the package populated.
	gohortLibDirMu.Lock()
	gohortLibDirPath = ""
	gohortLibDirMu.Unlock()

	libBase := EnsureGohortLibDir()
	if libBase == "" {
		t.Fatal("EnsureGohortLibDir returned empty")
	}
	binDir := filepath.Join(libBase, "bin")
	for _, name := range []string{"_gohort_shim.py", "fetch_url", "fetch_via", "browse_page", "_gohort_curl.py", "curl", "wget"} {
		fi, err := os.Stat(filepath.Join(binDir, name))
		if err != nil {
			t.Fatalf("shim %s missing: %v", name, err)
		}
		if fi.Mode().Perm()&0111 == 0 {
			t.Errorf("shim %s not executable (mode %v)", name, fi.Mode())
		}
	}
	disp, _ := os.ReadFile(filepath.Join(binDir, "_gohort_shim.py"))
	if !strings.Contains(string(disp), "def main(op, argv)") {
		t.Error("_gohort_shim.py missing main(op, argv)")
	}
	// The bare wrappers must delegate to the shared dispatcher, not duplicate it.
	w, _ := os.ReadFile(filepath.Join(binDir, "fetch_via"))
	if !strings.Contains(string(w), "from _gohort_shim import main") {
		t.Error("fetch_via wrapper doesn't delegate to _gohort_shim")
	}
	if SandboxGohortBinMountPath != SandboxGohortLibMountPath+"/bin" {
		t.Errorf("bin mount %q must be a subpath of lib mount %q (so the RO bind covers it)",
			SandboxGohortBinMountPath, SandboxGohortLibMountPath)
	}
}

// The shim bin dir must sit at the FRONT of the sandbox PATH so its
// fetch_url/browse_page win over any same-named host binary — and it must be
// the dir that actually EXISTS for the run in question. A remapping sandbox
// reaches it at the mount point; without remapping nothing is mounted, and
// prepending the mount path there left the shims unreachable on every host
// with no bubblewrap — which is every macOS host.

// End to end through the real hook, no fake module: the curl a sandboxed
// command types reaches gohort's own fetch and is held to its rules. Aimed at
// a loopback server, which the hook refuses, so the test needs no internet and
// shows both halves: the request got to gohort, and gohort, not curl, decided.
func TestCurlStandInIsHeldToTheHooksRules(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer srv.Close()
	work := t.TempDir()
	hook, err := NewSandboxHook(work, []string{"fetch"}, &ToolSession{Username: "owner"})
	if err != nil || hook == nil {
		t.Fatalf("hook: %v", err)
	}
	defer hook.Close()
	lib := t.TempDir()
	bin := filepath.Join(lib, "bin")
	os.MkdirAll(bin, 0o755)
	if err := os.WriteFile(filepath.Join(lib, "gohort.py"), []byte(SandboxHookPythonShim), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, s := range sandboxToolShims {
		os.WriteFile(filepath.Join(bin, s.name), []byte(s.content), 0o755)
	}
	cmd := exec.Command(py, filepath.Join(bin, "curl"), "-sS", srv.URL+"/x")
	cmd.Dir = work
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "PYTHONPATH=" + lib,
		"GOHORT_HOOK_PATH=" + hook.SocketPath, "GOHORT_NETWORK=closed"}
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 7 {
		t.Fatalf("want exit 7, got %v:\n%s", err, out)
	}
	if !strings.Contains(string(out), "refusing to reach non-public host") {
		t.Errorf("the hook's own refusal should come through:\n%s", out)
	}
	if reached {
		t.Error("the request reached the server: something other than the hook dialled it")
	}
}

// git in the sandbox, end to end through the real hook: a clone runs the real
// git here, and only its HTTP leaves, through gohort, which adds the
// credential that covers the host. Checked against a real git server
// (git http-backend) that refuses anything without the credential:
//   - the clone works and every request reached the server signed in;
//   - the token never landed in the sandbox: the clone's .git/config holds
//     the plain URL;
//   - a push is refused by name, and nothing is sent;
//   - a server no credential covers is held to the hook's own rule (this one
//     is on loopback, which a plain fetch may not reach), and the reply says so;
//   - a command that does not fetch runs the real git, untouched.
func TestGitRunsInTheSandboxAndFetchesThroughTheHook(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	execPath, _ := exec.Command(gitBin, "--exec-path").Output()
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend not available")
	}
	root := t.TempDir()
	gitEnv := []string{"HOME=" + root, "GIT_CONFIG_NOSYSTEM=1", "PATH=/usr/bin:/bin"}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(gitBin, append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir, cmd.Env = dir, gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	src := filepath.Join(root, "src")
	os.MkdirAll(src, 0o755)
	git(src, "init", "-q")
	os.WriteFile(filepath.Join(src, "README"), []byte("hello from the served repo\n"), 0o644)
	git(src, "add", "README")
	git(src, "commit", "-q", "-m", "first commit")
	serve := filepath.Join(root, "serve")
	os.MkdirAll(serve, 0o755)
	git(root, "clone", "-q", "--bare", src, filepath.Join(serve, "repo.git"))

	backendH := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + serve, "GIT_HTTP_EXPORT_ALL=1"}}
	var mu sync.Mutex
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok-git" {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		backendH.ServeHTTP(w, r)
	}))
	defer srv.Close()
	uncovered := httptest.NewServer(backendH)
	defer uncovered.Close()

	secureAPITestStore(t)
	if err := Secure().Save(SecureCredential{Name: "githost", Type: SecureCredBearer, BaseURL: srv.URL}, "tok-git"); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	hook, err := NewSandboxHook(work, []string{"fetch"}, &ToolSession{Username: "owner"})
	if err != nil || hook == nil {
		t.Fatalf("hook: %v", err)
	}
	defer hook.Close()
	lib := t.TempDir()
	bin := filepath.Join(lib, "bin")
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(lib, "gohort.py"), []byte(SandboxHookPythonShim), 0o644)
	for _, s := range sandboxToolShims {
		os.WriteFile(filepath.Join(bin, s.name), []byte(s.content), 0o755)
	}
	run := func(args ...string) (string, int) {
		cmd := exec.Command(py, append([]string{filepath.Join(bin, "git")}, args...)...)
		cmd.Dir = work
		cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "PYTHONPATH=" + lib, "HOME=" + work, "GIT_CONFIG_NOSYSTEM=1", "GOHORT_NETWORK=closed"}
		for k, v := range hook.RunEnv() {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		out, err := cmd.CombinedOutput()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return string(out), ee.ExitCode()
		}
		return string(out), 0
	}

	if out, code := run("clone", srv.URL+"/repo.git", "copy"); code != 0 {
		t.Fatalf("clone through the relay: exit %d\n%s", code, out)
	}
	if b, _ := os.ReadFile(filepath.Join(work, "copy", "README")); string(b) != "hello from the served repo\n" {
		t.Errorf("the clone's contents: %q", b)
	}
	mu.Lock()
	signed := len(auths) > 0
	for _, a := range auths {
		signed = signed && a == "Bearer tok-git"
	}
	mu.Unlock()
	if !signed {
		t.Errorf("every request should reach the server signed in: %q", auths)
	}
	cfg, _ := os.ReadFile(filepath.Join(work, "copy", ".git", "config"))
	if strings.Contains(string(cfg), "tok-git") || !strings.Contains(string(cfg), srv.URL+"/repo.git") {
		t.Errorf("the clone's config should hold the plain URL and no token:\n%s", cfg)
	}
	if out, code := run("-C", "copy", "log", "--oneline"); code != 0 || !strings.Contains(out, "first commit") {
		t.Errorf("a command that does not fetch runs the real git: exit %d\n%s", code, out)
	}
	if out, code := run("-C", "copy", "fetch"); code != 0 {
		t.Errorf("a fetch in the clone goes through the relay too: exit %d\n%s", code, out)
	}
	mu.Lock()
	before := len(auths)
	mu.Unlock()
	if out, code := run("-C", "copy", "push", "origin", "main"); code != 1 || !strings.Contains(out, "not available here") {
		t.Errorf("a push is refused by name: exit %d\n%s", code, out)
	}
	mu.Lock()
	if len(auths) != before {
		t.Error("a refused push sent a request")
	}
	mu.Unlock()
	if out, code := run("clone", uncovered.URL+"/repo.git", "copy2"); code == 0 || !strings.Contains(out, "non-public host") {
		t.Errorf("a server no credential covers is held to the hook's rule: exit %d\n%s", code, out)
	}
}

// The relay carries git's fetch requests and nothing else: not a push, and
// not an arbitrary request dressed as one.
func TestGitRelayCarriesFetchesOnly(t *testing.T) {
	for _, c := range []struct {
		method, url string
		ok          bool
	}{
		{"GET", "https://host-a/org/repo.git/info/refs?service=git-upload-pack", true},
		{"POST", "https://host-a/org/repo.git/git-upload-pack", true},
		{"GET", "https://host-a/org/repo.git/info/refs?service=git-receive-pack", false},
		{"POST", "https://host-a/org/repo.git/git-receive-pack", false},
		{"GET", "https://host-a/api/v1/user", false},
		{"POST", "https://host-a/upload", false},
		{"DELETE", "https://host-a/org/repo.git/git-upload-pack", false},
	} {
		u, _ := neturl.Parse(c.url)
		if got := gitRelayRefusal(c.method, u) == ""; got != c.ok {
			t.Errorf("%s %s: allowed=%v, want %v", c.method, c.url, got, c.ok)
		}
	}
}

// The proxy end to end, in a real sandbox with its network cut (bubblewrap):
// an ordinary HTTPS client (Python's urllib, honouring HTTPS_PROXY) reaches a
// server outside through the run's relay and gohort's tunnel, and nothing else
// gets out. The server is on this host's loopback, which a tunnel may not
// reach, so the test opens it to the dial for this test; everything else is
// as it runs:
//   - the reply arrives, so the relay, the token and the tunnel all worked;
//   - without the proxy grant the tunnel is refused;
//   - with no token the proxy asks for one;
//   - a port other than https's is refused;
//   - loopback stays out of reach once the test's opening is gone.
func TestProxyReachesOutOfAClosedSandboxThroughTheRelay(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap not available")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("through the tunnel"))
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	prevDial, prevPorts := relayDial, relayConnectPorts
	t.Cleanup(func() { relayDial, relayConnectPorts = prevDial, prevPorts })
	opened := func() {
		relayDial = func(ctx context.Context, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
		}
		relayConnectPorts = map[string]bool{"443": true, port: true}
	}
	opened()

	// Not under /tmp: bwrap mounts a fresh tmpfs there after binding the
	// workspace, and a workspace inside it vanishes (limitTestWorkspace in
	// core/sandbox says more).
	root, err := os.MkdirTemp("/var/tmp", "gohort-proxy-")
	if err != nil {
		t.Skipf("no writable dir outside /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	SetWorkspacesDir(filepath.Join(root, "workspaces"))
	gohortLibDirMu.Lock()
	gohortLibDirPath = ""
	gohortLibDirMu.Unlock()
	work := filepath.Join(root, "workspaces", "owner", "w")
	os.MkdirAll(work, 0o755)
	sess := &ToolSession{Username: "owner", WorkspaceDir: work}
	closed := netgate.WithWorkspaceNetwork(context.Background(), false)
	// fetch writes the probe: an HTTPS GET of target, through the proxy the
	// run was given, or through proxyExpr (a Python expression) instead.
	fetch := func(target, proxyExpr string) {
		script := "import ssl, urllib.request, os\n"
		if proxyExpr != "" {
			script += "os.environ['HTTPS_PROXY'] = os.environ['https_proxy'] = " + proxyExpr + "\n"
		}
		script += "try:\n    print(urllib.request.urlopen(" + strconvQuote(target) + ", context=ssl._create_unverified_context(), timeout=20).read().decode())\n" +
			"except Exception as e:\n    print('FAILED', e)\n"
		os.WriteFile(filepath.Join(work, "probe.py"), []byte(script), 0o644)
	}
	run := func(caps []string) string {
		res := sandbox.RunSandboxedShellIn(closed, sandbox.ShellRun{
			Command: "python3 probe.py", WorkspaceDir: work,
			HookCapabilities: caps, HookSession: sess, Proxy: true,
		})
		if !res.Sandbox {
			t.Skip("this host runs commands unconfined")
		}
		return res.Output
	}
	fetch("https://repo.example.test:"+port+"/x", "")
	if out := run([]string{"fetch", "proxy"}); !strings.Contains(out, "through the tunnel") {
		t.Fatalf("a proxied HTTPS request from a closed sandbox should get through:\n%s", out)
	}
	if out := run([]string{"fetch"}); !strings.Contains(out, "403") || strings.Contains(out, "through the tunnel") {
		t.Errorf("without the proxy grant the tunnel is refused:\n%s", out)
	}
	fetch("https://repo.example.test:"+port+"/x", "'http://127.0.0.1:' + os.environ['GOHORT_RELAY_PORT']")
	if out := run([]string{"fetch", "proxy"}); !strings.Contains(out, "407") {
		t.Errorf("a request without the run's token is asked for it:\n%s", out)
	}
	fetch("https://repo.example.test:8443/x", "")
	if out := run([]string{"fetch", "proxy"}); !strings.Contains(out, "403") || strings.Contains(out, "through the tunnel") {
		t.Errorf("a port other than https's is refused:\n%s", out)
	}
	relayDial, relayConnectPorts = prevDial, map[string]bool{"443": true, port: true}
	fetch("https://127.0.0.2:"+port+"/x", "")
	if out := run([]string{"fetch", "proxy"}); !strings.Contains(out, "502") || strings.Contains(out, "through the tunnel") {
		t.Errorf("a loopback address is out of a tunnel's reach:\n%s", out)
	}
}

func strconvQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "\\'") + "'" }

// A rewritten git remote maps back to the address it stood for, an ssh one
// to https on the same host.
func TestRelayGitTarget(t *testing.T) {
	for rest, want := range map[string]string{
		"https/host-a/org/repo.git/info/refs": "https://host-a/org/repo.git/info/refs?service=git-upload-pack",
		"http/host-a:8080/repo.git/info/refs": "http://host-a:8080/repo.git/info/refs?service=git-upload-pack",
		"ssh/host-a:org/repo.git/info/refs":   "https://host-a/org/repo.git/info/refs?service=git-upload-pack",
		"ssh/host-a/org/repo.git/info/refs":   "https://host-a/org/repo.git/info/refs?service=git-upload-pack",
		"ftp/host-a/x":                        "",
	} {
		if got := relayGitTarget(rest, "service=git-upload-pack"); want != "" && got != want || want == "" && got != "" {
			t.Errorf("%s -> %q, want %q", rest, got, want)
		}
	}
}

// The relay answers this run only: a git request carrying another token is
// refused before anything is sent.
func TestRelayRefusesAnotherRunsToken(t *testing.T) {
	work := t.TempDir()
	hook, err := NewSandboxHook(work, []string{"fetch"}, &ToolSession{Username: "owner"})
	if err != nil || hook == nil {
		t.Fatalf("hook: %v", err)
	}
	defer hook.Close()
	ask := func(path string) string {
		c, err := net.Dial("unix", hook.SocketPath)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.Write([]byte("{\"method\":\"relay\",\"params\":{}}\nGET " + path + " HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"))
		b, _ := io.ReadAll(c)
		return string(b)
	}
	if out := ask("/wrong-token/https/host-a/org/repo.git/info/refs?service=git-upload-pack"); !strings.HasPrefix(out, "HTTP/1.1 403") || !strings.Contains(out, "not this run's relay") {
		t.Errorf("another token: %q", out)
	}
	if out := ask("/" + hook.RelayToken + "/https/host-a/org/repo.git/git-receive-pack"); !strings.HasPrefix(out, "HTTP/1.1 403") || !strings.Contains(out, "push") {
		t.Errorf("a push through the relay: %q", out)
	}
	if hook.RelayPort == 0 || len(hook.RelayToken) < 16 {
		t.Errorf("a hook makes its run a port and a token: %d %q", hook.RelayPort, hook.RelayToken)
	}
}

// A script names what it made, through the real hook: from Python with
// gohort.describe and from a shell with describe_file, on a run whose hook
// grants nothing else (naming the run's own output reaches nothing outside
// it). The name is kept with the file (core/filenotes). A file outside the
// run's folder, or a link in it that points out, is refused.
func TestAScriptNamesWhatItMade(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	work := t.TempDir()
	os.WriteFile(filepath.Join(work, "generated_music.mp3"), []byte("ID3"), 0o644)
	os.WriteFile(filepath.Join(work, "cover.png"), []byte("png"), 0o644)
	outside := filepath.Join(t.TempDir(), "elsewhere.txt")
	os.WriteFile(outside, []byte("x"), 0o644)
	os.Symlink(outside, filepath.Join(work, "link.txt"))
	hook, err := NewSandboxHook(work, []string{"log"}, &ToolSession{Username: "owner"})
	if err != nil || hook == nil {
		t.Fatalf("hook: %v", err)
	}
	defer hook.Close()
	lib := t.TempDir()
	bin := filepath.Join(lib, "bin")
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(lib, "gohort.py"), []byte(SandboxHookPythonShim), 0o644)
	for _, s := range sandboxToolShims {
		os.WriteFile(filepath.Join(bin, s.name), []byte(s.content), 0o755)
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = work
		cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "PYTHONPATH=" + lib, "GOHORT_HOOK_PATH=" + hook.SocketPath}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run(py, "-c", "from gohort import describe; print(describe('generated_music.mp3', 'Gas Prices Are Too High'))"); err != nil || !strings.Contains(out, `"Gas Prices Are Too High"`) {
		t.Fatalf("python describe: %v\n%s", err, out)
	}
	if n, _ := filenotes.Get(filepath.Join(work, "generated_music.mp3")); n.Title != "Gas Prices Are Too High" {
		t.Errorf("the title is kept with the file: %+v", n)
	}
	if out, err := run(py, filepath.Join(bin, "describe_file"), "cover.png", "Album", "cover,", "red", "truck"); err != nil || !strings.Contains(out, `"Album cover, red truck"`) {
		t.Errorf("shell describe_file: %v\n%s", err, out)
	}
	for _, p := range []string{outside, "link.txt", "../x"} {
		if out, err := run(py, "-c", "from gohort import describe; describe('"+p+"', 'nope')"); err == nil || !strings.Contains(out, "describe:") {
			t.Errorf("describe %s should be refused: %v\n%s", p, err, out)
		}
	}
	if _, ok := filenotes.Get(outside); ok {
		t.Error("a file outside the run's folder was noted")
	}
}
