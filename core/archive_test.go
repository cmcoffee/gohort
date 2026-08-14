package core

// The expander treats an archive as untrusted input. These pin the ways
// it refuses one — which is the whole reason it was lifted here instead
// of written a second time.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tarGzWith writes a .tar.gz containing the given members.
func tarGzWith(t *testing.T, path string, members map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range members {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := gzip.NewWriter(f)
	defer zw.Close()
	if _, err := zw.Write(buf.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func TestExpandArchivesUnpacksAndRemovesTheArchive(t *testing.T) {
	dir := t.TempDir()
	tarGzWith(t, filepath.Join(dir, "capture.tar.gz"), map[string]string{
		"app.log":       "ERROR disk full\n",
		"conf/app.conf": "debug=true\n",
	})

	res, err := ExpandArchives(context.Background(), dir, ExpandLimits{})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if res.Opened != 1 {
		t.Errorf("expected one archive opened, got %d", res.Opened)
	}
	// The archive expands into a directory named for it, so an extracted
	// file still records where it came from.
	body, err := os.ReadFile(filepath.Join(dir, "capture.d", "app.log"))
	if err != nil {
		t.Fatalf("extracted file missing: %v", err)
	}
	if string(body) != "ERROR disk full\n" {
		t.Errorf("content wrong: %q", body)
	}
	if _, err := os.Stat(filepath.Join(dir, "capture.d", "conf", "app.conf")); err != nil {
		t.Errorf("nested member missing: %v", err)
	}
	// The archive itself is gone: it has been replaced by its contents.
	if _, err := os.Stat(filepath.Join(dir, "capture.tar.gz")); !os.IsNotExist(err) {
		t.Error("the archive should be removed once expanded")
	}
}

func TestExpandArchivesRefusesTraversalMembers(t *testing.T) {
	// "../../etc/cron.d/x" inside a customer's dump has to fail as a
	// refused member, not as a file written outside the tree.
	dir := t.TempDir()
	sentinel := filepath.Join(t.TempDir(), "escaped.txt")
	tarGzWith(t, filepath.Join(dir, "evil.tar.gz"), map[string]string{
		"../../escaped.txt": "pwned",
		"ok.log":            "fine\n",
	})

	res, err := ExpandArchives(context.Background(), dir, ExpandLimits{})
	if err != nil {
		t.Fatalf("a traversal member must not fail the whole expansion: %v", err)
	}
	if res.Skipped == 0 {
		t.Error("the traversing member was not counted as refused")
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("a member escaped the destination")
	}
	// The legitimate member still landed: one bad entry does not reject
	// the dump.
	if _, err := os.Stat(filepath.Join(dir, "evil.d", "ok.log")); err != nil {
		t.Errorf("the good member should still extract: %v", err)
	}
}

func TestExpandArchivesDoesNotRecreateLinks(t *testing.T) {
	// A tree that reconstructs a filesystem can point out of itself no
	// matter how carefully the names were checked, so links are counted
	// and dropped rather than made.
	dir := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd", Mode: 0o777})
	_ = tw.WriteHeader(&tar.Header{Name: "real.log", Typeflag: tar.TypeReg, Size: 3, Mode: 0o644})
	_, _ = tw.Write([]byte("hi\n"))
	tw.Close()

	f, _ := os.Create(filepath.Join(dir, "links.tar.gz"))
	zw := gzip.NewWriter(f)
	_, _ = zw.Write(buf.Bytes())
	zw.Close()
	f.Close()

	res, err := ExpandArchives(context.Background(), dir, ExpandLimits{})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if res.Skipped == 0 {
		t.Error("the symlink member was not counted as refused")
	}
	if _, err := os.Lstat(filepath.Join(dir, "links.d", "link")); err == nil {
		t.Error("a symlink was re-created out of an archive")
	}
	if _, err := os.Stat(filepath.Join(dir, "links.d", "real.log")); err != nil {
		t.Errorf("the regular member should still extract: %v", err)
	}
}

func TestExpandArchivesEnforcesTheByteBudget(t *testing.T) {
	// A declared size is a claim made by the archive, and the archive is
	// the untrusted party — so the copy is capped regardless of it.
	dir := t.TempDir()
	tarGzWith(t, filepath.Join(dir, "big.tar.gz"), map[string]string{
		"huge.log": strings.Repeat("x", 4096),
	})

	res, err := ExpandArchives(context.Background(), dir, ExpandLimits{MaxBytes: 512})
	if err == nil {
		t.Fatal("expected the budget to be enforced")
	}
	// A budget overrun ABORTS the pass rather than being downgraded to
	// one unreadable archive, so it comes back as a typed error the
	// caller can distinguish from a corrupt member.
	if !errors.Is(err, ErrExpandBudget) {
		t.Errorf("expected ErrExpandBudget, got: %v", err)
	}
	if res.Bytes > 4096 {
		t.Errorf("wrote %d bytes past a 512-byte budget", res.Bytes)
	}
}

func TestExpandArchivesNestsToTheDepthCap(t *testing.T) {
	// An archive containing itself is a cheap way to spend a disk.
	dir := t.TempDir()
	inner := filepath.Join(t.TempDir(), "inner.tar.gz")
	tarGzWith(t, inner, map[string]string{"deep.log": "found me\n"})
	innerBody, err := os.ReadFile(inner)
	if err != nil {
		t.Fatal(err)
	}
	tarGzWith(t, filepath.Join(dir, "outer.tar.gz"), map[string]string{
		"inner.tar.gz": string(innerBody),
	})

	if _, err := ExpandArchives(context.Background(), dir, ExpandLimits{}); err != nil {
		t.Fatalf("expand: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "outer.d", "inner.d", "deep.log")); err != nil {
		t.Errorf("nested archive was not expanded: %v", err)
	}

	// Depth 1 opens the outer one and stops.
	dir2 := t.TempDir()
	tarGzWith(t, filepath.Join(dir2, "outer.tar.gz"), map[string]string{
		"inner.tar.gz": string(innerBody),
	})
	if _, err := ExpandArchives(context.Background(), dir2, ExpandLimits{MaxDepth: 1}); err != nil {
		t.Fatalf("expand: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir2, "outer.d", "inner.d")); err == nil {
		t.Error("the depth cap did not stop the nested expansion")
	}
}

func TestExpandZipAndSingleFileStreams(t *testing.T) {
	dir := t.TempDir()

	zf, err := os.Create(filepath.Join(dir, "z.zip"))
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	w, _ := zw.Create("inside.log")
	_, _ = w.Write([]byte("from a zip\n"))
	zw.Close()
	zf.Close()

	// A lone .gz decompresses to the same name without the extension.
	gf, _ := os.Create(filepath.Join(dir, "rotated.log.gz"))
	gz := gzip.NewWriter(gf)
	_, _ = gz.Write([]byte("yesterday\n"))
	gz.Close()
	gf.Close()

	if _, err := ExpandArchives(context.Background(), dir, ExpandLimits{}); err != nil {
		t.Fatalf("expand: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(dir, "z.d", "inside.log")); err != nil || string(body) != "from a zip\n" {
		t.Errorf("zip member wrong: %q (%v)", body, err)
	}
	if body, err := os.ReadFile(filepath.Join(dir, "rotated.log")); err != nil || string(body) != "yesterday\n" {
		t.Errorf("gz stream wrong: %q (%v)", body, err)
	}
}

func TestSafeJoinAndUnopened(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"", "/etc/passwd", "../x", "a/../../x", `..\..\x`} {
		if _, ok := SafeJoin(root, bad); ok {
			t.Errorf("SafeJoin accepted %q", bad)
		}
	}
	if got, ok := SafeJoin(root, "a/b.log"); !ok || !strings.HasPrefix(got, root) {
		t.Errorf("SafeJoin refused a legitimate member: %q %v", got, ok)
	}
	// Formats with no built-in expander are reported rather than ignored:
	// a bundle that seems thin because half of it is in a .7z is a
	// different problem from one that is thin because nothing was caught.
	for _, name := range []string{"x.7z", "x.tar.xz", "dump.gpg", "y.zst"} {
		if !UnopenedArchive(name) {
			t.Errorf("%q should read as an archive we cannot open", name)
		}
	}
	if UnopenedArchive("app.log") || UnopenedArchive("x.tar.gz") {
		t.Error("a plain file or a supported archive is not 'unopened'")
	}
	if !ArchiveExpandable("x.tar.gz") || ArchiveExpandable("app.log") {
		t.Error("ArchiveExpandable disagrees with the expander table")
	}
}
