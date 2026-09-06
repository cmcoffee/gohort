package servitor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/bundle"
)

// --- Appliance CRUD ---

func (T *Servitor) handleAppliances(w http.ResponseWriter, r *http.Request) {
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		if udb != nil {
			cleanDanglingRecords(udb)
		}
		items := []Appliance{}
		seen := map[string]bool{}
		// The user's OWN appliances.
		if udb != nil {
			for _, key := range udb.Keys(applianceTable) {
				var a Appliance
				if udb.Get(applianceTable, key, &a) {
					if a.Owner == "" {
						a.Owner = userID // legacy record: the holder owns it
					}
					a.Password = ""
					a.RepoToken = ""
					a.LeadTierAvailable = AllLLMsPrivate()
					items = append(items, a)
					seen[a.ID] = true
				}
			}
		}
		// Shared appliances owned by OTHERS — discoverable + usable by everyone,
		// but managed only by their owner (see canManageAppliance).
		for id, owner := range T.listSharedAppliances() {
			if seen[id] || owner == userID {
				continue
			}
			if ownerUDB := UserDB(T.DB, owner); ownerUDB != nil {
				var a Appliance
				if ownerUDB.Get(applianceTable, id, &a) {
					a.Owner = owner
					a.Shared = true
					a.Password = ""
					a.RepoToken = ""
					a.LeadTierAvailable = AllLLMsPrivate()
					items = append(items, a)
					seen[id] = true
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(items)

	case http.MethodPost:
		if udb == nil {
			http.Error(w, "no database", http.StatusInternalServerError)
			return
		}
		var req Appliance
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		if req.Type == "" {
			req.Type = "ssh"
		}
		// Set by the "remote" case, which rewrites Type to the far side's kind.
		isRemote := false
		switch req.Type {
		case "command":
			if req.Name == "" || req.Command == "" {
				http.Error(w, "name and command required", http.StatusBadRequest)
				return
			}
		case "repo":
			req.RepoURL = strings.TrimSpace(req.RepoURL)
			if req.RepoURL == "" {
				http.Error(w, "repo url required", http.StatusBadRequest)
				return
			}
			if req.Name == "" {
				req.Name = repoNameFromURL(req.RepoURL)
			}
		case "bundle":
			// A bundle owns no host and no remote of its own — it is created
			// empty and filled by an upload, so a name is all that is required.
			if req.Name == "" {
				http.Error(w, "name required", http.StatusBadRequest)
				return
			}
		case "remote":
			// "remote" is a PICKER mode, not a stored type. What gets stored is
			// the type the far side reported, so every prompt, tool set and
			// branch downstream treats this exactly as it would a local
			// appliance of that kind — which is the whole point. PeerName is
			// what says "the exec seam goes over the wire"; nothing else in the
			// session behaves differently.
			req.PeerName = strings.TrimSpace(req.PeerName)
			req.RemoteID = strings.TrimSpace(req.RemoteID)
			isRemote = true
			if req.PeerName == "" || req.RemoteID == "" {
				http.Error(w, "a remote system needs a peer and the appliance id on that peer", http.StatusBadRequest)
				return
			}
			if _, ok := GetRemotePeer(req.PeerName); !ok {
				http.Error(w, fmt.Sprintf("no peer named %q is registered — add it under Peers first", req.PeerName), http.StatusBadRequest)
				return
			}
			if req.Name == "" {
				http.Error(w, "name required", http.StatusBadRequest)
				return
			}
			req.Type = firstNonEmptyStr(strings.TrimSpace(req.RemoteKind), "ssh")
		case "toolset":
			if req.Name == "" {
				http.Error(w, "name required", http.StatusBadRequest)
				return
			}
			// Fingerprints are stamped further down, once the OWNER is known —
			// a shared appliance's tools come from the owner's pool, not the
			// editor's.
			req.Domain = strings.TrimSpace(req.Domain)
		case "workspace":
			// A workspace references other appliances; it owns no creds/store.
			req.Members = dedupeStrings(req.Members)
			if req.Name == "" {
				http.Error(w, "name required", http.StatusBadRequest)
				return
			}
			if len(req.Members) == 0 {
				http.Error(w, "select at least one member appliance", http.StatusBadRequest)
				return
			}
			req.MemberRoles = pruneMemberRoles(req.MemberRoles, req.Members)
			req.MemberLinks = pruneMemberLinks(req.MemberLinks, req.Members)
		default:
			req.Type = "ssh"
			if req.Name == "" || req.Host == "" {
				http.Error(w, "name and host required", http.StatusBadRequest)
				return
			}
			if req.Port == 0 {
				req.Port = 22
			}
			if req.User == "" {
				req.User = "root"
			}
		}
		// Normalize the tier overrides, and enforce the privacy rule SERVER-side.
		// The form omits "lead" when the deployment forbids it, but a form is not
		// a gate: a saved record, an import, or a second tab open from before the
		// setting changed would all carry a value the runtime then silently
		// ignores. Refusing here means the stored record and the behavior agree.
		req.ToolsRunAs = normalizeToolsRunAs(req.ToolsRunAs)
		req.OrchestratorTier = normalizeApplianceTier(req.OrchestratorTier)
		req.WorkerTier = normalizeApplianceTier(req.WorkerTier)
		// Computed for the form, never stored — it would otherwise persist a
		// snapshot of a deployment setting and go stale the moment that setting
		// changed.
		req.LeadTierAvailable = false
		if !AllLLMsPrivate() && (req.OrchestratorTier == "lead" || req.WorkerTier == "lead") {
			http.Error(w, "pinning this appliance to the lead model needs Admin → LLMs → Model Privacy turned on — "+
				"Servitor handles credentials and log contents, so it stays on the worker until every configured model is private",
				http.StatusBadRequest)
			return
		}
		isNew := req.ID == ""
		// The store the record lives in, and its owner. A new record is owned by
		// its creator and saved to their store; an update lands in the OWNER's
		// store (which may not be the requester's, for a shared record edited by
		// an admin).
		targetUDB := udb
		owner := userID
		// Preserve sensitive/derived fields when updating without re-supplying them.
		if !isNew {
			existing, exOwner, exUDB, found := T.resolveAppliance(userID, udb, req.ID)
			if !found {
				http.Error(w, "appliance not found", http.StatusNotFound)
				return
			}
			// Only the owner or an admin may edit / re-share a record. A non-owner
			// can USE a shared appliance but not change it.
			if !canManageAppliance(userID, existing, servitorIsAdmin(r)) {
				http.Error(w, "not allowed to edit this appliance", http.StatusForbidden)
				return
			}
			targetUDB = exUDB
			owner = exOwner
			// Write-only secrets. Every read path blanks these before the
			// record leaves the server, so the edit form ALWAYS loads with
			// them empty and an empty field on save means "not shown", never
			// "clear it".
			//
			// Preserved unconditionally rather than on req.Type, which is the
			// hole this closes: the guard read the INCOMING type, so any save
			// whose body did not carry type ("repo" / "ssh") — a partial
			// update, another surface, an agent posting the record back —
			// wrote a blank straight over a stored token or password. And it
			// presented as "no token configured" rather than as a token that
			// stopped working, because by then there genuinely was none.
			if req.Password == "" {
				req.Password = existing.Password
			}
			if req.RepoToken == "" {
				req.RepoToken = existing.RepoToken
			}
			// The one case where carrying one forward is wrong: a real type
			// change. The secret belongs to a kind this appliance no longer
			// is, it can never be used again, and leaving it is secret
			// material lingering in a record nobody thinks holds one.
			if req.Type != "" && req.Type != existing.Type {
				if req.Type != "ssh" {
					req.Password = ""
				}
				if req.Type != "repo" {
					req.RepoToken = ""
				}
			}
			req.Profile = existing.Profile
			req.LogMap = existing.LogMap
			req.Scanned = existing.Scanned
			req.RepoFiles = existing.RepoFiles
			req.RepoCloned = existing.RepoCloned
			// Bundle ingest state is derived from the uploaded evidence, never
			// from the edit form. Without this an edit that only renamed the
			// appliance would zero the counters and the tools would start
			// reporting the bundle as not ingested.
			req.BundleState = existing.BundleState
			req.BundleError = existing.BundleError
			req.BundleSources = existing.BundleSources
			req.BundleUploaded = existing.BundleUploaded
			req.BundleIngested = existing.BundleIngested
			req.BundleFiles = existing.BundleFiles
			req.BundleLines = existing.BundleLines
			req.BundleBytes = existing.BundleBytes
			req.BundleBinaries = existing.BundleBinaries
			req.BundleUnopened = existing.BundleUnopened
		}
		// Linked repos: keep only ids that resolve (own or shared) to a REPO
		// appliance. Repo and workspace records carry none — a repo linking a
		// repo means nothing, and a workspace already composes members.
		if req.Type != "workspace" {
			req.MemberLinks = nil
		}
		if req.Type == "repo" || req.Type == "workspace" {
			req.LinkedRepos = nil
		} else if len(req.LinkedRepos) > 0 {
			kept := req.LinkedRepos[:0]
			for _, rid := range req.LinkedRepos {
				if la, _, _, ok := T.resolveAppliance(userID, udb, rid); ok && la.Type == "repo" {
					kept = append(kept, la.ID)
				}
			}
			req.LinkedRepos = kept
		}
		req.Owner = owner
		// Toolset bindings are fingerprinted on the way IN, against the owner's
		// pool, so a binding can never reach the store without the pin that
		// makes it verifiable. Any hash the client sent is discarded: a
		// caller-supplied fingerprint would let it bless a body nobody
		// approved, which is the whole thing the pin exists to prevent.
		if req.Type == "toolset" {
			req.Toolset = bindToolsetTools(owner, userID, req.Toolset)
		} else {
			req.Toolset = nil
		}
		// Only a remote stub carries peer coordinates. Cleared otherwise so an
		// edit that changes the type cannot leave a record that short-circuits
		// to a peer while claiming to be a local SSH box.
		if !isRemote {
			req.PeerName, req.RemoteID = "", ""
		}
		if req.ID == "" {
			req.ID = UUIDv4()
		}
		targetUDB.Set(applianceTable, req.ID, req)
		// Keep the global shared index in sync with the record's Shared flag.
		T.setApplianceShared(req.ID, owner, req.Shared)
		dropConn(owner, req.ID) // force reconnect with new credentials
		// Repo appliances: clone + ingest under the OWNER (one shared clone) on
		// create or when the store is empty.
		if req.Type == "repo" && (isNew || req.RepoFiles == 0) {
			go T.cloneAndIngestRepo(AppContext(), owner, targetUDB, req.ID)
		}
		resp := req
		resp.Password = ""
		resp.RepoToken = ""
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (T *Servitor) handleAppliance(w http.ResponseWriter, r *http.Request) {
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/appliance/")
	if id == "" || udb == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		// Resolve own OR shared so a non-owner can load a shared appliance's
		// record (read-only; the UI gates edit/delete on can_manage).
		a, owner, _, found := T.resolveAppliance(userID, udb, id)
		if !found {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		a.Owner = owner
		a.Password = ""
		a.RepoToken = "" // never send the stored token back to the edit form
		a.LeadTierAvailable = AllLLMsPrivate()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(a)
	case http.MethodDelete:
		a, owner, ownerUDB, found := T.resolveAppliance(userID, udb, id)
		if !found {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		// Only the owner or an admin may delete.
		if !canManageAppliance(userID, a, servitorIsAdmin(r)) {
			http.Error(w, "not allowed to delete this appliance", http.StatusForbidden)
			return
		}
		T.setApplianceShared(id, owner, false) // drop from the shared index first
		purgeAppliance(owner, ownerUDB, id)
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// cleanDanglingRecords removes facts, knowledge docs, notes, and techniques that
// reference appliance IDs no longer present in the appliances table.
func cleanDanglingRecords(udb Database) {
	if udb == nil {
		return
	}
	valid := make(map[string]bool)
	for _, k := range udb.Keys(applianceTable) {
		valid[k] = true
	}
	// Facts, knowledge docs, and discoveries are keyed "applianceID:subkey".
	for _, tbl := range []string{factsTable, knowledgeTable, discoveriesTable} {
		for _, k := range udb.Keys(tbl) {
			if parts := strings.SplitN(k, ":", 2); len(parts) == 2 && !valid[parts[0]] {
				udb.Unset(tbl, k)
			}
		}
	}
	// Notes and techniques are keyed directly by applianceID.
	for _, tbl := range []string{notesTable, techniquesTable} {
		for _, k := range udb.Keys(tbl) {
			if !valid[k] {
				udb.Unset(tbl, k)
			}
		}
	}
}

// clearApplianceMemory wipes all learned knowledge for an appliance: facts, knowledge docs,
// notes, techniques, and the cached system profile + log map stored on the appliance record.
func clearApplianceMemory(udb Database, applianceID string) {
	// Wipe auxiliary tables keyed by "applianceID:subkey".
	prefix := applianceID + ":"
	for _, tbl := range []string{factsTable, knowledgeTable, discoveriesTable} {
		for _, k := range udb.Keys(tbl) {
			if strings.HasPrefix(k, prefix) {
				udb.Unset(tbl, k)
			}
		}
	}
	// Notes and techniques keyed directly by applianceID.
	udb.Unset(notesTable, applianceID)
	udb.Unset(techniquesTable, applianceID)
	// Clear the profile and log map stored on the appliance record itself.
	var a Appliance
	if udb.Get(applianceTable, applianceID, &a) {
		a.Profile = ""
		a.LogMap = nil
		a.Scanned = ""
		udb.Set(applianceTable, applianceID, a)
	}
	// Also clear the orchestrate-scoped memory so "Clear Memory" is consistent
	// across the dual-write split (legacy ssh_* AND the scope the modal reads).
	clearApplianceScopedMemory(udb, applianceID)
}

// purgeAppliance removes all data associated with an appliance: the record itself,
// facts, knowledge docs, notes, techniques, and the pooled SSH connection.
func purgeAppliance(userID string, udb Database, applianceID string) {
	udb.Unset(applianceTable, applianceID)

	// Facts, knowledge docs, and discoveries are keyed "applianceID:subkey".
	prefix := applianceID + ":"
	for _, tbl := range []string{factsTable, knowledgeTable, discoveriesTable} {
		for _, k := range udb.Keys(tbl) {
			if strings.HasPrefix(k, prefix) {
				udb.Unset(tbl, k)
			}
		}
	}

	// Notes and techniques are keyed directly by applianceID.
	udb.Unset(notesTable, applianceID)
	udb.Unset(techniquesTable, applianceID)

	// Drop the orchestrate-scoped memory too, so a deleted appliance leaves no
	// orphaned scope behind.
	clearApplianceScopedMemory(udb, applianceID)

	// Bulk content stores live outside the user DB, keyed by appliance id, so
	// deleting the record does not touch them on its own. Both are dropped
	// here: an appliance the user deleted should not leave its ingested
	// contents sitting in an encrypted store nothing points at any more.
	// Bundles additionally clear any staged upload that never finished
	// ingesting, which is the one place uploaded evidence exists as plaintext.
	wipeRepoFiles(userID, applianceID)
	bundle.Open(userID, applianceID).Wipe()
	bundle.PurgeStaging(userID, applianceID)

	dropConn(userID, applianceID)
}
