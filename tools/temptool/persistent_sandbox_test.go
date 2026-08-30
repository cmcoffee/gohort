package temptool

// Persistent shells must not build their own sandbox.
//
// This package used to carry a private copy of core/sandbox's bwrap argv
// (persistentBwrapArgv) behind its own exec.LookPath("bwrap"), justified at the
// time by core's helpers being unexported and by an expected future divergence.
// The copy then missed everything the original learned: the second backend, and
// — the one that mattered — the fail-closed bypass policy. A host with no bwrap
// refused every one-shot command and handed the LLM a long-lived unconfined
// shell.
//
// The fix is that there is one builder and this package calls it. What keeps it
// fixed is this test, because the failure mode is not a bug in a line of code:
// it is somebody re-deriving that a local copy is simpler than an export. It
// reads the source rather than exercising behavior, because the behavior it
// guards against is indistinguishable from correct behavior on a host that
// happens to have bwrap installed — which is every host anyone would run this
// suite on.

import (
	"go/parser"
	"go/printer"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// sourceWithoutComments reparses a file and prints it back with comments
// dropped, so the scan below reads CODE.
//
// Needed because the first thing this guard did was fail on the comment
// explaining what it guards against — which names the banned call in order to
// say why it is banned. A guard that cannot describe its own subject forces the
// next person to either weaken the prose or delete the test, and they will
// pick whichever is quicker.
func sourceWithoutComments(t *testing.T, path string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0) // no parser.ParseComments: comments are dropped
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var buf strings.Builder
	if err := (&printer.Config{Mode: printer.RawFormat}).Fprint(&buf, fset, f); err != nil {
		t.Fatalf("print %s: %v", path, err)
	}
	return buf.String()
}

func TestPersistentShellsDoNotRollTheirOwnSandbox(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	// Each banned fragment, with the reason it is banned — an assertion that
	// only prints "found forbidden string" teaches the next reader nothing
	// about why they cannot have it.
	banned := map[string]string{
		`LookPath("bwrap")`: "probe the backend through core/sandbox (NewSandboxedShellCmd), which knows about every backend rather than one",
		`"--unshare-pid"`:   "the argv belongs to core/sandbox; a second copy is a second thing to forget to update",
		`"--ro-bind"`:       "the bind set belongs to core/sandbox for the same reason",
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src := sourceWithoutComments(t, f)
		for frag, why := range banned {
			if strings.Contains(src, frag) {
				t.Errorf("%s builds its own sandbox (%s) — %s", f, frag, why)
			}
		}
	}
}

// The refusal has to reach the caller as an error. A persistent shell that
// logged a warning and opened anyway is the exact bug above, and "we log it"
// was how it survived review the first time.
func TestPersistentShellOpenReturnsTheRefusalRatherThanLoggingIt(t *testing.T) {
	body := sourceWithoutComments(t, "persistent_shell.go")
	i := strings.Index(body, "func startSandboxedShell(")
	if i < 0 {
		t.Fatal("startSandboxedShell is gone; this guard needs rewriting alongside it")
	}
	fn := body[i:]
	if j := strings.Index(fn, "\nfunc "); j > 0 {
		fn = fn[:j]
	}
	if !strings.Contains(fn, "return nil, err") {
		t.Error("a build refusal must be returned to the caller, not swallowed")
	}
	if strings.Contains(fn, `exec.CommandContext(ctx, "sh"`) {
		t.Error("there is no unconfined fallback: that fallback WAS the vulnerability")
	}
}
