// bundle_ingest.go — turning a staged upload into an ingested bundle: expand
// the archives, walk what falls out, slice each text file into the encrypted
// store with its derived index, then delete the staging tree.
//
// Expansion is built-in only (gzip, bzip2, tar, zip). A dump that needs a
// site-specific tool to open — an encrypted blob, an xz archive, a vendor
// format — is out of scope here by design: running an operator-supplied command
// against an uploaded file is a permission question, not a decompression one,
// and it belongs behind the per-(agent, appliance) grant rather than inside an
// ingest pass. Such a file is stored as-is and reported unopened, so the gap is
// visible in the bundle listing instead of looking like an empty upload.
package servitor

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	// maxBundleBytes caps the total EXPANDED size of one bundle. A dump that
	// exceeds it fails loudly mid-expansion rather than filling the volume.
	maxBundleBytes = 8 << 30 // 8 GiB
	// maxBundleFiles caps how many files one bundle may contain.
	maxBundleFiles = 50000
	// maxUnpackDepth bounds nested archives (a tarball of tarballs is normal
	// in a support dump; a tarball nested forty deep is an attack).
	maxUnpackDepth = 6
	// maxBundleFileBytes caps a single ingested TEXT file. Well above any
	// real log; present so one absurd file cannot own the whole budget.
	maxBundleFileBytes = 2 << 30 // 2 GiB
)

// bundleIngestStats is what an ingest pass did, for the status line the user
// sees and the record fields the tools gate on.
type bundleIngestStats struct {
	Files     int   // index entries written (text + recorded binaries)
	TextFiles int   // files actually sliced into the store
	Binaries  int   // recorded present, not ingested as text
	Unopened  int   // archives we have no built-in expander for
	Lines     int   // total lines ingested
	Bytes     int64 // total expanded bytes seen
	Skipped   int   // entries refused (traversal, symlink, over cap)
}

// ingestBundleDir expands and ingests everything under stageDir into the
// bundle's encrypted store, then removes stageDir. The store is REPLACED, not
// merged: an upload is a new copy of the evidence, and half of one dump plus
// half of another is not a thing anyone wants to investigate.
func ingestBundleDir(ctx context.Context, user, applianceID, stageDir string) (bundleIngestStats, error) {
	var st bundleIngestStats
	store := bundleStore(user, applianceID)
	if store == nil {
		return st, fmt.Errorf("BundleFilesDB is not initialized")
	}
	if err := expandArchives(ctx, stageDir, 0, &st); err != nil {
		return st, err
	}
	wipeBundleFiles(user, applianceID)

	// st.Bytes was the running expansion budget; from here it becomes the
	// true expanded total, measured off what is actually on disk. Reset
	// rather than added to, or a file that arrived inside an archive would
	// be counted once when written and again when walked.
	st.Bytes = 0
	now := time.Now().UTC()
	err := filepath.WalkDir(stageDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			// One unreadable entry must not abandon the rest of the dump,
			// but it is logged: a silently skipped file reads downstream as
			// evidence that was never in the bundle.
			Log("[servitor.bundle] skip %q: %v", p, err)
			st.Skipped++
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil // symlinks and devices are never followed out of staging
		}
		if st.Files >= maxBundleFiles {
			st.Skipped++
			return nil
		}
		rel, rerr := filepath.Rel(stageDir, p)
		if rerr != nil {
			st.Skipped++
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			st.Skipped++
			return nil
		}
		bf, ingested, ierr := ingestOneBundleFile(store, normBundlePath(rel), p, info, now)
		if ierr != nil {
			Log("[servitor.bundle] %q: %v", rel, ierr)
			st.Skipped++
			return nil
		}
		store.Set(bundleFilesTable, bf.Path, bf)
		st.Files++
		st.Bytes += bf.Bytes
		switch {
		case ingested:
			st.TextFiles++
			st.Lines += bf.Lines
		case bf.Format == bundleFormatArchive:
			st.Unopened++
		default:
			st.Binaries++
		}
		return nil
	})
	os.RemoveAll(stageDir) // the plaintext copy does not outlive the ingest
	if err != nil {
		return st, err
	}
	Log("[servitor.bundle] ingested %s/%s: %d files (%d text, %d binary, %d unopened), %s lines, %s",
		user, applianceID, st.Files, st.TextFiles, st.Binaries, st.Unopened, humanCount(st.Lines), HumanSize(st.Bytes))
	return st, nil
}

