package filestore

// The path scope is what lets ANOTHER app's tool take a folder name as a
// parameter and have it checked when the tool runs. Quoting is not
// containment, and an enum cannot express a set that changes — which is
// the whole point of a drop folder.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func scopeFixture(t *testing.T) (*FileStoreApp, Store, string) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"scan-2026-08-13", "scan-2026-08-12"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	st, err := SaveStore(app.DB, Store{Name: "Support bundles", Path: root})
	if err != nil {
		t.Fatalf("save store: %v", err)
	}
	return app, st, root
}

func TestScopeResolvesAFolderToAnAbsolutePath(t *testing.T) {
	app, st, root := scopeFixture(t)

	got, err := app.resolveScope("u", st.Slug, "scan-2026-08-13")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// Absolute, so a template gets something unambiguous regardless of
	// what working directory the target happens to have.
	if !filepath.IsAbs(got) {
		t.Errorf("expected an absolute path, got %q", got)
	}
	if resolved, _ := filepath.EvalSymlinks(filepath.Join(root, "scan-2026-08-13")); got != resolved {
		t.Errorf("resolved to %q, want %q", got, resolved)
	}
}

func TestScopeRefusesWhatQuotingWouldHaveAllowed(t *testing.T) {
	app, st, _ := scopeFixture(t)

	// Each of these is a perfectly well-formed single argument that
	// shell-quoting would pass through untouched.
	for _, bad := range []string{"../", "..", "../../etc", "/etc", "nope", ""} {
		if _, err := app.resolveScope("u", st.Slug, bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	// And the refusal says what would change the answer, because a model
	// reads it and has to act on it.
	_, err := app.resolveScope("u", st.Slug, "nope")
	if err == nil || !strings.Contains(err.Error(), "Support bundles") {
		t.Errorf("the refusal should name the store to look in, got: %v", err)
	}
}

func TestScopeListsWhatIsValidNow(t *testing.T) {
	app, st, root := scopeFixture(t)

	got := app.listScope("u", st.Slug)
	if len(got) != 2 {
		t.Fatalf("expected both folders, got %v", got)
	}
	// The late-binding property: a folder that appears after the tool was
	// authored is immediately valid, which is the case an enum cannot
	// cover.
	if err := os.MkdirAll(filepath.Join(root, "scan-2026-08-14"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := app.listScope("u", st.Slug); len(got) != 3 {
		t.Errorf("a new folder should be valid without re-authoring, got %v", got)
	}
	if _, err := app.resolveScope("u", st.Slug, "scan-2026-08-14"); err != nil {
		t.Errorf("the new folder should resolve: %v", err)
	}
}

func TestScopeIsRegisteredUnderFiles(t *testing.T) {
	// The kind is what a tool parameter names ("files:<store>"), so the
	// registration and the declaration have to agree.
	if !PathScopeKnown("files:anything") {
		t.Error("the files path scope is not registered")
	}
	// An unknown kind fails CLOSED: a parameter declaring a constraint
	// nobody implements must not quietly become unconstrained.
	if _, err := ResolvePathScope("u", "nosuchkind:x", "value"); err == nil {
		t.Error("an unknown scope kind should refuse rather than pass")
	}
}
