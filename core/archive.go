// Expanding archives that arrived from somewhere else.
//
// Lifted out of servitor's bundle ingest, which had the only
// implementation, once a second caller (the file store's upload path)
// wanted the same thing. A second expander would have been the more
// expensive mistake: this one already knows that an archive is UNTRUSTED
// INPUT, and that knowledge is spread across every function here rather
// than concentrated in one check somebody could remember to copy.
//
// What "untrusted" means in practice, and why each rule exists:
//
//   - Every member name is resolved against the destination root BEFORE
//     anything is created. "../../etc/cron.d/x" inside a customer's dump
//     has to fail as a refused member, not as a file written outside the
//     tree.
//   - Symlinks, hardlinks, devices and fifos are never re-created. The
//     contents are read as DATA; a tree that reconstructs a filesystem
//     can point out of itself no matter how carefully the names were
//     checked.
//   - The declared size of a member is a claim made by the archive, so
//     the copy is capped regardless of it. A zip bomb declares whatever
//     it likes.
//   - Nesting is depth-capped, because an archive containing itself is a
//     cheap way to spend a disk.
//
// A corrupt member is common in a real dump and is NOT a reason to
// reject the dump: the archive stays in place, gets counted, and the
// caller decides.

package core

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Defaults, used when ExpandLimits leaves a field at zero.
const (
	defaultExpandBytes = 8 << 30 // 8 GiB of expanded output
	defaultExpandDepth = 6       // nested archives
)

// ExpandLimits bounds one expansion pass.
type ExpandLimits struct {
	// MaxBytes caps TOTAL expanded output across the whole pass, not per
	// file. Zero uses the default.
	MaxBytes int64
	// MaxDepth caps nested expansion (an archive inside an archive).
	// Zero uses the default.
	MaxDepth int
}

func (l ExpandLimits) bytes() int64 {
	if l.MaxBytes > 0 {
		return l.MaxBytes
	}
	return defaultExpandBytes
}

func (l ExpandLimits) depth() int {
	if l.MaxDepth > 0 {
		return l.MaxDepth
	}
	return defaultExpandDepth
}

// ErrExpandBudget is returned when a pass exceeds its byte cap.
//
// It ABORTS the whole pass rather than being counted as one archive that
// would not open, which is the behavior this inherited and the one place
// it was changed on the way here. The two failures are different: a
// corrupt member is local and the rest of the dump is still worth
// reading, while a budget overrun says the next archive will fail the
// same way. Swallowing it meant a directory of fifty oversized captures
// did fifty futile expansions and reported them as fifty unreadable
// files.
var ErrExpandBudget = Error("expansion exceeded its size budget")

// ExpandResult reports what a pass did.
//
// Skipped and Unopened are separate on purpose: "we refused this" and
// "we do not know how to open this" are different facts, and a caller
// that reports a thin result needs to say which happened.
type ExpandResult struct {
	Opened   int   // archives expanded
	Unopened int   // archives left alone (no built-in expander, or corrupt)
	Skipped  int   // members refused: traversal, symlink, over cap
	Bytes    int64 // expanded bytes written
}

// ExpandArchives repeatedly expands archives found under dir, in place,
// until nothing is left to expand or the depth cap is reached.
//
// Each archive is replaced by a directory of the same name plus ".d", so
// the path of an extracted file still records where it came from — which
// is what lets a search result say "this line came out of logs.tar.gz"
// rather than losing the provenance at unpack time.
func ExpandArchives(ctx context.Context, dir string, lim ExpandLimits) (ExpandResult, error) {
	run := &expandRun{lim: lim}
	err := run.pass(ctx, dir, 0)
	return run.res, err
}

// ArchiveExpandable reports whether there is a built-in expander for a
// path.
func ArchiveExpandable(path string) bool { return expanderFor(path) != nil }

// UnopenedArchive reports whether a path LOOKS like an archive we have
// no expander for — encrypted, or a format not built in.
//
// Worth reporting rather than ignoring: a bundle that seems thin because
// half of it is in a .7z is a different problem from one that is thin
// because nothing was captured, and only this can tell them apart.
func UnopenedArchive(path string) bool {
	l := strings.ToLower(path)
	for _, ext := range []string{".xz", ".txz", ".tar.xz", ".7z", ".rar", ".zst", ".tar.zst", ".lz4", ".enc", ".gpg", ".pgp", ".aes"} {
		if strings.HasSuffix(l, ext) {
			return true
		}
	}
	return false
}

// SafeJoin resolves an archive member name under root, refusing anything
// that escapes it. ok=false for absolute paths, traversal, and empty
// names.
//
// Backslashes are normalized first: an archive written on Windows uses
// them as separators, and treating "..\\..\\etc" as a filename rather
// than a path is how a traversal check gets walked straight past.
func SafeJoin(root, name string) (string, bool) {
	name = strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
	if name == "" || strings.HasPrefix(name, "/") {
		return "", false
	}
	clean := filepath.Clean(filepath.Join(root, name))
	rootClean := filepath.Clean(root) + string(os.PathSeparator)
	if !strings.HasPrefix(clean, rootClean) {
		return "", false
	}
	return clean, true
}

// --- the pass ---------------------------------------------------------

type expandRun struct {
	lim ExpandLimits
	res ExpandResult
}

