// bundle_upload.go — the inbound half of an evidence bundle: streaming the
// upload onto local disk, then handing the staged tree to the ingest pass.
//
// The upload is streamed with r.MultipartReader rather than ParseMultipartForm.
// The convenience form buffers the whole request — in memory up to its limit
// and into os.TempDir past it — which for a several-hundred-megabyte dump means
// the bytes land somewhere nobody configured, on a volume nobody sized, before
// they ever reach the staging directory that was chosen for exactly this.
package servitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// Bundle lifecycle states, stored on the appliance record so the UI can show
// what is happening to an upload that outlives its HTTP request.
const (
	bundleStateStaging   = "staging"   // bytes arriving
	bundleStateIngesting = "ingesting" // expanding + slicing into the store
	bundleStateReady     = "ready"
	bundleStateFailed    = "failed"
)

// bundleStagingRoot is where one appliance's in-flight upload lands. Per
// appliance rather than per request: a second upload to the same bundle
// REPLACES the first, and sharing the directory is what makes that true even
// when the first upload is still being written.
func bundleStagingRoot(owner, applianceID string) (string, error) {
	base := strings.TrimSpace(BulkStagingDir())
	if base == "" {
		return "", fmt.Errorf("no staging directory is configured — set [paths] bundle_dir in the config and restart")
	}
	if owner == "" || applianceID == "" {
		return "", fmt.Errorf("owner and appliance are both required to stage an upload")
	}
	return filepath.Join(base, safeBundleComponent(owner), safeBundleComponent(applianceID)), nil
}

