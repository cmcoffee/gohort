package filestore

// The store root is a valid scope value: a binary that must run at the base
// of a tree has to be able to name that tree. What must NOT come with it is
// traversal reading as success — ".." cleans to the root too.

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestScopeResolvesTheRootItself(t *testing.T) {
	app, st, root := scopeFixture(t)

	// EvalSymlinks, because a store under a symlinked path (/tmp on macOS,
	// /var/folders underneath) resolves to the real one and the comparison
	// would otherwise fail for a reason that has nothing to do with scopes.
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, spelling := range []string{".", "", "/", "./", "  .  "} {
		got, err := app.resolveScope("u", st.Slug, spelling)
		if err != nil {
			t.Errorf("%q should name the root: %v", spelling, err)
			continue
		}
		if got != want {
			t.Errorf("%q: want %q, got %q", spelling, want, got)
		}
	}
}

// The reason this is an allowlist of spellings and not an equality-tolerant
// check inside resolveUnder: these clean to the root as well, and a caller
// probing with them should be told no rather than handed the store.
func TestScopeStillRefusesTraversalThatLandsOnTheRoot(t *testing.T) {
	app, st, _ := scopeFixture(t)

	for _, probe := range []string{"..", "../", "../..", "scan-2026-08-13/.."} {
		if got, err := app.resolveScope("u", st.Slug, probe); err == nil {
			t.Errorf("%q should be refused, resolved to %q", probe, got)
		}
	}
}

// Relaxing the root must not have relaxed anything else.
func TestScopeStillRefusesOutsideAndMissing(t *testing.T) {
	app, st, _ := scopeFixture(t)

	if _, err := app.resolveScope("u", st.Slug, "../../etc"); err == nil {
		t.Error("a path outside the store should still be refused")
	}
	if _, err := app.resolveScope("u", st.Slug, "no-such-folder"); err == nil {
		t.Error("a folder that is not there should still be refused")
	}
	if _, err := app.resolveScope("u", "no-such-store", "."); err == nil {
		t.Error("an unreachable store should still be refused, root or not")
	}
}

// A lister that omits the root leaves the one folder a caller cannot find by
// looking, now that it is nameable.
func TestScopeChoicesAdvertiseTheRoot(t *testing.T) {
	app, st, _ := scopeFixture(t)

	got := app.listScope("u", st.Slug)
	if len(got) == 0 || got[0] != "." {
		t.Fatalf("want \".\" listed first, got %v", got)
	}
	if !strings.Contains(strings.Join(got, ","), "scan-2026-08-13") {
		t.Errorf("the subfolders should still be listed: %v", got)
	}
}