func (r *expandRun) pass(ctx context.Context, dir string, depth int) error {
	if depth >= r.lim.depth() {
		return nil
	}
	var archives []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if expanderFor(p) != nil {
			archives = append(archives, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(archives) == 0 {
		return nil
	}
	for _, a := range archives {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		fn := expanderFor(a)
		if fn == nil {
			continue
		}
		if err := fn(r, a); err != nil {
			// The budget is a property of the PASS, so hitting it stops
			// everything; a corrupt member is local, so the archive
			// stays in place, gets counted, and the rest is still read.
			if errors.Is(err, ErrExpandBudget) {
				return err
			}
			Log("[archive] expand %q: %v", filepath.Base(a), err)
			r.res.Unopened++
			continue
		}
		r.res.Opened++
		os.Remove(a)
	}
	return r.pass(ctx, dir, depth+1)
}

// expander expands one archive next to itself.
type expander func(r *expandRun, path string) error

// expanderFor picks the expander for a path, or nil when it is not an
// archive we open. Order matters: ".tar.gz" must be seen as a tar, not a
// gzip.
func expanderFor(p string) expander {
	l := strings.ToLower(p)
	switch {
	case strings.HasSuffix(l, ".tar.gz"), strings.HasSuffix(l, ".tgz"):
		return (*expandRun).tarGz
	case strings.HasSuffix(l, ".tar.bz2"), strings.HasSuffix(l, ".tbz"), strings.HasSuffix(l, ".tbz2"):
		return (*expandRun).tarBz2
	case strings.HasSuffix(l, ".tar"):
		return (*expandRun).tarPlain
	case strings.HasSuffix(l, ".zip"):
		return (*expandRun).zipFile
	case strings.HasSuffix(l, ".gz"):
		return (*expandRun).gzFile
	case strings.HasSuffix(l, ".bz2"):
		return (*expandRun).bz2File
	}
	return nil
}

// expandDirFor returns the directory an archive expands into: the
// archive's path with its extension replaced by ".d", so two archives
// whose names differ only by extension cannot collide.
func expandDirFor(archive string) string {
	base := archive
	for _, ext := range []string{".tar.gz", ".tar.bz2", ".tgz", ".tbz2", ".tbz", ".tar", ".zip"} {
		if strings.HasSuffix(strings.ToLower(base), ext) {
			base = base[:len(base)-len(ext)]
			break
		}
	}
	return base + ".d"
}

func (r *expandRun) tarGz(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer zr.Close()
	return r.untar(tar.NewReader(zr), expandDirFor(p))
}

func (r *expandRun) tarBz2(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return r.untar(tar.NewReader(bzip2.NewReader(f)), expandDirFor(p))
}

func (r *expandRun) tarPlain(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return r.untar(tar.NewReader(f), expandDirFor(p))
}

func (r *expandRun) untar(tr *tar.Reader, dest string) error {
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return err
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, ok := SafeJoin(dest, hdr.Name)
		if !ok {
			r.res.Skipped++
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, 0o700)
		case tar.TypeReg:
			if err := r.write(target, tr, hdr.Size); err != nil {
				return err
			}
		default:
			// Symlinks, hardlinks, devices, fifos: the contents are read
			// as data, never re-created as a filesystem, so a link that
			// would point out of the tree simply is not made.
			r.res.Skipped++
		}
	}
}

func (r *expandRun) zipFile(p string) error {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return err
	}
	defer zr.Close()
	dest := expandDirFor(p)
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return err
	}
	for _, f := range zr.File {
		target, ok := SafeJoin(dest, f.Name)
		if !ok {
			r.res.Skipped++
			continue
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(target, 0o700)
			continue
		}
		if !f.Mode().IsRegular() {
			r.res.Skipped++
			continue
		}
		rc, err := f.Open()
		if err != nil {
			r.res.Skipped++
			continue
		}
		err = r.write(target, rc, int64(f.UncompressedSize64))
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// gzFile decompresses a single gzip file to the same name without ".gz".
func (r *expandRun) gzFile(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer zr.Close()
	return r.write(strings.TrimSuffix(p, filepath.Ext(p)), zr, -1)
}

// bz2File decompresses a single bzip2 file to the same name without
// ".bz2".
func (r *expandRun) bz2File(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return r.write(strings.TrimSuffix(p, filepath.Ext(p)), bzip2.NewReader(f), -1)
}

// write copies one member out, enforcing the whole-pass byte budget as
// it goes.
//
// declared is the member's advertised size (-1 when unknown, as for a
// raw compressed stream). The copy is capped regardless of it, because a
// declared size is a claim made by the archive and the archive is the
// untrusted party.
func (r *expandRun) write(target string, src io.Reader, declared int64) error {
	budget := r.lim.bytes()
	if declared >= 0 && r.res.Bytes+declared > budget {
		return fmt.Errorf("%w (%s)", ErrExpandBudget, HumanSize(budget))
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	remaining := budget - r.res.Bytes
	n, err := io.Copy(out, io.LimitReader(src, remaining+1))
	r.res.Bytes += n
	if err != nil {
		return err
	}
	if n > remaining {
		return fmt.Errorf("%w (%s)", ErrExpandBudget, HumanSize(budget))
	}
	return nil
}
