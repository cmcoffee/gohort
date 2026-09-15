package workspace

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// registerTestRoot installs a scope kind that accepts exactly one folder, so
// the tests exercise the real ResolvePathScope path rather than a stub of it.
func registerTestRoot(t *testing.T, kind, name, abs string) {
	t.Helper()
	RegisterPathScope(kind, PathScope{
		Resolve: func(user, root, value string) (string, error) {
			if root != name {
				return "", Error("no such root " + root)
			}
			if value != "." && value != "bundle-a" {
				return "", Error("no such folder " + value)
			}
			if value == "." {
				return abs, nil
			}
			return abs + "/" + value, nil
		},
		Roots: func(user string) []PathScopeRoot {
			return []PathScopeRoot{{Ref: kind + ":" + name, Label: name}}
		},
	})
}

func TestRunCwdResolvesThroughARegisteredRoot(t *testing.T) {
	registerTestRoot(t, "testfiles", "bundles", "/srv/bundles")
	sess := &ToolSession{Username: "alice", WorkspaceDir: t.TempDir()}

	got, err := resolveRunCwd(map[string]any{
		"cwd_root": "testfiles:bundles", "cwd": "bundle-a",
	}, sess)
	if err != nil {
		t.Fatalf("a folder inside a registered root should resolve: %v", err)
	}
	if got != "/srv/bundles/bundle-a" {
		t.Errorf("want the absolute path, got %q", got)
	}
}

// A root with no folder is refused, and NOT defaulted to ".".
//
// A scope resolver proves a value lands strictly below its root, so "." cleans
// to the root and comes back as "resolves outside the store" — a containment
// error about a path the caller never typed. The refusal has to say what is
// actually missing.
func TestRunCwdRefusesARootWithNoFolder(t *testing.T) {
	registerTestRoot(t, "testfiles", "bundles", "/srv/bundles")
	sess := &ToolSession{Username: "alice"}

	_, err := resolveRunCwd(map[string]any{"cwd_root": "testfiles:bundles"}, sess)
	if err == nil {
		t.Fatal("a root with no folder should be refused")
	}
	if !strings.Contains(err.Error(), "pass cwd as well") {
		t.Errorf("the refusal should name the missing parameter, got: %v", err)
	}
	if strings.Contains(err.Error(), "outside") {
		t.Errorf("it must not surface as a containment error: %v", err)
	}
}

// The whole point of the gate: a path the model names outright is refused,
// because under bubblewrap the cwd is bind-mounted in and a free path would
// be a read escape from the workspace.
func TestRunCwdRefusesAPathOutsideEveryRoot(t *testing.T) {
	registerTestRoot(t, "testfiles", "bundles", "/srv/bundles")
	sess := &ToolSession{Username: "alice"}

	if _, err := resolveRunCwd(map[string]any{
		"cwd_root": "testfiles:bundles", "cwd": "../../etc",
	}, sess); err == nil {
		t.Fatal("traversal out of the root should be refused")
	}
	if _, err := resolveRunCwd(map[string]any{
		"cwd_root": "testfiles:nope", "cwd": ".",
	}, sess); err == nil {
		t.Fatal("an unregistered root should be refused")
	}
	// An unknown KIND fails closed too — a constraint nobody implements must
	// not read as no constraint.
	if _, err := resolveRunCwd(map[string]any{
		"cwd_root": "nosuchkind:x", "cwd": ".",
	}, sess); err == nil {
		t.Fatal("an unknown scope kind should be refused, not ignored")
	}
}

// A model that guessed cannot act on "unknown root" alone.
func TestRunCwdRefusalNamesTheRegisteredRoots(t *testing.T) {
	registerTestRoot(t, "testfiles", "bundles", "/srv/bundles")
	sess := &ToolSession{Username: "alice"}

	_, err := resolveRunCwd(map[string]any{"cwd_root": "testfiles:wrong"}, sess)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), `"testfiles:bundles"`) {
		t.Errorf("the refusal should list what IS registered, got: %v", err)
	}
}

// cwd without cwd_root is the mistake worth naming: starting in the workspace
// anyway would look like the folder was empty.
func TestRunCwdWithoutARootIsRefusedNotIgnored(t *testing.T) {
	sess := &ToolSession{Username: "alice"}
	if _, err := resolveRunCwd(map[string]any{"cwd": "bundle-a"}, sess); err == nil {
		t.Fatal("a cwd with no root should be refused")
	}
}

// Every existing caller passes neither, and must keep starting in the
// workspace with no refusal.
func TestRunWithNoCwdIsUnchanged(t *testing.T) {
	sess := &ToolSession{Username: "alice"}
	got, err := resolveRunCwd(map[string]any{"command": "ls"}, sess)
	if err != nil {
		t.Fatalf("the ordinary run must not have become refusable: %v", err)
	}
	if got != "" {
		t.Errorf("want no WorkDir, got %q", got)
	}
}
