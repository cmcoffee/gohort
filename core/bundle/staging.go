// The inbound half: getting an upload onto local disk, intact and inside a
// directory somebody chose, before anything tries to read it.
//
// Split from the app's HTTP handler rather than living with it, because the two
// answer different questions. WHO may replace this bundle's evidence, and what
// the record should say while the bytes are moving, are the app's — they need
// its permissions, its user model and its own notion of what a bundle belongs
// to. Where the bytes land, what a filename is allowed to become, and how much
// may arrive are this package's, and every app that accepts a bundle wants the
// same answers.
//
// The upload is streamed by the caller with r.MultipartReader rather than
// ParseMultipartForm. The convenience form buffers the whole request — in
// memory up to its limit and into os.TempDir past it — which for a
// several-hundred-megabyte dump means the bytes land somewhere nobody
// configured, on a volume nobody sized, before they ever reach the staging
// directory that was chosen for exactly this.
package bundle

import (
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"

	"github.com/cmcoffee/snugforge/nfo"
)

// StagingDir returns the configured base directory for in-flight uploads, or ""
// when none is set. Assigned by core at init, like OpenStore.
//
// A seam for the same reason: this package cannot import the hub that reads the
// config. Unset is not an error here — StagingRoot turns it into one, with a
// sentence naming the config key, because that is the only place a person can
// act on it.
var StagingDir func() string

// StagingRoot is where one bundle's in-flight upload lands.
//
// Per bundle rather than per request: a second upload to the same bundle
// REPLACES the first, and sharing the directory is what makes that true even
// while the first upload is still being written.
func StagingRoot(owner, id string) (string, error) {
	base := ""
	if StagingDir != nil {
		base = strings.TrimSpace(StagingDir())
	}
	if base == "" {
		return "", fmt.Errorf("no staging directory is configured — set [paths] bundle_dir in the config and restart")
	}
	if owner == "" || id == "" {
		return "", fmt.Errorf("owner and bundle id are both required to stage an upload")
	}
	return filepath.Join(base, safeComponent(owner), safeComponent(id)), nil
}

// safeComponent reduces an identifier to one path-safe element. The IDs are
// ours (a username, a UUID), so this is a belt-and-braces check on the one place
// a crafted value would become a directory.
func safeComponent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			b.WriteByte(c)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		out = "unnamed"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

// SafeUploadName reduces a browser-supplied filename to a basename we are
// willing to create. The extension is preserved because the expander dispatches
// on it — a ".tar.gz" that arrives as "dump" is a bundle nobody can open.
func SafeUploadName(name string) string {
	name = strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
	name = filepath.Base(name)
	if name == "." || name == ".." || name == "/" || name == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_', r == '+', r == ' ':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.TrimLeft(b.String(), ".") // never create a dotfile
	if len(out) > 200 {
		out = out[:200]
	}
	return strings.TrimSpace(out)
}

// StreamPartsToStage copies every "file" part of a multipart body onto disk,
// returning the names staged and the total bytes written.
func StreamPartsToStage(mr *multipart.Reader, stage string) ([]string, int64, error) {
	var (
		names []string
		total int64
	)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return names, total, nil
		}
		if err != nil {
			return names, total, fmt.Errorf("reading the upload: %w", err)
		}
		if part.FileName() == "" {
			part.Close()
			continue // a plain form field, not a file
		}
		name := SafeUploadName(part.FileName())
		if name == "" {
			part.Close()
			return names, total, fmt.Errorf("%q is not a usable filename", part.FileName())
		}
		dst, err := os.OpenFile(filepath.Join(stage, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			part.Close()
			return names, total, fmt.Errorf("staging %q: %w", name, err)
		}
		// LimitReader caps the whole upload at the expanded-size budget: a
		// compressed dump larger than what it is allowed to expand into can
		// only end in a refused ingest, so it is refused at the door.
		n, cerr := io.Copy(dst, io.LimitReader(part, MaxBytes-total+1))
		dst.Close()
		part.Close()
		total += n
		if cerr != nil {
			return names, total, fmt.Errorf("staging %q: %w", name, cerr)
		}
		if total > MaxBytes {
			return names, total, fmt.Errorf("the upload exceeds the %s limit", nfo.HumanSize(MaxBytes))
		}
		names = append(names, name)
	}
}

// PurgeStaging removes any staged-but-not-yet-ingested upload for a bundle.
// Called when the thing that owned the bundle is deleted, so evidence does not
// outlive the record that pointed at it.
func PurgeStaging(owner, id string) {
	stage, err := StagingRoot(owner, id)
	if err != nil || stage == "" {
		return
	}
	base := ""
	if StagingDir != nil {
		base = strings.TrimSpace(StagingDir())
	}
	// Never remove a path this package did not construct.
	if base == "" || !strings.HasPrefix(stage, filepath.Clean(base)+string(os.PathSeparator)) {
		return
	}
	os.RemoveAll(stage)
}
