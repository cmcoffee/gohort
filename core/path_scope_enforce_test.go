package core

// Path-scope enforcement, and the sandbox bind that makes a proved path
// usable.
//
// The field has been on core.ToolParam all along, and until v0.6.241 the
// ONLY dispatch path that checked it was servitor's appliance dispatch.
// A Builder-authored tool declaring path_scope was therefore decorated
// rather than constrained: its value was shell-quoted and substituted,
// and quoting stops a value contributing SYNTAX while saying nothing
// about it pointing somewhere else — which is what this file's own
// opening paragraph warns about.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scopeTestKind is this file's own scope kind, kept out of every other
// test's way (see the note in scopeFixture).
const scopeTestKind = "pathscope_enforce_test"

// scopeFixture registers a "files"-style scope over a temp root with two
// folders, the way filestore registers a store.
func scopeFixture(t *testing.T) (root string, teardown func()) {
	t.Helper()
	root = t.TempDir()
	for _, d := range []string{"bundle-a", "bundle-b"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// A kind name unique to this test. The registry is global and
	// process-wide, so a generic name here leaks into any other test that
	// enumerates roots — which is exactly what happened: registering
	// "testfiles" made an unrelated store test fail in the full run and
	// pass alone.
	RegisterPathScope(scopeTestKind, PathScope{
		Resolve: func(user, name, value string) (string, error) {
			if name != "logs" {
				return "", Error("no such store " + name)
			}
			abs := filepath.Join(root, value)
			// The containment check a real resolver does: the joined path
			// must still be under the root.
			clean := filepath.Clean(abs)
			if !strings.HasPrefix(clean, filepath.Clean(root)+string(filepath.Separator)) {
				return "", Error("outside the store")
			}
			if _, err := os.Stat(clean); err != nil {
				return "", Error("no such folder")
			}
			return clean, nil
		},
		Values: func(user, name string) []string { return []string{"bundle-a", "bundle-b"} },
	})
	return root, func() {}
}

func TestScopedArgsAreProvedAndReturnedAbsolute(t *testing.T) {
	root, done := scopeFixture(t)
	defer done()

	params := map[string]ToolParam{
		"bundle": {Type: "string", PathScope: scopeTestKind + ":logs"},
		"query":  {Type: "string"}, // unscoped, must pass through untouched
	}

	out, resolved, err := ResolveScopedArgs("u", "ag", params,
		map[string]any{"bundle": "bundle-a", "query": "ERROR"})
	if err != nil {
		t.Fatalf("a folder that exists should resolve: %v", err)
	}
	want := filepath.Join(root, "bundle-a")
	if out["bundle"] != want {
		t.Errorf("the value substituted should be the ABSOLUTE path: %v", out["bundle"])
	}
	if out["query"] != "ERROR" {
		t.Errorf("an unscoped arg must pass through: %v", out["query"])
	}
	if len(resolved) != 1 || resolved[0] != want {
		t.Errorf("the resolved paths are what a sandbox has to bind: %v", resolved)
	}

	// The escape the scope exists for. Shell quoting would have made
	// this a single well-formed argument and let it through.
	if _, _, err := ResolveScopedArgs("u", "ag", params,
		map[string]any{"bundle": "../../etc"}); err == nil {
		t.Error("a traversal out of the root must be refused")
	}
	// A folder inside the root that does not exist is refused too — the
	// resolver proves the path, not just the prefix.
	if _, _, err := ResolveScopedArgs("u", "ag", params,
		map[string]any{"bundle": "bundle-z"}); err == nil {
		t.Error("a folder that is not there should be refused")
	}
	// An ABSENT argument is the required-check's business, not this one.
	if _, res, err := ResolveScopedArgs("u", "ag", params, map[string]any{"query": "x"}); err != nil || len(res) != 0 {
		t.Errorf("absence should pass through: %v %v", res, err)
	}
	// The caller's map is not rewritten in place — it is what gets logged
	// and echoed back, and it should say what the model actually asked.
	args := map[string]any{"bundle": "bundle-b"}
	if _, _, err := ResolveScopedArgs("u", "ag", params, args); err != nil {
		t.Fatal(err)
	}
	if args["bundle"] != "bundle-b" {
		t.Errorf("the original args were mutated: %v", args)
	}
	// No scoped params at all: the same map back, nothing allocated.
	plain := map[string]ToolParam{"q": {Type: "string"}}
	if out, res, err := ResolveScopedArgs("u", "ag", plain, args); err != nil ||
		len(res) != 0 || out["bundle"] != "bundle-b" {
		t.Errorf("an unscoped tool should be untouched: %v %v %v", out, res, err)
	}
}
