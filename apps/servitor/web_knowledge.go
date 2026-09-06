package servitor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	. "github.com/cmcoffee/gohort/core"
)

// handleFacts returns all stored facts for a given appliance (GET)
// or deletes a single fact by its DB key (DELETE ?key=<id>).
func (T *Servitor) handleFacts(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		id := r.URL.Query().Get("id")
		if id == "" || udb == nil {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		facts := factsForAppliance(udb, id)
		if facts == nil {
			facts = []SshFact{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(facts)
	case http.MethodPost:
		var req struct {
			ApplianceID string   `json:"appliance_id"`
			Key         string   `json:"key"`
			Value       string   `json:"value"`
			Tags        []string `json:"tags,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ApplianceID == "" || req.Key == "" || req.Value == "" {
			http.Error(w, "appliance_id, key, and value required", http.StatusBadRequest)
			return
		}
		var appliance Appliance
		if udb == nil || !udb.Get(applianceTable, req.ApplianceID, &appliance) {
			http.Error(w, "appliance not found", http.StatusNotFound)
			return
		}
		storeFact(udb, req.ApplianceID, appliance.Name, req.Key, req.Value, "long", req.Tags)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		key := r.URL.Query().Get("key")
		if key == "" || udb == nil {
			http.Error(w, "key required", http.StatusBadRequest)
			return
		}
		udb.Unset(factsTable, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleMemoryClear wipes all learned memory for an appliance (facts, knowledge docs,
// notes, techniques) without deleting the appliance record or its system profile.
func (T *Servitor) handleMemoryClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
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
	// Clearing shared memory affects every user of a shared appliance, so it's
	// an owner/admin-only management action, operating on the owner's store.
	rec, ownerUser, ownerUDB, found := T.resolveAppliance(userID, udb, req.ApplianceID)
	if !found {
		http.Error(w, "appliance not found", http.StatusNotFound)
		return
	}
	if !canManageAppliance(userID, rec, servitorIsAdmin(r)) {
		http.Error(w, "not allowed to clear this appliance's memory", http.StatusForbidden)
		return
	}
	clearApplianceMemory(ownerUDB, req.ApplianceID)
	// Repo appliances: also drop the ingested code files. Reset the clone
	// bookkeeping so the record reflects "needs re-clone". Connection settings
	// (URL/branch/token) are kept, mirroring how SSH settings survive a clear.
	if rec.Type == "repo" {
		wipeRepoFiles(ownerUser, req.ApplianceID)
		rec.RepoFiles = 0
		rec.RepoCloned = ""
		ownerUDB.Set(applianceTable, req.ApplianceID, rec)
	}
	// Bundles: clearing memory drops the recorded findings, NOT the evidence.
	// A re-clone restores a repo; nothing restores a dump, so the ingested
	// content survives a memory clear and is removed only by deleting the
	// appliance or replacing it with a new upload.
	if rec.Type == "bundle" {
		Log("[servitor.bundle] cleared recorded memory for %s; %d ingested files kept (evidence is not re-obtainable)",
			req.ApplianceID, rec.BundleFiles)
	}
	w.WriteHeader(http.StatusOK)
}

// handleRepoRefresh re-clones a repo appliance and re-ingests its files,
// picking up new commits (or restoring files after a memory clear). The clone
// runs in the background; the client polls the appliance list for RepoFiles.
func (T *Servitor) handleRepoRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
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
	rec, ownerUser, ownerUDB, found := T.resolveAppliance(userID, udb, req.ApplianceID)
	if !found {
		http.Error(w, "appliance not found", http.StatusNotFound)
		return
	}
	if rec.Type != "repo" {
		http.Error(w, "not a repository appliance", http.StatusBadRequest)
		return
	}
	// Re-cloning replaces the shared code for everyone, so gate it to owner+admin.
	if !canManageAppliance(userID, rec, servitorIsAdmin(r)) {
		http.Error(w, "not allowed to refresh this repository", http.StatusForbidden)
		return
	}
	// Run the re-clone as a live, cancelable session (mirrors handleMap) so the
	// UI shows a spinner + live status and offers Cancel, instead of a silent
	// 202. The AgentLoopPanel subscribes to the same event stream.
	sid := UUIDv4()
	ctx, cancel := context.WithCancel(AppContext())
	probeSessions.Register(sid, "Refreshing "+rec.Name, cancel).SetOwner(userID)
	sessionAppliances.Store(sid, rec.ID)
	go func() {
		defer cancel()
		emit(sid, probeEvent{Kind: "status", Text: fmt.Sprintf("Re-cloning %s…", repoDisplayTarget(rec))})
		T.cloneAndIngestRepo(ctx, ownerUser, ownerUDB, rec.ID)
		if ctx.Err() != nil {
			probeSessions.AppendEvent(sid, probeEvent{Kind: "error", Text: "Refresh cancelled."}, true)
			probeSessions.ScheduleCleanup(sid)
			return
		}
		files := 0
		var updated Appliance
		if ownerUDB.Get(applianceTable, rec.ID, &updated) {
			files = updated.RepoFiles
		}
		emit(sid, probeEvent{Kind: "status", Text: fmt.Sprintf("Ingested %d files.", files)})
		// Validate the stored knowledge against the freshly-pulled code and
		// auto-correct stale docs. Runs under ctx, so Cancel stops it too.
		T.runRepoMemoryAudit(ctx, sid, ownerUser, ownerUDB, rec)
		probeSessions.AppendEvent(sid, probeEvent{Kind: "done"}, true)
		probeSessions.ScheduleCleanup(sid)
	}()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"session_id": sid})
}

// handleCollectionsList returns the caller's knowledge collections (their own +
// deployment-scoped) as [{id,name,description}], so the appliance edit form can
// render the "Linked Knowledge" picker. Read-only; the selection itself is saved
// on the appliance record via the normal appliance POST (Collections field).
func (T *Servitor) handleCollectionsList(w http.ResponseWriter, r *http.Request) {
	userID, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	type item struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
	}
	out := []item{}
	for _, c := range ListCollections(UserDB(CollectionsDB(), userID), userID) {
		out = append(out, item{ID: c.ID, Name: c.Name, Description: c.Description})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
