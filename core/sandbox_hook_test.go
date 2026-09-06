package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsNonPublicHost(t *testing.T) {
	nonPublic := []string{"", "localhost", "127.0.0.1", "::1", "10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.1.1", "0.0.0.0", "foo.local", "bar.internal"}
	for _, h := range nonPublic {
		if !IsNonPublicHost(h) {
			t.Errorf("%q should be non-public", h)
		}
	}
	public := []string{"i.redd.it", "example.com", "8.8.8.8", "1.1.1.1", "graph.microsoft.com"}
	for _, h := range public {
		if IsNonPublicHost(h) {
			t.Errorf("%q should be public", h)
		}
	}
}

// The gohort python helper is deployed best-effort, and every failure
// path used to be Debug-only or silent. That is the wrong volume for
// this particular failure: the only symptom that reaches anyone is a
// ModuleNotFoundError on the first line of a script, which names the
// script's import rather than the deployment that never happened.

func TestGohortLibReportsWhyItCouldNotDeploy(t *testing.T) {
	// A run with no workspaces dir configured said nothing at all, at
	// any level — the case that leaves an operator with a broken tool
	// and an empty log.
	prevDir := WorkspacesDir()
	SetWorkspacesDir("")
	gohortLibDirMu.Lock()
	gohortLibDirPath, gohortLibWarned = "", false
	gohortLibDirMu.Unlock()
	t.Cleanup(func() {
		SetWorkspacesDir(prevDir)
		gohortLibDirMu.Lock()
		gohortLibDirPath, gohortLibWarned = "", false
		gohortLibDirMu.Unlock()
	})

	var lines []string
	prevLog := Log
	Log = func(v ...any) {
		if len(v) > 0 {
			lines = append(lines, fmt.Sprint(v[0]))
		}
	}
	t.Cleanup(func() { Log = prevLog })

	if got := EnsureGohortLibDir(); got != "" {
		t.Fatalf("with no workspaces dir there is nowhere to deploy, got %q", got)
	}
	if len(lines) == 0 {
		t.Fatal("the failure was silent — the only symptom left is ModuleNotFoundError inside a script")
	}
	joined := strings.Join(lines, "\n")
	// It must name the consequence, not just the fault: whoever reads
	// this is about to be told by an agent that a tool is broken.
	for _, want := range []string{"ModuleNotFoundError", "workspaces directory"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the warning should mention %q: %s", want, joined)
		}
	}

	// Once per process, not once per dispatch: this runs on every
	// sandboxed tool call.
	before := len(lines)
	EnsureGohortLibDir()
	EnsureGohortLibDir()
	if len(lines) != before {
		t.Errorf("warned again on later dispatches: %d → %d lines", before, len(lines))
	}
}

// And the happy path still deploys something importable.
func TestGohortLibDeploysAnImportablePackage(t *testing.T) {
	dir := t.TempDir()
	prevDir := WorkspacesDir()
	SetWorkspacesDir(filepath.Join(dir, "workspaces"))
	gohortLibDirMu.Lock()
	gohortLibDirPath, gohortLibWarned = "", false
	gohortLibDirMu.Unlock()
	t.Cleanup(func() {
		SetWorkspacesDir(prevDir)
		gohortLibDirMu.Lock()
		gohortLibDirPath, gohortLibWarned = "", false
		gohortLibDirMu.Unlock()
	})

	lib := EnsureGohortLibDir()
	if lib == "" {
		t.Fatal("deployment failed on a writable path")
	}
	// PYTHONPATH points at the directory CONTAINING the package, so
	// `import gohort` resolves the package dir beneath it.
	if _, err := os.Stat(filepath.Join(lib, "gohort", "__init__.py")); err != nil {
		t.Errorf("no importable gohort package under %s: %v", lib, err)
	}
}

