package servitor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/bundle"
	"github.com/cmcoffee/gohort/core/jsonapi"
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
					a.HasKey, a.PrivateKey = a.PrivateKey != "", ""
					a.LeadTierAvailable = AllLLMsPrivate()
					items = append(items, a)
					seen[a.ID] = true
				}
			}
		}
		// Shared appliances owned by OTHERS — discoverable + usable by everyone,
		// but managed only by their owner (see canManageAppliance).
		for id, owner := range T.systemsSharedWith(userID) {
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
					a.HasKey, a.PrivateKey = a.PrivateKey != "", ""
					// Someone else's record: its EnvVars can hold the
					// secrets the command runs with. Names only, unless an
					// admin (who may manage it) is looking.
					if !servitorIsAdmin(r) {
						a.EnvVars = redactEnvVars(a.EnvVars)
					}
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
		// A retired type: an existing one keeps working and can be edited, and
		// no new one is made (the type-change check below covers turning
		// another system into one).
		if why, retired := retiredSystemTypes[req.Type]; retired && req.ID == "" {
			http.Error(w, why, http.StatusBadRequest)
			return
		}
		switch req.Type {
		case "command":
			// Runs `sh -c` on the gohort host as the gohort process: owning
			// one is owning the server. See localCommandAllowed.
			if !servitorIsAdmin(r) {
				http.Error(w, "a local command system runs commands on the gohort server itself, so only an admin can create or change one", http.StatusForbidden)
				return
			}
			if req.Name == "" || req.Command == "" {
				http.Error(w, "name and command required", http.StatusBadRequest)
				return
			}
		case "repo":
			req.RepoURL = strings.TrimSpace(req.RepoURL)
			req.RepoPath = strings.TrimSpace(req.RepoPath)
			if req.RepoURL == "" && req.RepoPath == "" {
				http.Error(w, "a git url or a local folder is required", http.StatusBadRequest)
				return
			}
			// A local folder is read from the gohort server's own disk. The
			// owner being an admin is not enough (repoLocalRoot): someone
			// with edit access to an admin's repo could otherwise point it
			// anywhere on the server. As with the local command system, the
			// person saving it must be an admin too (and to change one that
			// already reads a folder: see the edit branch below).
			if req.RepoPath != "" && !servitorIsAdmin(r) {
				http.Error(w, localRepoAdminOnly, http.StatusForbidden)
				return
			}
			if req.Name == "" {
				if req.RepoPath != "" {
					req.Name = filepath.Base(filepath.Clean(req.RepoPath))
				} else {
					req.Name = repoNameFromURL(req.RepoURL)
				}
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
			// The peer's grant is this DEPLOYMENT's: an administrator set it
			// up, and the far side handed it a shell on that system. A record
			// pointing at it gives whoever owns the record that shell, so only
			// an administrator makes one (and may share it, as with any system).
			if !servitorIsAdmin(r) {
				http.Error(w, "a remote system runs commands through this deployment's peer link, so only an admin can create or change one", http.StatusForbidden)
				return
			}
			if req.PeerName == "" || req.RemoteID == "" {
				http.Error(w, "a remote system needs a peer and the system id on that peer", http.StatusBadRequest)
				return
			}
			peer, ok := GetRemotePeer(req.PeerName)
			if !ok {
				http.Error(w, fmt.Sprintf("no peer named %q is registered: add it under Peers first", req.PeerName), http.StatusBadRequest)
				return
			}
			offered := false
			for _, inv := range peer.Investigable {
				offered = offered || inv.ID == req.RemoteID
			}
			if !offered {
				http.Error(w, fmt.Sprintf("%s does not offer a system with id %q to this instance: pick one from the list it advertises (refresh the peer if it was just granted)", req.PeerName, req.RemoteID), http.StatusBadRequest)
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
				http.Error(w, "select at least one member system", http.StatusBadRequest)
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
		// Refused rather than dropped: an entry the gate cannot read would be
		// quietly left out, and the owner would wonder why lookups still ask.
		lookups, err := normalize_lookup_allow(req.LookupAllow)
		if err != nil {
			http.Error(w, "lookup destinations: "+err.Error(), http.StatusBadRequest)
			return
		}
		req.LookupAllow = lookups
		req.OrchestratorTier = normalizeApplianceTier(req.OrchestratorTier)
		req.WorkerTier = normalizeApplianceTier(req.WorkerTier)
		// Computed for the form, never stored — it would otherwise persist a
		// snapshot of a deployment setting and go stale the moment that setting
		// changed.
		req.LeadTierAvailable = false
		if !AllLLMsPrivate() && (req.OrchestratorTier == "lead" || req.WorkerTier == "lead") {
			http.Error(w, "pinning this system to the lead model needs Admin → LLMs → Model Privacy turned on: "+
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
				http.Error(w, "system not found", http.StatusNotFound)
				return
			}
			// Only the owner or an admin may edit / re-share a record. A non-owner
			// can USE a shared appliance but not change it.
			if !canManageAppliance(userID, existing, servitorIsAdmin(r)) {
				http.Error(w, "not allowed to edit this system", http.StatusForbidden)
				return
			}
			if why, retired := retiredSystemTypes[req.Type]; retired && existing.Type != req.Type && !isRemote {
				http.Error(w, why, http.StatusBadRequest)
				return
			}
			if existing.RepoPath != "" && !servitorIsAdmin(r) {
				http.Error(w, localRepoAdminOnly, http.StatusForbidden)
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
			if req.PrivateKey == "" && !req.ClearKey {
				req.PrivateKey = existing.PrivateKey
			}
			if req.RepoToken == "" {
				req.RepoToken = existing.RepoToken
			}
			req.EnvVars = restoreRedactedEnvVars(req.EnvVars, existing.EnvVars)
			// The one case where carrying one forward is wrong: a real type
			// change. The secret belongs to a kind this appliance no longer
			// is, it can never be used again, and leaving it is secret
			// material lingering in a record nobody thinks holds one.
			if req.Type != "" && req.Type != existing.Type {
				if req.Type != "ssh" {
					req.Password = ""
					req.PrivateKey = ""
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
		// A repo is cloned on THIS server; refuse a source it must not reach
		// before the record exists to be cloned (see validateRepoSource).
		if req.Type == "repo" {
			if req.RepoPath != "" {
				// One source or the other: a folder has no remote, branch or
				// token, and a stored token would linger unused.
				req.RepoURL, req.RepoBranch, req.RepoToken = "", "", ""
				if _, err := repoLocalRoot(req.RepoPath, UserIsAdmin(owner)); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
			} else if err := validateRepoSource(req.RepoURL, req.RepoBranch, UserIsAdmin(owner)); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
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
		var prev Appliance
		hadPrev := targetUDB.Get(applianceTable, req.ID, &prev)
		// Shared with everyone only ever stays; it is never turned on.
		req.Shared = hadPrev && keptEveryone(prev.Shared, req.AllowedUsers, req.Shared)
		// Absent from the body (nil, where a cleared list arrives as []):
		// a save that did not send it keeps who it is shared with.
		if req.AllowedUsers == nil && hadPrev {
			req.AllowedUsers = prev.AllowedUsers
		}
		req.AllowedUsers = cleanSystemRecipients(req.AllowedUsers, owner)
		// Who among them has Full access: sent by the editor, which sets the
		// levels; a save that did not send it keeps the stored ones.
		if req.FullAccess == nil && hadPrev {
			req.FullAccess, req.LevelsSet = prev.FullAccess, prev.LevelsSet
		} else if req.FullAccess != nil {
			req.LevelsSet = true
		}
		req.FullAccess = keepReached(cleanSystemRecipients(req.FullAccess, owner), req.AllowedUsers)
		// A private key is checked as it is saved, where a bad paste can be
		// told apart, rather than at the first connection, where it reads as
		// the system refusing to let us in.
		if req.Type != "" && req.Type != "ssh" {
			req.PrivateKey = ""
		}
		if req.PrivateKey != "" {
			req.PrivateKey = strings.TrimSpace(req.PrivateKey) + "\n"
			if _, err := parseSystemKey(req.PrivateKey, req.Password); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		req.HasKey, req.ClearKey = false, false
		targetUDB.Set(applianceTable, req.ID, req)
		// Keep the global shared index in sync with the record's Shared flag,
		// and the named one with who it is shared with by name or team.
		T.setApplianceShared(req.ID, owner, req.Shared)
		T.indexNamedShares(owner, req.ID, req.AllowedUsers)
		dropConn(owner, req.ID) // force reconnect with new credentials
		// Saving a system is the owner saying "this is the machine": the next
		// connection trusts whatever key it presents now (a rebuilt host).
		if req.Type == "ssh" {
			forgetHostKey(req.Host, req.Port)
		}
		// Repo appliances: clone + ingest under the OWNER (one shared clone) on
		// create or when the store is empty.
		if req.Type == "repo" && (isNew || req.RepoFiles == 0) {
			go T.cloneAndIngestRepo(AppContext(), owner, targetUDB, req.ID)
		}
		resp := req
		resp.Password = ""
		resp.RepoToken = ""
		resp.HasKey, resp.PrivateKey = resp.PrivateKey != "", ""
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (T *Servitor) handleAppliance(c *jsonapi.Call) (any, error) {
	userID := c.User
	id := strings.TrimPrefix(c.R.URL.Path, "/api/appliance/")
	if id == "" || c.UDB == nil {
		return nil, jsonapi.NotFound("not found")
	}
	// Resolve own OR shared so a non-owner can load a shared appliance's
	// record (read-only; the UI gates edit/delete on can_manage).
	a, owner, _, found := T.resolveAppliance(userID, c.UDB, id)
	if !found {
		return nil, jsonapi.NotFound("not found")
	}
	a.Owner = owner
	a.Password = ""
	a.RepoToken = "" // never send the stored token back to the edit form
	a.HasKey, a.PrivateKey = a.PrivateKey != "", ""
	// EnvVars values are the owner's secrets: a user it is merely shared
	// with sees the names (see redactEnvVars).
	if !canManageAppliance(userID, a, servitorIsAdmin(c.R)) {
		a.EnvVars = redactEnvVars(a.EnvVars)
	}
	a.LeadTierAvailable = AllLLMsPrivate()
	return a, nil
}

// handleApplianceDelete removes a system: the owner or an admin only.
func (T *Servitor) handleApplianceDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	userID := c.User
	id := strings.TrimPrefix(c.R.URL.Path, "/api/appliance/")
	if id == "" || c.UDB == nil {
		return nil, jsonapi.NotFound("not found")
	}
	a, owner, ownerUDB, found := T.resolveAppliance(userID, c.UDB, id)
	if !found {
		return nil, jsonapi.NotFound("not found")
	}
	// Only the owner or an admin may delete.
	if !canManageAppliance(userID, a, servitorIsAdmin(c.R)) {
		return nil, jsonapi.Forbidden("not allowed to delete this system")
	}
	T.setApplianceShared(id, owner, false) // drop from the shared index first
	T.indexNamedShares(owner, id, nil)
	purgeAppliance(owner, ownerUDB, id)
	return jsonapi.Empty(http.StatusOK), nil
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

// retiredSystemTypes are the system types no new record is made of, each with
// why and what to use instead. Existing ones keep working and can be edited.
//
// A local command system gives an agent free-form sh on this server; a file
// store's mapped commands give it one tool per approved use instead, run
// without a shell, with the store's caps. A tool-backed service is not a
// system at all: it is an agent working through a set of approved tools,
// which an agent's own tools and MCP servers already are. A workspace is a
// coordinator over several systems, which an agent with those systems as its
// Sources is; an evidence bundle is files with an index, which a file store
// folder with evidence on is (docs/design/servitor-systems-model.md).
var retiredSystemTypes = map[string]string{
	"command": "new local command systems are no longer made: they give an agent a free shell on this server. Register the folder as a file store (Admin, File stores), add the command to it, and map its uses there; each use becomes a tool the agent can be given. Existing local command systems keep working.",
	"toolset": "new tool-backed services are no longer made: a service reached through tools is the agent's own tools. Give the agent those tools directly, or connect the service as an MCP server. Existing tool-backed services keep working.",
	"workspace": "new workspaces are no longer made: an agent does what a workspace did. Make an agent, attach each member system to it under Sources, and put the roles (which system is the scheduler, which the primary database) in its instructions; its lead can ask each one in turn. Existing workspaces keep working.",
	"bundle":    "new evidence bundles are no longer made: evidence lives in file stores now. Upload the files into a folder of a file store with Index folders as evidence on (Admin, File stores), and give the agent that store under Sources; it gets summarize and timeline tools for each folder. Existing evidence bundles keep working.",
}
