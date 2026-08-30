package servitor

// One test survives the bundle lift, and it is the only one that could not
// move: an AGREEMENT between two packages rather than a property of either.
//
// core builds the bundle tools; servitor's tool_guard.go names them in a
// hardcoded allow-list, and a tool missing from it panics the app at session
// start. Neither side can check that alone — core does not know the list
// exists, and the list is just strings. Renaming a tool in core without
// renaming it here is exactly the drift this catches.
//
// Everything else went with the code it covers: the store's behaviour to
// core/bundle, the tools' own promises to core, the staging path's filename
// and purge guards to core/bundle.

import (
	"path/filepath"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// withBundleDB points the evidence store at a throwaway database for one test.
func withBundleDB(t *testing.T) {
	t.Helper()
	db, err := OpenDB(filepath.Join(t.TempDir(), "bundles.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	prev := BundleFilesDB
	BundleFilesDB = db
	t.Cleanup(func() { BundleFilesDB = prev })
}

// TestBundleToolsAreOnTheWorkerAllowList — servitor's worker is pinned to
// local-only tools, and a tool absent from the allow-list panics the app at
// session start rather than failing quietly.
func TestBundleToolsAreOnTheWorkerAllowList(t *testing.T) {
	withBundleDB(t)
	tools := BundleTools("u1", "b1")
	if len(tools) != 5 {
		t.Errorf("bundleCodeTools returned %d tools, want 5", len(tools))
	}
	for _, td := range tools {
		if !servitorWorkerToolAllowList[td.Tool.Name] {
			t.Errorf("tool %q is not on the worker allow-list", td.Tool.Name)
		}
	}
}
