package servitor

import (
	"context"
	"fmt"
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// handleFacts returns all stored facts for a given appliance (GET)
// or deletes a single fact by its DB key (DELETE ?key=<id>).
func (T *Servitor) handleFacts(c *jsonapi.Call) (any, error) {
	id := c.R.URL.Query().Get("id")
	if id == "" || c.UDB == nil {
		return nil, jsonapi.BadRequest("id required")
	}
	facts := factsForAppliance(c.UDB, id)
	if facts == nil {
		facts = []SshFact{}
	}
	return facts, nil
}

// handleFactSave records a fact about a system.
func (T *Servitor) handleFactSave(c *jsonapi.Call, req factReq) (any, error) {
	if req.ApplianceID == "" || req.Key == "" || req.Value == "" {
		return nil, jsonapi.BadRequest("appliance_id, key, and value required")
	}
	var appliance Appliance
	if c.UDB == nil || !c.UDB.Get(applianceTable, req.ApplianceID, &appliance) {
		return nil, jsonapi.NotFound("system not found")
	}
	storeFact(c.UDB, req.ApplianceID, appliance.Name, req.Key, req.Value, "long", req.Tags)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleFactDelete removes a fact by its key.
func (T *Servitor) handleFactDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	key := c.R.URL.Query().Get("key")
	if key == "" || c.UDB == nil {
		return nil, jsonapi.BadRequest("key required")
	}
	c.UDB.Unset(factsTable, key)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// factReq is what handleFactSave is posted.
type factReq struct {
	ApplianceID string   `json:"appliance_id"`
	Key         string   `json:"key"`
	Value       string   `json:"value"`
	Tags        []string `json:"tags,omitempty"`
}

// handleMemoryClear wipes all learned memory for an appliance (facts, knowledge docs,
// notes, techniques) without deleting the appliance record or its system profile.
func (T *Servitor) handleMemoryClear(c *jsonapi.Call, req applianceReq) (any, error) {
	userID, udb := c.User, c.UDB
	if req.ApplianceID == "" {
		return nil, jsonapi.BadRequest("appliance_id required")
	}
	if udb == nil {
		return nil, jsonapi.Status(http.StatusInternalServerError, "no database")
	}
	// Clearing shared memory affects every user of a shared appliance, so it's
	// an owner/admin-only management action, operating on the owner's store.
	rec, ownerUser, ownerUDB, found := T.resolveAppliance(userID, udb, req.ApplianceID)
	if !found {
		return nil, jsonapi.NotFound("system not found")
	}
	if !canManageAppliance(userID, rec, servitorIsAdmin(c.R)) {
		return nil, jsonapi.Forbidden("not allowed to clear this system's memory")
	}
	clearApplianceMemory(ownerUDB, req.ApplianceID)
	// Repo appliances: also drop the ingested code files. Reset the clone
	// bookkeeping so the record reflects "needs re-clone". Connection settings
	// (URL/branch/token) are kept, mirroring how SSH settings survive a clear.
	//
	// Re-read first. rec was loaded BEFORE clearApplianceMemory blanked the
	// profile, log map and scan time on the stored record, and writing that
	// copy back put the old profile straight back: "Clear Memory" on a repo
	// came back with the profile it had just cleared.
	if rec.Type == "repo" {
		wipeRepoFiles(ownerUser, req.ApplianceID)
		var fresh Appliance
		if ownerUDB.Get(applianceTable, req.ApplianceID, &fresh) {
			fresh.RepoFiles = 0
			fresh.RepoCloned = ""
			ownerUDB.Set(applianceTable, req.ApplianceID, fresh)
		}
	}
	// Bundles: clearing memory drops the recorded findings, NOT the evidence.
	// A re-clone restores a repo; nothing restores a dump, so the ingested
	// content survives a memory clear and is removed only by deleting the
	// appliance or replacing it with a new upload.
	if rec.Type == "bundle" {
		Log("[servitor.bundle] cleared recorded memory for %s; %d ingested files kept (evidence is not re-obtainable)",
			req.ApplianceID, rec.BundleFiles)
	}
	return jsonapi.Empty(http.StatusOK), nil
}

// handleRepoRefresh re-clones a repo appliance and re-ingests its files,
// picking up new commits (or restoring files after a memory clear). The clone
// runs in the background; the client polls the appliance list for RepoFiles.
func (T *Servitor) handleRepoRefresh(c *jsonapi.Call, req applianceReq) (any, error) {
	userID, udb := c.User, c.UDB
	if req.ApplianceID == "" {
		return nil, jsonapi.BadRequest("appliance_id required")
	}
	if udb == nil {
		return nil, jsonapi.Status(http.StatusInternalServerError, "no database")
	}
	rec, ownerUser, ownerUDB, found := T.resolveAppliance(userID, udb, req.ApplianceID)
	if !found {
		return nil, jsonapi.NotFound("system not found")
	}
	if rec.Type != "repo" {
		return nil, jsonapi.BadRequest("not a repository system")
	}
	// Re-cloning replaces the shared code for everyone, so gate it to owner+admin.
	if !canManageAppliance(userID, rec, servitorIsAdmin(c.R)) {
		return nil, jsonapi.Forbidden("not allowed to refresh this repository")
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
	return map[string]string{"session_id": sid}, nil
}

// handleCollectionsList returns the caller's knowledge collections (their own +
// deployment-scoped) as [{id,name,description}], so the appliance edit form can
// render the "Linked Knowledge" picker. Read-only; the selection itself is saved
// on the appliance record via the normal appliance POST (Collections field).
func (T *Servitor) handleCollectionsList(c *jsonapi.Call) (any, error) {
	type item struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
	}
	out := []item{}
	for _, col := range ListCollections(UserDB(CollectionsDB(), c.User), c.User) {
		out = append(out, item{ID: col.ID, Name: col.Name, Description: col.Description})
	}
	return out, nil
}
