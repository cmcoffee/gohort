package servitor

// What is left of the bundle suite after the store moved to core/bundle: the
// parts that are about SERVITOR's use of it rather than about the store.
//
// Two things survive here, and both are about an AGREEMENT between packages
// rather than about either side alone: that every tool core builds is on this
// app's worker allow-list, and that the upload path this app owns sanitizes a
// browser-supplied filename. The rest moved with the code they cover.

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

// TestSafeUploadNameSanitizes locks the browser-supplied filename down to a
// basename we are willing to create, while KEEPING the extension the expander
// dispatches on.
func TestSafeUploadNameSanitizes(t *testing.T) {
	cases := map[string]string{
		"dump.tar.gz":         "dump.tar.gz",
		"../../etc/passwd":    "passwd",
		`C:\Users\x\logs.zip`: "logs.zip",
		"weird;name|pipe.log": "weird_name_pipe.log",
		".hidden":             "hidden",
		"/":                   "",
		"..":                  "",
	}
	for in, want := range cases {
		if got := safeUploadName(in); got != want {
			t.Errorf("safeUploadName(%q) = %q, want %q", in, got, want)
		}
	}
}
