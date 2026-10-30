// Files a record keeps: what an app's agent made for one record and the
// person wants beside it, such as each rendered take of a song.
//
// An app's assets are the app's own (its artwork, its sounds), written by its
// owner and shown to everyone it is shared with. These are the opposite: one
// person's, on one of their records, in a folder of that record's own, and
// gone with it. The record lists them in one of its fields (RecordFile), so a
// take is part of the song it was rendered from, not a second thing to find
// and connect back.

package appassets

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	recordFilesDirMu sync.RWMutex
	recordFilesDir   string
)

// SetRecordFilesDir configures the base directory holding records' files.
// Wired at startup beside SetAppAssetsDir.
func SetRecordFilesDir(dir string) {
	recordFilesDirMu.Lock()
	recordFilesDir = dir
	recordFilesDirMu.Unlock()
}

// RecordFilesDir is the configured base, or "" when unset: records keep no
// files on such a deployment.
func RecordFilesDir() string {
	recordFilesDirMu.RLock()
	defer recordFilesDirMu.RUnlock()
	return recordFilesDir
}

// RecordFile is one kept file as its record lists it.
type RecordFile struct {
	Name  string `json:"name"`  // the file's name in the record's folder
	Title string `json:"title"` // which one it is, as whoever kept it said
	Type  string `json:"type"`  // its content type
	Size  int64  `json:"size"`
	Added string `json:"added"` // RFC 3339
}

// MaxRecordFileBytes caps one kept file: a few minutes of rendered audio or a
// short clip, not a film.
const MaxRecordFileBytes = 64 << 20

// MaxRecordFiles caps how many files one record keeps.
const MaxRecordFiles = 40

// recordFileExts are the kinds a record keeps, each something a browser
// plays or shows and nothing that runs: no SVG, which can carry script.
var recordFileExts = map[string]string{
	".mp3":  "audio/mpeg",
	".wav":  "audio/wav",
	".ogg":  "audio/ogg",
	".m4a":  "audio/mp4",
	".flac": "audio/flac",
	".aac":  "audio/aac",
	".opus": "audio/ogg",
	".mp4":  "video/mp4",
	".webm": "video/webm",
	".mov":  "video/quicktime",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".pdf":  "application/pdf",
}

// RecordFileType is the content type a kept file is served as, and whether a
// record keeps that kind of file at all.
func RecordFileType(name string) (string, bool) {
	ct, ok := recordFileExts[strings.ToLower(filepath.Ext(name))]
	return ct, ok
}

// RecordFileKinds lists the extensions a record keeps, for a refusal to name.
func RecordFileKinds() string {
	return ".mp3 .wav .ogg .m4a .flac .aac .opus .mp4 .webm .mov .png .jpg .jpeg .gif .webp .pdf"
}

// validRecordFileName reports whether name is a flat name of a kept kind.
func validRecordFileName(name string) bool {
	if name == "" || len(name) > 128 || strings.HasPrefix(name, ".") {
		return false
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return false
	}
	_, ok := RecordFileType(name)
	return ok
}

