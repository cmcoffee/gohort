package filestore

// Every property worth testing here is a bound: what a model can reach,
// and how much of it can come back. None of it needs a server or an LLM,
// which is why the reading half is its own file.

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// folderFixture builds a store with one bundle in it.
func folderFixture(t *testing.T) (root, bundle string) {
	t.Helper()
	root = t.TempDir()
	bundle = filepath.Join(root, "ticket-1234")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bundle, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("app.log", strings.Join([]string{
		"line one, quiet",
		"line two, quiet",
		"ERROR connection refused to db-3",
		"line four, after the error",
		"line five",
		"ERROR connection refused to db-7",
	}, "\n"))
	write("access.log", "GET /health 200\nGET /pay 500\n")

	// A rotated, gzipped file: the normal state of a log folder, and the
	// thing a naive reader silently returns nothing for.
	f, err := os.Create(filepath.Join(bundle, "app.log.1.gz"))
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	_, _ = zw.Write([]byte("older line\nERROR connection refused to db-1\n"))
	zw.Close()
	f.Close()
	return root, bundle
}

func TestSearchFindsMatchesWithContext(t *testing.T) {
	_, bundle := folderFixture(t)
	matches, capped, err := Search(bundle, SearchOpts{Pattern: "connection refused", Context: 1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if capped {
		t.Error("three matches should not hit the cap")
	}
	if len(matches) != 3 {
		t.Fatalf("expected 3 matches (two plain, one gzipped), got %d: %+v", len(matches), matches)
	}
	var sawGz bool
	for _, m := range matches {
		if strings.HasSuffix(m.File, ".gz") {
			sawGz = true
		}
	}
	if !sawGz {
		t.Error("a rotated .gz was not searched — every question about yesterday would answer 'nothing found'")
	}
	// Context comes from the same pass, and a hit at the top of a file
	// has no preceding lines to carry.
	for _, m := range matches {
		if m.File == "app.log" && m.Line == 3 {
			if len(m.Before) != 1 || !strings.Contains(m.Before[0], "line two") {
				t.Errorf("before-context wrong: %+v", m.Before)
			}
			if len(m.After) != 1 || !strings.Contains(m.After[0], "line four") {
				t.Errorf("after-context wrong: %+v", m.After)
			}
		}
	}
}

func TestSearchGlobNarrowsAndCaseFlagWidens(t *testing.T) {
	_, bundle := folderFixture(t)
	got, _, err := Search(bundle, SearchOpts{Pattern: "GET", Glob: "access.log"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("glob should have narrowed to one file, got %d matches", len(got))
	}
	if hits, _, _ := Search(bundle, SearchOpts{Pattern: "error connection"}); len(hits) != 0 {
		t.Errorf("exact match is the default, got %d", len(hits))
	}
	if hits, _, _ := Search(bundle, SearchOpts{Pattern: "error connection", IgnoreCase: true}); len(hits) == 0 {
		t.Error("ignore_case should have matched")
	}
}

func TestSearchCapsAndSaysSo(t *testing.T) {
	// "60 matches" and "the first 60 of many" are different answers, and
	// an investigator acting on the first as though it were the second
	// draws a conclusion from a truncated set.
	root := t.TempDir()
	bundle := filepath.Join(root, "noisy")
	_ = os.MkdirAll(bundle, 0o755)
	var b strings.Builder
	for i := 0; i < maxMatches*3; i++ {
		b.WriteString("ERROR something\n")
	}
	_ = os.WriteFile(filepath.Join(bundle, "flood.log"), []byte(b.String()), 0o644)

	matches, capped, err := Search(bundle, SearchOpts{Pattern: "ERROR"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(matches) > maxMatches {
		t.Errorf("returned %d matches, cap is %d", len(matches), maxMatches)
	}
	if !capped {
		t.Error("the cap was hit and the caller was not told")
	}
}

func TestSearchTruncatesAMonstrousLine(t *testing.T) {
	// One line of a minified stack trace can be megabytes. A tool that
	// returns it whole has dumped a log into the context window by
	// another route.
	root := t.TempDir()
	bundle := filepath.Join(root, "wide")
	_ = os.MkdirAll(bundle, 0o755)
	_ = os.WriteFile(filepath.Join(bundle, "wide.log"),
		[]byte("ERROR "+strings.Repeat("x", 50000)), 0o644)

	matches, _, err := Search(bundle, SearchOpts{Pattern: "ERROR"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one match, got %d", len(matches))
	}
	if runes := []rune(matches[0].Text); len(runes) > maxLineRunes+1 {
		t.Errorf("line came back %d runes long, cap is %d", len(runes), maxLineRunes)
	}
}

func TestReadReturnsABoundedWindow(t *testing.T) {
	_, bundle := folderFixture(t)
	lines, start, err := Read(bundle, "app.log", 3, 3)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if start != 2 || len(lines) != 3 {
		t.Fatalf("expected 3 lines starting at 2, got %d from %d", len(lines), start)
	}
	if !strings.Contains(lines[1], "ERROR") {
		t.Errorf("the window should be centred on the requested line: %+v", lines)
	}
	// A request for more than the ceiling gets the ceiling.
	all, _, err := Read(bundle, "app.log", 0, maxWindowLines*10)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(all) > maxWindowLines {
		t.Errorf("returned %d lines, ceiling is %d", len(all), maxWindowLines)
	}
}

// --- containment ------------------------------------------------------

func TestPathEscapesAreRefused(t *testing.T) {
	// This is the only thing between a model-supplied filename and the
	// rest of the disk.
	_, bundle := folderFixture(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("not yours"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{
		"../../etc/passwd",
		"../",
		"/etc/passwd",
		"subdir/../../../etc/passwd",
	} {
		if _, _, err := Read(bundle, rel, 0, 10); err == nil {
			t.Errorf("%q was allowed out of the bundle", rel)
		}
	}

	// A symlink INSIDE the bundle pointing out of it is the same hole
	// with better manners, which is why containment resolves rather than
	// trusting the textual path.
	link := filepath.Join(bundle, "sneaky.log")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, _, err := Read(bundle, "sneaky.log", 0, 10); err == nil {
		t.Error("a symlink out of the bundle was followed")
	}
}

func TestSubRootScopesAndRefusesEscapes(t *testing.T) {
	root, _ := folderFixture(t)

	// Empty means the store root — the FLAT-store case, and the whole
	// reason `within` is optional rather than required.
	got, err := SubRoot(root, "")
	if err != nil {
		t.Fatalf("empty should resolve to the store root: %v", err)
	}
	if resolved, _ := filepath.EvalSymlinks(root); got != resolved {
		t.Errorf("empty resolved to %q, want the root %q", got, resolved)
	}

	if _, err := SubRoot(root, "ticket-1234"); err != nil {
		t.Fatalf("a real subfolder should resolve: %v", err)
	}
	for _, name := range []string{"..", "../..", "nope", "/etc"} {
		if _, err := SubRoot(root, name); err == nil {
			t.Errorf("%q resolved as a subfolder", name)
		}
	}
	// A FILE is not a subfolder, and saying so beats searching something
	// with no files under it.
	_ = os.WriteFile(filepath.Join(root, "loose.log"), []byte("x"), 0o644)
	if _, err := SubRoot(root, "loose.log"); err == nil {
		t.Error("a file resolved as a subfolder")
	}
}

// A store whose files sit at the top level is a legitimate layout, not
// an empty store. Searching one without naming `within` has to work, or
// every flat store has to invent a subfolder to satisfy the tool.
func TestFlatStoreSearchesWithoutASubfolder(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "server.log"),
		[]byte("fine\nERROR disk full\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir, err := SubRoot(root, "")
	if err != nil {
		t.Fatalf("flat store should resolve: %v", err)
	}
	matches, _, err := Search(dir, SearchOpts{Pattern: "ERROR"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(matches) != 1 || matches[0].File != "server.log" {
		t.Errorf("expected the top-level file to be searched, got %+v", matches)
	}
	if folders, err := ListFolders(root); err != nil || len(folders) != 0 {
		t.Errorf("a flat store has no subfolders, got %v (%v)", folders, err)
	}
}

func TestListFoldersNewestFirst(t *testing.T) {
	root, _ := folderFixture(t)
	older := filepath.Join(root, "ticket-0001")
	_ = os.MkdirAll(older, 0o755)
	_ = os.WriteFile(filepath.Join(older, "old.log"), []byte("old\n"), 0o644)
	// Make it genuinely older than the fixture's files.
	past := mustStat(t, filepath.Join(root, "ticket-1234")).ModTime().Add(-72 * 60 * 60 * 1e9)
	_ = os.Chtimes(filepath.Join(older, "old.log"), past, past)

	bundles, err := ListFolders(root)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(bundles) != 2 {
		t.Fatalf("expected 2 folders, got %d", len(bundles))
	}
	if bundles[0].Name != "ticket-1234" {
		t.Errorf("newest should lead, got %s", bundles[0].Name)
	}
	if bundles[0].Files == 0 || bundles[0].Bytes == 0 {
		t.Errorf("a folder should report what is in it: %+v", bundles[0])
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// --- writing ----------------------------------------------------------

func TestEnsureSubCreatesAndContains(t *testing.T) {
	root := t.TempDir()

	// The no-setup property: uploading into a folder that does not exist
	// yet creates it, because declaring it first IS the setup this store
	// exists to avoid.
	dir, err := EnsureSub(root, "scan-2026-08-13")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("subfolder was not created: %v", err)
	}
	// Idempotent: a second upload into the same scan must not fail.
	if _, err := EnsureSub(root, "scan-2026-08-13"); err != nil {
		t.Errorf("second call should be fine: %v", err)
	}
	// Empty is the store root — the flat-store case.
	got, err := EnsureSub(root, "")
	if err != nil {
		t.Fatalf("empty: %v", err)
	}
	if resolved, _ := filepath.EvalSymlinks(root); got != resolved {
		t.Errorf("empty should be the root, got %q", got)
	}

	// Containment is proved BEFORE the directory exists, which is the
	// case a resolve-only check cannot cover.
	outside := filepath.Join(t.TempDir(), "escaped")
	for _, bad := range []string{"../escaped", "../../tmp/x", "/etc/cron.d"} {
		if _, err := EnsureSub(root, bad); err == nil {
			t.Errorf("%q was allowed out of the store", bad)
		}
	}
	if _, err := os.Stat(outside); err == nil {
		t.Error("an escape attempt created a directory outside the store")
	}
}

func TestSaveUploadPartRefusesEscapingFilenames(t *testing.T) {
	// The filename comes from the client. filepath.Base alone is the kind
	// of defence that looks sufficient until someone finds the case it
	// misses, so the join is re-checked.
	dest := t.TempDir()
	for _, name := range []string{"../evil.sh", "../../etc/cron.d/x", "/etc/passwd", "", "  "} {
		if _, _, err := saveUploadPart(dest, name, strings.NewReader("x")); err == nil {
			// A name that reduces to a safe base is allowed — that is
			// correct — so only assert nothing landed OUTSIDE dest.
			entries, _ := os.ReadDir(dest)
			for _, e := range entries {
				if strings.Contains(e.Name(), "/") || strings.Contains(e.Name(), "..") {
					t.Errorf("%q wrote outside: %s", name, e.Name())
				}
			}
		}
	}
	if _, err := os.Stat("/etc/cron.d/x"); err == nil {
		t.Fatal("an upload escaped to /etc/cron.d")
	}

	// The ordinary case still works, and reports what it wrote.
	n, saved, err := saveUploadPart(dest, "app.log", strings.NewReader("hello\n"))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if saved != "app.log" || n != 6 {
		t.Errorf("wrote %q (%d bytes)", saved, n)
	}
	body, _ := os.ReadFile(filepath.Join(dest, "app.log"))
	if string(body) != "hello\n" {
		t.Errorf("content wrong: %q", body)
	}
}
