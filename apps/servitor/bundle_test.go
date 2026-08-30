package servitor

// What is left of the bundle suite after the store moved to core/bundle: the
// parts that are about SERVITOR's use of it rather than about the store.
//
// Each one names something the package still owns — its worker allow-list, the
// tools' argument parsing and rendering, the upload path's filename handling —
// so the split is legible from here rather than looking like tests that got
// lost.

import (
	"path/filepath"
	"strings"
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
	tools := bundleCodeTools("u1", "b1")
	if len(tools) != 5 {
		t.Errorf("bundleCodeTools returned %d tools, want 5", len(tools))
	}
	for _, td := range tools {
		if !servitorWorkerToolAllowList[td.Tool.Name] {
			t.Errorf("tool %q is not on the worker allow-list", td.Tool.Name)
		}
	}
}

// TestBundleToolsReportNotIngestedRatherThanEmpty — an empty store must read as
// "the evidence is not loaded", never as "your search found nothing", which an
// LLM would report as the absence of the thing searched for.
func TestBundleToolsReportNotIngestedRatherThanEmpty(t *testing.T) {
	withBundleDB(t)
	for _, td := range bundleCodeTools("u1", "empty") {
		args := map[string]any{}
		switch td.Tool.Name {
		case "search_bundle":
			args["pattern"] = "anything"
		case "read_bundle_file":
			args["path"] = "var/log/messages"
		}
		out, err := td.Handler(args)
		if err != nil {
			t.Errorf("%s on an empty store errored: %v", td.Tool.Name, err)
			continue
		}
		if !strings.Contains(out, "BUNDLE NOT INGESTED") {
			t.Errorf("%s on an empty store returned %q — it must say the bundle is not ingested", td.Tool.Name, out)
		}
	}
}

// TestParseBundleArgTimeRejectsGarbage — an unreadable window must fail loudly.
// Silently ignoring it turns "nothing happened in that hour" into a confident,
// wrong answer drawn from the whole bundle.
func TestParseBundleArgTimeRejectsGarbage(t *testing.T) {
	if _, err := parseBundleArgTime("last tuesday"); err == nil {
		t.Error("an unparseable time was accepted")
	}
	if ts, err := parseBundleArgTime(""); err != nil || !ts.IsZero() {
		t.Error("an empty time should mean no restriction, with no error")
	}
	for _, s := range []string{"2026-03-14", "2026-03-14 02:00", "2026-03-14 02:00:00", "2026-03-14T02:00:00Z"} {
		if _, err := parseBundleArgTime(s); err != nil {
			t.Errorf("%q was rejected: %v", s, err)
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
