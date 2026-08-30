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
	"net/http"
	"os"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/bundle"
)

// Bundle lifecycle states, stored on the appliance record so the UI can show
// what is happening to an upload that outlives its HTTP request.
const (
	bundleStateStaging   = "staging"   // bytes arriving
	bundleStateIngesting = "ingesting" // expanding + slicing into the store
	bundleStateReady     = "ready"
	bundleStateFailed    = "failed"
)

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
	stage, err := bundle.StagingRoot(owner, applianceID)
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
		bundle.PurgeStaging(owner, applianceID)
		rec.BundleSources = nil
	}
	if err := os.MkdirAll(stage, 0700); err != nil {
		http.Error(w, "could not create the staging directory: "+err.Error(), http.StatusInternalServerError)
		return
	}

	rec.BundleState = bundleStateStaging
	rec.BundleError = ""
	ownerUDB.Set(applianceTable, applianceID, rec)

	names, total, err := bundle.StreamPartsToStage(mr, stage)
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

// ingestBundle runs the expansion + ingest pass and records the outcome on the
// appliance. Always leaves the record in a terminal state: an ingest that dies
// without saying so reads in the UI as one still running, forever.
func (T *Servitor) ingestBundle(ctx context.Context, owner string, ownerUDB Database, applianceID string) {
	stage, err := bundle.StagingRoot(owner, applianceID)
	if err != nil {
		T.markBundleFailed(ownerUDB, applianceID, err.Error())
		return
	}
	st, err := bundle.Open(owner, applianceID).Ingest(ctx, stage)
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
	stage, err := bundle.StagingRoot(owner, req.ApplianceID)
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
