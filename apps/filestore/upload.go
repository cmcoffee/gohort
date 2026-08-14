// The inbound half: putting files into a store through the interface,
// for when scp or a mount is not how they arrive.
//
// Streamed with r.MultipartReader rather than ParseMultipartForm, for
// the reason servitor's bundle upload gives: the convenience form
// buffers the whole request — in memory to its limit and into
// os.TempDir past it — so a several-hundred-megabyte capture lands
// somewhere nobody configured, on a volume nobody sized, before it ever
// reaches the folder that was chosen for exactly this.
//
// The target subfolder is CREATED if it does not exist. That is the
// no-setup property the whole store is for: dropping scan-2026-08-13
// into a store is the entire ceremony, and requiring the folder to be
// declared first would put the setup back.
//
// Archives ARE expanded, through core's expander — the one lifted out of
// servitor's bundle ingest once this became its second caller. A capture
// almost always arrives as a tarball, and a store that could not open
// one would push the unpacking back onto whoever is trying to ask a
// question about it.

package filestore

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// maxExpandBytes bounds what one upload may unpack to. Generous, so a
// real capture fits; finite, because an upload is untrusted input and a
// zip bomb should cost a refusal rather than the disk.
const maxExpandBytes = 4 << 30 // 4 GiB

// handleUpload accepts one or more files into a store subfolder.
//
//	POST /filestore/api/upload?slug=<store>&within=<subfolder>
//
// Admin-gated like the rest of this app: it writes to the host
// filesystem, and a path-taking write is not something to hand out more
// widely than the config that names the paths.
func (T *FileStoreApp) handleUpload(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if !AuthIsAdmin(T.DB, r) {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	st, found := LoadStore(T.DB, strings.TrimSpace(r.URL.Query().Get("slug")))
	if !found {
		http.Error(w, "no such file store", http.StatusNotFound)
		return
	}
	dest, err := EnsureSub(st.Path, r.URL.Query().Get("within"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "expected a multipart upload: "+err.Error(), http.StatusBadRequest)
		return
	}
	var written []string
	var total int64
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, "reading upload: "+err.Error(), http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(part.FileName())
		if name == "" {
			part.Close()
			continue // a plain form field, not a file
		}
		n, saved, err := saveUploadPart(dest, name, part)
		part.Close()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		written = append(written, saved)
		total += n
	}
	if len(written) == 0 {
		http.Error(w, "no files in that upload", http.StatusBadRequest)
		return
	}
	// Unpack whatever arrived as an archive. The budget is generous but
	// finite: an upload is untrusted input, and a zip bomb should cost a
	// refusal rather than the disk.
	exp, err := ExpandArchives(r.Context(), dest, ExpandLimits{MaxBytes: maxExpandBytes})
	if err != nil {
		// The files are already on disk and searchable as they are, so a
		// failed unpack is reported, not fatal.
		Log("[filestore] expanding upload in %s: %v", dest, err)
	}
	Log("[filestore] %s uploaded %d file(s), %s, into %s/%s",
		user, len(written), HumanSize(total), st.Name, filepath.Base(dest))
	out := map[string]any{
		"store": st.Slug, "within": filepath.Base(dest),
		"files": written, "bytes": total,
	}
	if exp.Opened > 0 || exp.Unopened > 0 || exp.Skipped > 0 {
		out["expanded"] = exp.Opened
		out["expanded_bytes"] = exp.Bytes
		// Reported rather than swallowed: a store that looks thin because
		// half of it is in a .7z is a different problem from one that is
		// thin because nothing was captured.
		out["unopened"] = exp.Unopened
		out["refused_members"] = exp.Skipped
	}
	writeJSON(w, out)
}

// saveUploadPart streams one part to disk under dest.
//
// The filename comes from the client, so it is reduced to its base
// element and re-checked after joining: a part named "../../etc/cron.d/x"
// is the obvious attack, and filepath.Base alone is the kind of defence
// that looks sufficient until someone finds the case it misses.
func saveUploadPart(dest, name string, r io.Reader) (int64, string, error) {
	base := filepath.Base(filepath.Clean("/" + name))
	if base == "." || base == string(filepath.Separator) || strings.TrimSpace(base) == "" {
		return 0, "", fmt.Errorf("a file in that upload has no usable name")
	}
	full := filepath.Join(dest, base)
	if filepath.Dir(full) != filepath.Clean(dest) {
		return 0, "", fmt.Errorf("that filename does not stay in the folder: %s", name)
	}
	f, err := os.OpenFile(full, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, "", fmt.Errorf("cannot write %s: %w", base, err)
	}
	defer f.Close()
	n, err := io.Copy(f, r)
	if err != nil {
		// A partial file is worse than none: it reads as a complete log
		// that happens to stop mid-incident.
		os.Remove(full)
		return 0, "", fmt.Errorf("writing %s failed after %s: %w", base, HumanSize(n), err)
	}
	return n, base, nil
}