// A unix socket path over 107 bytes fails with a bare EINVAL — "invalid
// argument" — which names neither the limit nor the path. It cost a live
// deployment a working tool and an operator a hunt through permissions and
// filesystems before the length was the suspect. These pin the arithmetic so it
// cannot come back quietly.

func TestHookSocketFitsUnderADeepWorkspace(t *testing.T) {
	// The real path from the failure, rebuilt: a per-agent workspace is
	// <root>/.agents/<email>/<uuid>/, which is 92 characters before the socket
	// name has even started.
	deep := "/opt/gohort/data/workspaces/.agents/cmcoffee@gmail.com/45dbd021-4c1d-494b-a2ab-6416c355cbd8"
	old := filepath.Join(deep, ".gohort_hook_084099fb5aee06bf.sock")
	if len(old) <= maxUnixSocketPath {
		t.Fatalf("the path that failed is %d bytes — this test has lost its subject", len(old))
	}

	got, err := hookSocketPath(deep, "084099fb5aee06bf")
	if err != nil {
		t.Fatalf("a deep workspace must still get a socket: %v", err)
	}
	if len(got) > maxUnixSocketPath {
		t.Errorf("still too long at %d bytes: %s", len(got), got)
	}
	// And it is NOT in the workspace, which is the whole point — shortening
	// the name could never have been enough, since the prefix alone leaves 15
	// characters and ".gohort_hook_.sock" is 18 with no token.
	if strings.HasPrefix(got, deep) {
		t.Errorf("a deep workspace cannot host the socket: %s", got)
	}
}

func TestHookSocketNameIsUnique(t *testing.T) {
	// Two hooks alive at once must not collide — the token is what separates
	// them, and moving to a shared directory is exactly where a dropped token
	// would start mattering.
	a, err := hookSocketPath(t.TempDir(), "aaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	b, err := hookSocketPath(t.TempDir(), "bbbbbbbbbbbbbbbb")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("two tokens produced one path: %s", a)
	}
	if filepath.Dir(a) != filepath.Dir(b) {
		t.Errorf("both should sit in the same short dir, got %s and %s", a, b)
	}
	// 0700 on the directory: the sockets are 0600 already, but a listable
	// directory would hand every token to anyone on the host.
	if fi, err := os.Stat(filepath.Dir(a)); err == nil {
		if perm := fi.Mode().Perm(); perm != 0700 {
			t.Errorf("hook dir perms are %o, want 700", perm)
		}
	}
}

// The end-to-end check the arithmetic above cannot make: that a hook actually
// BINDS under a workspace path long enough to have failed before. This is the
// regression test for the live failure — everything else here is the reasoning
// that led to it.
func TestHookActuallyListensUnderADeepWorkspace(t *testing.T) {
	deep := filepath.Join(t.TempDir(),
		".agents", "cmcoffee@gmail.com", "45dbd021-4c1d-494b-a2ab-6416c355cbd8")
	h, err := NewSandboxHook(deep, []string{"log"}, &ToolSession{})
	if err != nil {
		t.Fatalf("a hook under a deep workspace must still listen: %v", err)
	}
	if h == nil {
		t.Fatal("capabilities were declared, so there should be a hook")
	}
	defer h.Close()
	if strings.HasPrefix(h.SocketPath, deep) {
		t.Errorf("the socket cannot live in a workspace this deep: %s", h.SocketPath)
	}
	if len(h.SocketPath) > maxUnixSocketPath {
		t.Errorf("bound path is %d bytes: %s", len(h.SocketPath), h.SocketPath)
	}
}

// No declared capabilities means no socket at all — the privacy posture the
// hook was built with, and a path that must not start binding things now that
// binding is cheaper.
func TestNoCapabilitiesStillMeansNoSocket(t *testing.T) {
	h, err := NewSandboxHook(t.TempDir(), nil, &ToolSession{})
	if err != nil || h != nil {
		t.Errorf("a tool with no capabilities gets no hook, got %v / %v", h, err)
	}
}