// bundleFormatArchive marks a file we recognized as an archive but have no
// built-in expander for. Distinct from "binary" so the listing can say the
// difference between "a core dump, nothing to read" and "an archive nobody
// opened", which are different problems with different fixes.
const bundleFormatArchive = "archive"

// bundleFormatBinary marks a file that is present but not text.
const bundleFormatBinary = "binary"

// ingestOneBundleFile writes one file's slices and returns its index entry.
// Binary and unopened-archive files get an index entry with no slices, so they
// appear in the listing as present-but-unread rather than vanishing.
func ingestOneBundleFile(store Database, relPath, absPath string, info os.FileInfo, now time.Time) (bundleFile, bool, error) {
	bf := bundleFile{
		Path:     relPath,
		Key:      bundleFileKey(relPath),
		Bytes:    info.Size(),
		Ingested: now.Format(time.RFC3339),
		Severity: map[string]int{},
	}
	if relPath == "" {
		return bf, false, fmt.Errorf("empty path")
	}
	if unopenedArchive(relPath) {
		bf.Format = bundleFormatArchive
		return bf, false, nil
	}
	if info.Size() == 0 {
		bf.Format = bundleFormatText
		return bf, false, nil
	}
	if info.Size() > maxBundleFileBytes {
		bf.Format = bundleFormatBinary
		return bf, false, fmt.Errorf("%s is %s, over the %s per-file cap", relPath, HumanSize(info.Size()), HumanSize(maxBundleFileBytes))
	}
	f, err := os.Open(absPath)
	if err != nil {
		return bf, false, err
	}
	defer f.Close()

	head := make([]byte, binarySniff)
	n, _ := io.ReadFull(f, head)
	if isBinary(head[:n]) {
		bf.Format = bundleFormatBinary
		return bf, false, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return bf, false, err
	}

	// Two passes would mean reading a multi-gigabyte file twice, so format
	// detection runs on the first slice and is applied from there on. The
	// consequence is that lines before the format is known are re-examined
	// once, at the end of the first slice — cheap, and it keeps the whole
	// ingest to a single sequential read.
	rd := bufio.NewReaderSize(f, 1<<20)
	var (
		slice   []string
		sample  []string
		lineNo  int
		sliceNo int
		first   time.Time
		last    time.Time
		year    = bundleYearFromInfo(info)
	)
	flush := func() {
		if len(slice) == 0 {
			return
		}
		store.Set(bundleSliceTable, bundleSliceKey(bf.Key, sliceNo), strings.Join(slice, "\n"))
		sliceNo++
		slice = slice[:0]
	}
	for {
		line, err := readBundleLine(rd)
		if line == "" && err == io.EOF {
			break
		}
		lineNo++
		if len(sample) < bundleFormatSample {
			sample = append(sample, line)
			if len(sample) == bundleFormatSample {
				bf.Format = detectBundleFormat(sample)
			}
		}
		slice = append(slice, line)
		if len(slice) >= bundleSliceLines {
			flush()
		}
		if sev := bundleSeverity(line); sev != "" {
			bf.Severity[sev]++
		}
		if bf.Host == "" && bf.Format != "" {
			bf.Host = bundleHost(bf.Format, line)
		}
		if bf.Format != "" {
			if ts, ok := parseBundleTime(bf.Format, year, line); ok {
				if first.IsZero() || ts.Before(first) {
					first = ts
				}
				if last.IsZero() || ts.After(last) {
					last = ts
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return bf, false, err
		}
	}
	flush()
	if bf.Format == "" { // file shorter than the sample window
		bf.Format = detectBundleFormat(sample)
	}
	// The sample lines were read before the format was known, so neither their
	// timestamps nor their host field were ever parsed. Re-read them under the
	// settled format — unconditionally, not just when nothing else was found:
	// the earliest timestamp in a log is overwhelmingly likely to be in its
	// first lines, which are exactly the ones the detection pass consumed. For
	// a file SHORTER than the sample window this pass is the only one that
	// runs, which is why it also fills the host.
	for _, ln := range sample {
		if ts, ok := parseBundleTime(bf.Format, year, ln); ok {
			if first.IsZero() || ts.Before(first) {
				first = ts
			}
			if last.IsZero() || ts.After(last) {
				last = ts
			}
		}
		if bf.Host == "" {
			bf.Host = bundleHost(bf.Format, ln)
		}
	}
	bf.Lines = lineNo
	bf.Slices = sliceNo
	if !first.IsZero() {
		bf.First = first.Format(time.RFC3339)
		bf.Last = last.Format(time.RFC3339)
		bf.YearInferred = bf.Format == bundleFormatSyslog
	}
	return bf, true, nil
}

// readBundleLine reads one line, dropping the trailing newline and truncating a
// pathological one. Written against bufio.Reader rather than bufio.Scanner
// because a Scanner ERRORS on a line past its buffer, which would abandon the
// rest of a file over one minified blob; here the line is cut, marked, and the
// read continues.
func readBundleLine(rd *bufio.Reader) (string, error) {
	var b strings.Builder
	truncated := false
	for {
		chunk, err := rd.ReadString('\n')
		if !truncated {
			room := bundleMaxLineBytes - b.Len()
			if len(chunk) > room {
				b.WriteString(strings.TrimRight(chunk[:room], "\n"))
				b.WriteString(bundleTruncMarker)
				truncated = true
			} else {
				b.WriteString(chunk)
			}
		}
		if err != nil || strings.HasSuffix(chunk, "\n") {
			return strings.TrimRight(b.String(), "\r\n"), err
		}
	}
}

// bundleYearFromInfo supplies the year for year-less formats, taken from the
// staged file's modification time. Recorded as inferred on the index entry
// (YearInferred) so nothing downstream presents it as read from the log.
func bundleYearFromInfo(info os.FileInfo) int {
	if info == nil {
		return time.Now().UTC().Year()
	}
	return info.ModTime().UTC().Year()
}

// unopenedArchive reports whether a path looks like an archive this pass has no
// built-in expander for — the honest complement to expandArchives.
func unopenedArchive(p string) bool {
	l := strings.ToLower(p)
	for _, ext := range []string{".xz", ".txz", ".tar.xz", ".7z", ".rar", ".zst", ".tar.zst", ".lz4", ".enc", ".gpg", ".pgp", ".aes"} {
		if strings.HasSuffix(l, ext) {
			return true
		}
	}
	return false
}

// --- expansion ---

// expandArchives repeatedly expands archives found under dir, in place, until
// nothing is left to expand or the depth cap is reached. Each archive is
// replaced by a directory of the same name, so the bundle path of an extracted
// file records where it came from.
func expandArchives(ctx context.Context, dir string, depth int, st *bundleIngestStats) error {
	if depth >= maxUnpackDepth {
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
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fn := expanderFor(a)
		if fn == nil {
			continue
		}
		if err := fn(a, st); err != nil {
			// A corrupt member inside a dump is common and is not a reason
			// to reject the dump. The archive stays in place and is
			// reported as unopened.
			Log("[servitor.bundle] expand %q: %v", filepath.Base(a), err)
			st.Skipped++
			continue
		}
		os.Remove(a)
	}
	return expandArchives(ctx, dir, depth+1, st)
}

// bundleExpander expands one archive next to itself.
type bundleExpander func(path string, st *bundleIngestStats) error

// expanderFor picks the expander for a path, or nil when it is not an archive
// we open. Order matters: ".tar.gz" must be seen as a tar, not a gzip.
func expanderFor(p string) bundleExpander {
	l := strings.ToLower(p)
	switch {
	case strings.HasSuffix(l, ".tar.gz"), strings.HasSuffix(l, ".tgz"):
		return expandTarGz
	case strings.HasSuffix(l, ".tar.bz2"), strings.HasSuffix(l, ".tbz"), strings.HasSuffix(l, ".tbz2"):
		return expandTarBz2
	case strings.HasSuffix(l, ".tar"):
		return expandTar
	case strings.HasSuffix(l, ".zip"):
		return expandZip
	case strings.HasSuffix(l, ".gz"):
		return expandGz
	case strings.HasSuffix(l, ".bz2"):
		return expandBz2
	}
	return nil
}

// expandDirFor returns the directory an archive expands into: the archive's
// path with its extension replaced by ".d", so two archives whose names differ
// only by extension cannot collide.
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

func expandTarGz(p string, st *bundleIngestStats) error {
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
	return untar(tar.NewReader(zr), expandDirFor(p), st)
}

func expandTarBz2(p string, st *bundleIngestStats) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return untar(tar.NewReader(bzip2.NewReader(f)), expandDirFor(p), st)
}

func expandTar(p string, st *bundleIngestStats) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return untar(tar.NewReader(f), expandDirFor(p), st)
}