// safeBundleComponent reduces an identifier to one path-safe element. The IDs
// are ours (a username, a UUID), so this is a belt-and-braces check on the one
// place a crafted value would become a directory.
func safeBundleComponent(s string) string {
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

// safeUploadName reduces a browser-supplied filename to a basename we are
// willing to create. The extension is preserved because the expander dispatches
// on it — a ".tar.gz" that arrives as "dump" is a bundle nobody can open.
func safeUploadName(name string) string {
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

// handleBundleUpload streams one or more files into a bundle appliance's
// staging directory. It does NOT ingest: a set of files is uploaded one request
// per file so each gets its own progress and its own retry, and ingesting after
// every request would start N passes over a half-staged tree, each wiping the
// one before it. The client posts /api/bundle/ingest once the last file lands.
//
// Owner/admin only: an upload replaces the evidence every user of a shared
// bundle is reading.
//
// ?reset=1 clears any previously staged files first. The client sends it on the
// FIRST file of a batch, so a second attempt after a failure replaces the old
// staging rather than ingesting a mixture of two uploads.
func (T *Servitor) handleBundleUpload(w http.ResponseWriter, r *http.Request) {
	if !IsStateChangingMethod(r.Method) {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if udb == nil {
		http.Error(w, "no database", http.StatusInternalServerError)
		return
	}
	applianceID := strings.TrimSpace(r.URL.Query().Get("appliance_id"))
	if applianceID == "" {
		http.Error(w, "appliance_id required", http.StatusBadRequest)
		return
	}
	rec, owner, ownerUDB, found := T.resolveAppliance(userID, udb, applianceID)
	if !found {
		http.Error(w, "appliance not found", http.StatusNotFound)
		return
	}
	if rec.Type != "bundle" {
		http.Error(w, "not an evidence-bundle appliance", http.StatusBadRequest)
		return
	}
	if !canManageAppliance(userID, rec, servitorIsAdmin(r)) {
		http.Error(w, "not allowed to upload to this bundle", http.StatusForbidden)
		return
	}
	if BundleFilesDB == nil {
		http.Error(w, "the evidence store is not initialized on this deployment", http.StatusServiceUnavailable)
		return
	}
	stage, err := bundleStagingRoot(owner, applianceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "expected a multipart form with one or more \"file\" parts: "+err.Error(), http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("reset") == "1" {
		purgeBundleStaging(owner, applianceID)
		rec.BundleSources = nil
	}
	if err := os.MkdirAll(stage, 0700); err != nil {
		http.Error(w, "could not create the staging directory: "+err.Error(), http.StatusInternalServerError)
		return
	}

	rec.BundleState = bundleStateStaging
	rec.BundleError = ""
	ownerUDB.Set(applianceTable, applianceID, rec)

	names, total, err := streamPartsToStage(mr, stage)
	if err != nil {
		// Staging is left in place for the next attempt to overwrite rather
		// than deleted here: a failed upload of file 9 of 10 should not
		// discard the eight that arrived.
		T.markBundleFailed(ownerUDB, applianceID, err.Error())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(names) == 0 {
		T.markBundleFailed(ownerUDB, applianceID, "no files in the upload")
		http.Error(w, "no files in the upload", http.StatusBadRequest)
		return
	}

	rec.BundleSources = append(rec.BundleSources, names...)
	rec.BundleUploaded = time.Now().Format(time.RFC3339)
	ownerUDB.Set(applianceTable, applianceID, rec)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"staged": names,
		"bytes":  total,
		"state":  bundleStateStaging,
	})
}

// streamPartsToStage copies every "file" part of a multipart body onto disk,
// returning the names staged and the total bytes written.
func streamPartsToStage(mr *multipart.Reader, stage string) ([]string, int64, error) {
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
		name := safeUploadName(part.FileName())
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
		n, cerr := io.Copy(dst, io.LimitReader(part, maxBundleBytes-total+1))
		dst.Close()
		part.Close()
		total += n
		if cerr != nil {
			return names, total, fmt.Errorf("staging %q: %w", name, cerr)
		}
		if total > maxBundleBytes {
			return names, total, fmt.Errorf("the upload exceeds the %s limit", HumanSize(maxBundleBytes))
		}
		names = append(names, name)
	}
}

// ingestBundle runs the expansion + ingest pass and records the outcome on the
// appliance. Always leaves the record in a terminal state: an ingest that dies
// without saying so reads in the UI as one still running, forever.
func (T *Servitor) ingestBundle(ctx context.Context, owner string, ownerUDB Database, applianceID string) {
	stage, err := bundleStagingRoot(owner, applianceID)
	if err != nil {
		T.markBundleFailed(ownerUDB, applianceID, err.Error())
		return
	}
	st, err := ingestBundleDir(ctx, owner, applianceID, stage)
	var rec Appliance
	if ownerUDB == nil || !ownerUDB.Get(applianceTable, applianceID, &rec) {
		return
	}
	if err != nil {
		rec.BundleState = bundleStateFailed
		rec.BundleError = err.Error()
		ownerUDB.Set(applianceTable, applianceID, rec)
		Log("[servitor.bundle] ingest failed for %s: %v", applianceID, err)
		return
	}
	rec.BundleState = bundleStateReady
	rec.BundleError = ""
	rec.BundleFiles = st.Files
	rec.BundleLines = st.Lines
	rec.BundleBytes = st.Bytes
	rec.BundleIngested = time.Now().Format(time.RFC3339)
	// Counts that mean "something is here we did not read" are kept on the
	// record, not just in the log, so the listing can say so without anyone
	// having to go looking for why a bundle seems thin.
	rec.BundleBinaries = st.Binaries
	rec.BundleUnopened = st.Unopened
	ownerUDB.Set(applianceTable, applianceID, rec)
}

// markBundleFailed stamps a failure reason on the record.
func (T *Servitor) markBundleFailed(ownerUDB Database, applianceID, reason string) {
	if ownerUDB == nil {
		return
	}
	var rec Appliance
	if !ownerUDB.Get(applianceTable, applianceID, &rec) {
		return
	}
	rec.BundleState = bundleStateFailed
	rec.BundleError = reason
	ownerUDB.Set(applianceTable, applianceID, rec)
}

// handleBundleIngest expands and ingests whatever is currently staged. Called
// by the client once the last file of a batch has uploaded, and again by hand
// as the way back from a failed ingest — a cap raised, a disk freed — without
// making the user re-send a gigabyte.
func (T *Servitor) handleBundleIngest(w http.ResponseWriter, r *http.Request) {
	if !IsStateChangingMethod(r.Method) {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		ApplianceID string `json:"appliance_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ApplianceID == "" {
		http.Error(w, "appliance_id required", http.StatusBadRequest)
		return
	}
	if udb == nil {
		http.Error(w, "no database", http.StatusInternalServerError)
		return
	}
	rec, owner, ownerUDB, found := T.resolveAppliance(userID, udb, req.ApplianceID)
	if !found {
		http.Error(w, "appliance not found", http.StatusNotFound)
		return
	}
	if rec.Type != "bundle" {
		http.Error(w, "not an evidence-bundle appliance", http.StatusBadRequest)
		return
	}
	if !canManageAppliance(userID, rec, servitorIsAdmin(r)) {
		http.Error(w, "not allowed to ingest this bundle", http.StatusForbidden)
		return
	}
	stage, err := bundleStagingRoot(owner, req.ApplianceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if entries, derr := os.ReadDir(stage); derr != nil || len(entries) == 0 {
		http.Error(w, "nothing is staged for this bundle — the uploaded files were consumed by a previous ingest. Upload them again.", http.StatusBadRequest)
		return
	}
	rec.BundleState = bundleStateIngesting
	rec.BundleError = ""
	ownerUDB.Set(applianceTable, req.ApplianceID, rec)
	go T.ingestBundle(AppContext(), owner, ownerUDB, req.ApplianceID)
	w.WriteHeader(http.StatusAccepted)
}

// purgeBundleStaging removes any staged-but-not-yet-ingested upload for an
// appliance. Called when the appliance is deleted, so evidence does not outlive
// the record that pointed at it.
func purgeBundleStaging(owner, applianceID string) {
	stage, err := bundleStagingRoot(owner, applianceID)
	if err != nil || stage == "" {
		return
	}
	base := strings.TrimSpace(BulkStagingDir())
	// Never remove a path this package did not construct.
	if base == "" || !strings.HasPrefix(stage, filepath.Clean(base)+string(os.PathSeparator)) {
		return
	}
	os.RemoveAll(stage)
}
