// Turning a staged upload into an ingested bundle: expand the archives, walk
// what falls out, slice each text file into the encrypted store with its
// derived index, then delete the staging tree.
//
// Expansion is built-in only (gzip, bzip2, tar, zip). A dump that needs a
// site-specific tool to open — an encrypted blob, an xz archive, a vendor
// format — is out of scope here by design: running an operator-supplied command
// against an uploaded file is a permission question, not a decompression one,
// and it belongs behind whatever grant the calling app gates execution with,
// rather than inside an ingest pass. Such a file is stored as-is and reported unopened, so the gap is
// visible in the bundle listing instead of looking like an empty upload.
package bundle

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/core/archive"
	"github.com/cmcoffee/snugforge/nfo"
)

const (
	// MaxBytes caps the total EXPANDED size of one bundle. A dump that
	// exceeds it fails loudly mid-expansion rather than filling the volume.
	MaxBytes = 8 << 30 // 8 GiB
	// maxBundleFiles caps how many files one bundle may contain.
	maxBundleFiles = 50000
	// maxUnpackDepth bounds nested archives (a tarball of tarballs is normal
	// in a support dump; a tarball nested forty deep is an attack).
	maxUnpackDepth = 6
	// maxBundleFileBytes caps a single ingested TEXT file. Well above any
	// real log; present so one absurd file cannot own the whole budget.
	maxBundleFileBytes = 2 << 30 // 2 GiB
)

// IngestStats is what an ingest pass did, for the status line the user
// sees and the record fields the tools gate on.
type IngestStats struct {
	Files     int   // index entries written (text + recorded binaries)
	TextFiles int   // files actually sliced into the store
	Binaries  int   // recorded present, not ingested as text
	Unopened  int   // archives we have no built-in expander for
	Lines     int   // total lines ingested
	Bytes     int64 // total expanded bytes seen
	Skipped   int   // entries refused (traversal, symlink, over cap)
}

// binarySniff is how many leading bytes are checked for a NUL before a file is
// called binary. Its own constant here rather than shared with servitor's repo
// backend: the two happen to agree on 8000 today, and a shared const would make
// that agreement look load-bearing when it is a coincidence of two independent
// heuristics.
const binarySniff = 8000

// isBinary reports whether a leading chunk contains a NUL byte.
//
// Six lines, and servitor's repo backend has its own. Same reasoning as
// humanCount below: copying a self-contained heuristic across a package
// boundary is cheaper than a shared home for it, and the two are free to
// diverge if one ever learns something the other should not.
func isBinary(b []byte) bool {
	n := len(b)
	if n > binarySniff {
		n = binarySniff
	}
	return bytes.IndexByte(b[:n], 0) >= 0
}