// untar extracts a tar stream into dest. Every member is checked against the
// destination root before anything is created: an archive is untrusted input,
// and "../../etc/cron.d/x" inside a customer's dump has to fail as a refused
// member rather than as a file written outside the staging tree.
func untar(tr *tar.Reader, dest string, st *bundleIngestStats) error {
	if err := os.MkdirAll(dest, 0700); err != nil {
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
		target, ok := safeJoin(dest, hdr.Name)
		if !ok {
			st.Skipped++
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, 0700)
		case tar.TypeReg:
			if err := writeExtracted(target, tr, hdr.Size, st); err != nil {
				return err
			}
		default:
			// Symlinks, hardlinks, devices, fifos: a bundle is read as data,
			// never re-created as a filesystem, so a link that would point
			// out of the tree simply is not made.
			st.Skipped++
		}
	}
}

func expandZip(p string, st *bundleIngestStats) error {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return err
	}
	defer zr.Close()
	dest := expandDirFor(p)
	if err := os.MkdirAll(dest, 0700); err != nil {
		return err
	}
	for _, f := range zr.File {
		target, ok := safeJoin(dest, f.Name)
		if !ok {
			st.Skipped++
			continue
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(target, 0700)
			continue
		}
		if !f.Mode().IsRegular() {
			st.Skipped++
			continue
		}
		rc, err := f.Open()
		if err != nil {
			st.Skipped++
			continue
		}
		err = writeExtracted(target, rc, int64(f.UncompressedSize64), st)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// expandGz decompresses a single gzip file to the same name without ".gz".
func expandGz(p string, st *bundleIngestStats) error {
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
	return writeExtracted(strings.TrimSuffix(p, filepath.Ext(p)), zr, -1, st)
}

// expandBz2 decompresses a single bzip2 file to the same name without ".bz2".
func expandBz2(p string, st *bundleIngestStats) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return writeExtracted(strings.TrimSuffix(p, filepath.Ext(p)), bzip2.NewReader(f), -1, st)
}

// writeExtracted copies one member out, enforcing the whole-bundle byte budget
// as it goes. declared is the member's advertised size (-1 when unknown, as for
// a raw compressed stream); the copy is capped regardless, because a declared
// size is a claim made by the archive and the archive is the untrusted party.
func writeExtracted(target string, src io.Reader, declared int64, st *bundleIngestStats) error {
	if declared >= 0 && st.Bytes+declared > maxBundleBytes {
		return fmt.Errorf("bundle exceeds the %s expanded-size cap", HumanSize(maxBundleBytes))
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	remaining := maxBundleBytes - st.Bytes
	n, err := io.Copy(out, io.LimitReader(src, remaining+1))
	st.Bytes += n
	if err != nil {
		return err
	}
	if n > remaining {
		return fmt.Errorf("bundle exceeds the %s expanded-size cap", HumanSize(maxBundleBytes))
	}
	return nil
}

// safeJoin resolves an archive member name under root, refusing anything that
// escapes it. Returns ok=false for absolute paths, traversal, and empty names.
func safeJoin(root, name string) (string, bool) {
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