// hashPart names a folder after a value that may hold anything (a username
// with an @, a record key someone typed), so it can never traverse.
func hashPart(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

// recordFileDir is the folder of one person's record in one app.
func recordFileDir(owner, slug, user, record string) (string, error) {
	base, err := appRecordFilesDir(owner, slug)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(user) == "" || strings.TrimSpace(record) == "" {
		return "", fmt.Errorf("a kept file needs a person and a record")
	}
	return filepath.Join(base, hashPart(user), hashPart(record)), nil
}

// appRecordFilesDir is the folder holding every record's files in one app.
func appRecordFilesDir(owner, slug string) (string, error) {
	base := RecordFilesDir()
	if base == "" {
		return "", fmt.Errorf("records cannot keep files on this deployment")
	}
	owner = strings.TrimSpace(owner)
	slug = strings.TrimSpace(slug)
	if owner == "" || slug == "" {
		return "", fmt.Errorf("owner and slug are required")
	}
	for label, part := range map[string]string{"owner": owner, "slug": slug} {
		if strings.ContainsAny(part, `/\`) || strings.Contains(part, "..") || part == "." {
			return "", fmt.Errorf("invalid %s for a kept file's path: %q", label, part)
		}
	}
	return filepath.Join(base, owner, slug), nil
}

var unsafeNameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// keptName is the name a file is kept under: its own name made flat and
// plain, numbered when the record already holds one by that name.
func keptName(dir, src string) string {
	ext := strings.ToLower(filepath.Ext(src))
	stem := strings.Trim(unsafeNameChars.ReplaceAllString(strings.TrimSuffix(filepath.Base(src), filepath.Ext(src)), "-"), "-.")
	if stem == "" {
		stem = "file"
	}
	if len(stem) > 80 {
		stem = stem[:80]
	}
	name := stem + ext
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
			return name
		}
		name = fmt.Sprintf("%s-%d%s", stem, i, ext)
	}
}

// KeepRecordFile copies the file at src into the record's folder and returns
// the entry the record lists it by. The file has to be a kind a record keeps
// and begin the way that kind does.
func KeepRecordFile(owner, slug, user, record, src, title string) (RecordFile, error) {
	ct, ok := RecordFileType(src)
	if !ok {
		return RecordFile{}, fmt.Errorf("%q is not a kind of file a record keeps: %s", filepath.Base(src), RecordFileKinds())
	}
	info, err := os.Stat(src)
	if err != nil {
		return RecordFile{}, err
	}
	if info.IsDir() {
		return RecordFile{}, fmt.Errorf("%q is a folder, not a file", filepath.Base(src))
	}
	if info.Size() == 0 {
		return RecordFile{}, fmt.Errorf("%q is empty", filepath.Base(src))
	}
	if info.Size() > MaxRecordFileBytes {
		return RecordFile{}, fmt.Errorf("%q is %d bytes, over the %d MiB a record keeps per file", filepath.Base(src), info.Size(), MaxRecordFileBytes>>20)
	}
	in, err := os.Open(src)
	if err != nil {
		return RecordFile{}, err
	}
	defer in.Close()
	head := make([]byte, 4096)
	n, _ := io.ReadFull(in, head)
	if err := assetMatchesType(filepath.Base(src), head[:n]); err != nil {
		return RecordFile{}, err
	}
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		return RecordFile{}, err
	}
	dir, err := recordFileDir(owner, slug, user, record)
	if err != nil {
		return RecordFile{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return RecordFile{}, fmt.Errorf("create the record's folder: %w", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) >= MaxRecordFiles {
		return RecordFile{}, fmt.Errorf("this record already keeps %d files (the limit): one has to be removed first", MaxRecordFiles)
	}
	name := keptName(dir, src)
	dst := filepath.Join(dir, name)
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return RecordFile{}, err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return RecordFile{}, err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return RecordFile{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	}
	return RecordFile{Name: name, Title: title, Type: ct, Size: info.Size(), Added: time.Now().UTC().Format(time.RFC3339)}, nil
}

// RecordFilePath is where a record's kept file is, and its content type.
func RecordFilePath(owner, slug, user, record, name string) (string, string, error) {
	if !validRecordFileName(name) {
		return "", "", fmt.Errorf("invalid file name")
	}
	dir, err := recordFileDir(owner, slug, user, record)
	if err != nil {
		return "", "", err
	}
	ct, _ := RecordFileType(name)
	return filepath.Join(dir, name), ct, nil
}

// DeleteRecordFile removes one kept file.
func DeleteRecordFile(owner, slug, user, record, name string) error {
	path, _, err := RecordFilePath(owner, slug, user, record, name)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// DeleteRecordFiles removes every file a record keeps, for a record that is
// being deleted.
func DeleteRecordFiles(owner, slug, user, record string) error {
	dir, err := recordFileDir(owner, slug, user, record)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// DeleteAppRecordFiles removes every file every record of an app keeps, for
// an app that is being deleted.
func DeleteAppRecordFiles(owner, slug string) error {
	dir, err := appRecordFilesDir(owner, slug)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}