// Ingest expands and ingests everything under stageDir into the bundle's
// encrypted store, then removes stageDir. The store is REPLACED, not merged: an
// upload is a new copy of the evidence, and half of one dump plus half of
// another is not a thing anyone wants to investigate.
func (b Bundle) Ingest(ctx context.Context, stageDir string) (IngestStats, error) {
	var st IngestStats
	store := b.store()
	if store == nil {
		return st, fmt.Errorf("no bundle store is configured")
	}
	if err := expandNested(ctx, stageDir, 0, &st); err != nil {
		return st, err
	}
	b.Wipe()

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
			nfo.Log("[bundle] skip %q: %v", p, err)
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
		bf, ingested, ierr := ingestOneFile(store, NormPath(rel), p, info, now)
		if ierr != nil {
			nfo.Log("[bundle] %q: %v", rel, ierr)
			st.Skipped++
			return nil
		}
		store.Set(filesTable, bf.Path, bf)
		st.Files++
		st.Bytes += bf.Bytes
		switch {
		case ingested:
			st.TextFiles++
			st.Lines += bf.Lines
		case bf.Format == FormatArchive:
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
	nfo.Log("[bundle] ingested %s/%s: %d files (%d text, %d binary, %d unopened), %s lines, %s",
		b.Owner, b.ID, st.Files, st.TextFiles, st.Binaries, st.Unopened, humanCount(st.Lines), nfo.HumanSize(st.Bytes))
	return st, nil
}

// FormatArchive marks a file we recognized as an archive but have no
// built-in expander for. Distinct from "binary" so the listing can say the
// difference between "a core dump, nothing to read" and "an archive nobody
// opened", which are different problems with different fixes.
const FormatArchive = "archive"

// FormatBinary marks a file that is present but not text.
const FormatBinary = "binary"

// ingestOneFile writes one file's slices and returns its index entry.
// Binary and unopened-archive files get an index entry with no slices, so they
// appear in the listing as present-but-unread rather than vanishing.
func ingestOneFile(store Store, relPath, absPath string, info os.FileInfo, now time.Time) (File, bool, error) {
	bf := File{
		Path:     relPath,
		Key:      fileKey(relPath),
		Bytes:    info.Size(),
		Ingested: now.Format(time.RFC3339),
		Severity: map[string]int{},
	}
	if relPath == "" {
		return bf, false, fmt.Errorf("empty path")
	}
	if archive.Unopened(relPath) {
		bf.Format = FormatArchive
		return bf, false, nil
	}
	if info.Size() == 0 {
		bf.Format = FormatText
		return bf, false, nil
	}
	if info.Size() > maxBundleFileBytes {
		bf.Format = FormatBinary
		return bf, false, fmt.Errorf("%s is %s, over the %s per-file cap", relPath, nfo.HumanSize(info.Size()), nfo.HumanSize(maxBundleFileBytes))
	}
	f, err := os.Open(absPath)
	if err != nil {
		return bf, false, err
	}
	defer f.Close()

	head := make([]byte, binarySniff)
	n, _ := io.ReadFull(f, head)
	if isBinary(head[:n]) {
		bf.Format = FormatBinary
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
		year    = yearFromInfo(info)
	)
	flush := func() {
		if len(slice) == 0 {
			return
		}
		store.Set(sliceTable, sliceKey(bf.Key, sliceNo), strings.Join(slice, "\n"))
		sliceNo++
		slice = slice[:0]
	}
	for {
		line, err := readLine(rd)
		if line == "" && err == io.EOF {
			break
		}
		lineNo++
		if len(sample) < formatSample {
			sample = append(sample, line)
			if len(sample) == formatSample {
				bf.Format = DetectFormat(sample)
			}
		}
		slice = append(slice, line)
		if len(slice) >= SliceLines {
			flush()
		}
		if sev := severityOf(line); sev != "" {
			bf.Severity[sev]++
		}
		if bf.Host == "" && bf.Format != "" {
			bf.Host = hostOf(bf.Format, line)
		}
		if bf.Format != "" {
			if ts, ok := ParseTime(bf.Format, year, line); ok {
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
		bf.Format = DetectFormat(sample)
	}
	// The sample lines were read before the format was known, so neither their
	// timestamps nor their host field were ever parsed. Re-read them under the
	// settled format — unconditionally, not just when nothing else was found:
	// the earliest timestamp in a log is overwhelmingly likely to be in its
	// first lines, which are exactly the ones the detection pass consumed. For
	// a file SHORTER than the sample window this pass is the only one that
	// runs, which is why it also fills the host.
	for _, ln := range sample {
		if ts, ok := ParseTime(bf.Format, year, ln); ok {
			if first.IsZero() || ts.Before(first) {
				first = ts
			}
			if last.IsZero() || ts.After(last) {
				last = ts
			}
		}
		if bf.Host == "" {
			bf.Host = hostOf(bf.Format, ln)
		}
	}
	bf.Lines = lineNo
	bf.Slices = sliceNo
	if !first.IsZero() {
		bf.First = first.Format(time.RFC3339)
		bf.Last = last.Format(time.RFC3339)
		bf.YearInferred = bf.Format == FormatSyslog
	}
	return bf, true, nil
}

// readLine reads one line, dropping the trailing newline and truncating a
// pathological one. Written against bufio.Reader rather than bufio.Scanner
// because a Scanner ERRORS on a line past its buffer, which would abandon the
// rest of a file over one minified blob; here the line is cut, marked, and the
// read continues.
func readLine(rd *bufio.Reader) (string, error) {
	var b strings.Builder
	truncated := false
	for {
		chunk, err := rd.ReadString('\n')
		if !truncated {
			room := maxLineBytes - b.Len()
			if len(chunk) > room {
				b.WriteString(strings.TrimRight(chunk[:room], "\n"))
				b.WriteString(truncMarker)
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

// yearFromInfo supplies the year for year-less formats, taken from the
// staged file's modification time. Recorded as inferred on the index entry
// (YearInferred) so nothing downstream presents it as read from the log.
func yearFromInfo(info os.FileInfo) int {
	if info == nil {
		return time.Now().UTC().Year()
	}
	return info.ModTime().UTC().Year()
}

// unopenedArchive reports whether a path looks like an archive this pass has no
// built-in expander for — the honest complement to expandNested.

// --- expansion ---

// expandNested expands everything under dir, in place, and folds the
// result into the ingest stats.
//
// The expander itself lives in core (core/archive.go): it was lifted out
// of here once the file store's upload path wanted the same thing, and
// a second implementation would have been the expensive mistake — this
// one knows an archive is untrusted input, and that knowledge is spread
// across every function in it rather than sitting in one check somebody
// could remember to copy.
func expandNested(ctx context.Context, dir string, depth int, st *IngestStats) error {
	res, err := archive.Expand(ctx, dir, archive.Limits{
		// The bundle budget is the WHOLE bundle's, and bytes already
		// ingested count against it, so the expander gets what is left
		// rather than the full cap.
		MaxBytes: MaxBytes - st.Bytes,
		MaxDepth: maxUnpackDepth - depth,
	})
	st.Bytes += res.Bytes
	st.Skipped += res.Skipped
	// An archive we could not open is reported as unopened, the same as
	// one we have no expander for: from the reader's side both mean
	// "there is more in here that nobody has read".
	st.Unopened += res.Unopened
	return err
}

// humanCount formats an integer with thousands separators (12345 -> "12,345").
//
// A copy of servitor's, deliberately. It is fifteen lines of pure formatting
// with no dependency, and the alternative — a shared package for one function,
// or an export from a hub this package cannot import — costs more than the
// duplication does. If a third caller wants it, that is the moment it earns a
// home of its own.
func humanCount(n int) string {
	s := fmt.Sprintf("%d", n)
	neg := ""
	if strings.HasPrefix(s, "-") {
		neg, s = "-", s[1:]
	}
	if len(s) <= 3 {
		return neg + s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteString(",")
		}
		b.WriteString(s[i : i+3])
	}
	return neg + b.String()
}
