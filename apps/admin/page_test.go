package admin

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// adminPageSource is the admin page as one text: page.go plus every
// page_<area>.go builder and the web_assets.go blobs. The source-reading tests
// below used to open page.go alone, which held the whole page until it was cut
// into one file per tab area; a check that counts forms or looks for a help
// string has to see all of them or it reads a split as a regression.
func adminPageSource(t *testing.T) string {
	t.Helper()
	names, err := filepath.Glob("page*.go")
	if err != nil {
		t.Fatal(err)
	}
	names = append(names, "web_assets.go")
	sort.Strings(names)
	var all []byte
	for _, n := range names {
		if filepath.Ext(n) != ".go" || len(n) > 8 && n[len(n)-8:] == "_test.go" {
			continue
		}
		b, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, b...)
		all = append(all, '\n')
	}
	return string(all)
}

// TestToggleLabel: the redundant 0=off suffix is stripped from a bool tunable's
// label (any spacing / casing), while a plain label and a non-matching
// parenthetical are left untouched.
func TestToggleLabel(t *testing.T) {
	cases := map[string]string{
		"Finding conflict detection (0 = off)":  "Finding conflict detection",
		"Automatic entity extraction (0 = off)": "Automatic entity extraction",
		"Something (0=off)":                     "Something",
		"Something (0 = OFF)":                   "Something",
		"Grounding gate: unsourced figures":     "Grounding gate: unsourced figures",
		"Recall min score (0 = off)":            "Recall min score", // helper strips; caller only applies it to bool knobs
		"Chunk size (chars)":                    "Chunk size (chars)",
	}
	for in, want := range cases {
		if got := toggleLabel(in); got != want {
			t.Errorf("toggleLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// No browser prompt on an administrator page: a list to pick from is asked
// through the shared modal (uiChoose), so it carries the app's chrome.
func TestAdminPagesAskThroughTheSharedModal(t *testing.T) {
	for _, src := range []string{configureBackendPickAction, connectorFormDef} {
		if strings.Contains(src, "window.prompt(") || strings.Contains(src, "window.confirm(") {
			t.Errorf("an administrator page still uses a browser dialog:\n%s", src[:200])
		}
	}
}
