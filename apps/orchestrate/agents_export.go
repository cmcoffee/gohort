package orchestrate

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// agentExport is the portable recipe shape: the agent itself plus any
// sub-agents it owns, each carrying its inline Tools. AgentRecord is
// embedded so the parent's fields stay at the top level — a plain
// AgentRecord JSON (older exports / hand-written recipes) still imports,
// with SubAgents simply empty.
type agentExport struct {
	AgentRecord
	SubAgents []AgentRecord `json:"sub_agents,omitempty"`
}

// stripAgentIdentity clears the fields that describe a particular install of an
// agent (id, owner, parent link, timestamps) so what remains is the portable
// recipe. Memory is not part of the recipe: it is per-user-per-agent learning,
// not the persona contract, and travels separately and only on request as an
// "agent_memory" artifact (agent_memory_artifact.go).
func stripAgentIdentity(a AgentRecord) AgentRecord {
	a.ID = ""
	a.Owner = ""
	a.OwnedBy = ""
	a.Created = time.Time{}
	a.Updated = time.Time{}
	return a
}

// buildAgentExport assembles the portable recipe for one TOP-LEVEL agent: the
// identity-stripped record plus its identity-stripped owned sub-agents, so
// importing the parent recreates the whole tree. Returns false when the agent
// isn't found or isn't owned by user. Shared by the HTTP export handler and the
// unified artifact-bundle agent type (agent_artifact.go).
func buildAgentExport(udb Database, id, user string) (agentExport, bool) {
	a, ok := loadAgent(udb, id)
	if !ok || (a.Owner != user && a.Owner != seedOwner) {
		return agentExport{}, false
	}
	var subs []AgentRecord
	for _, k := range udb.Keys(agentsTable) {
		if k == id {
			continue
		}
		var s AgentRecord
		if !udb.Get(agentsTable, k, &s) {
			continue
		}
		if s.OwnedBy != id || (s.Owner != user && s.Owner != seedOwner) {
			continue
		}
		// Sub-agents carry their scoped tools inline the same way the parent
		// does, so the recipe stays self-contained.
		s.Tools = toolsOfScoped(AgentScopedTools(udb, user, s.ID))
		subs = append(subs, stripAgentIdentity(s))
	}
	// Flattened namespace: the record no longer embeds tools, but the RECIPE
	// still carries them inline (same wire shape as pre-flatten exports) so a
	// bundle is portable across installs. Import folds them back into the
	// store scoped to the reborn agent.
	a.Tools = toolsOfScoped(AgentScopedTools(udb, user, a.ID))
	return agentExport{AgentRecord: stripAgentIdentity(a), SubAgents: subs}, true
}

// toolsOfScoped unwraps store rows to their tool definitions.
func toolsOfScoped(rows []PersistentTempTool) []TempTool {
	if len(rows) == 0 {
		return nil
	}
	out := make([]TempTool, 0, len(rows))
	for _, p := range rows {
		out = append(out, p.Tool)
	}
	return out
}

// handleAgentImport accepts a JSON agent record (the shape produced by
// .../export) and saves it as a new agent owned by the importer.
// Whatever ID, Owner, Created the importer sends are discarded — the
// record is reborn under the active user with a fresh id, so cross-
// install imports stay collision-free.
func (T *OrchestrateApp) handleAgentImport(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, ok := readImportBody(w, r)
	if !ok {
		return
	}
	if importBundleAtDoor(w, user, body, "agent", func(name string) (any, bool) {
		for _, a := range listAgents(udb, user) {
			if a.OwnedBy == "" && a.Owner == user && a.Name == name {
				return a, true
			}
		}
		return nil, false
	}) {
		return
	}
	var imp agentExport
	if err := json.Unmarshal(UnwrapArtifactUpload(body), &imp); err != nil {
		http.Error(w, "that does not read as an agent recipe ("+err.Error()+")", http.StatusBadRequest)
		return
	}
	saved, subCount, err := importAgentRecipe(udb, imp, user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if subCount > 0 {
		Log("[orchestrate.agents] imported agent %q (%s) with %d sub-agent(s)", saved.Name, saved.ID, subCount)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(saved)
}

// importAgentRecipe reconstitutes an agent recipe under owner: the parent is
// reborn with a fresh id (whatever id/owner/timestamps the recipe carried are
// discarded, so cross-install imports stay collision-free), and its bundled
// sub-agents are recreated parented to the new id. A malformed sub-agent is
// skipped (logged), not fatal — the parent already saved. Returns the saved
// parent and the number of sub-agents created. Shared by the HTTP import
// handler and the unified artifact-bundle agent type.
func importAgentRecipe(udb Database, imp agentExport, owner string) (AgentRecord, int, error) {
	rec := imp.AgentRecord
	if strings.TrimSpace(rec.Name) == "" {
		return AgentRecord{}, 0, Error("import: name is required")
	}
	if strings.TrimSpace(rec.OrchestratorPrompt) == "" {
		return AgentRecord{}, 0, Error("import: orchestrator_prompt is required")
	}
	rec.ID = ""
	rec.Owner = owner
	rec.OwnedBy = ""
	rec.Created = time.Time{}
	rec.Updated = time.Time{}
	makeImportedAgentInert(&rec)
	// Recipes carry tools inline (both pre- and post-flatten exports). Hold
	// them aside: they fold into the unified store AFTER the save assigns the
	// reborn agent its id — the record itself stays tool-free.
	inlineTools := rec.Tools
	rec.Tools = nil
	saved, err := saveAgent(udb, rec)
	if err != nil {
		return AgentRecord{}, 0, err
	}
	queueImportedTools(udb, owner, saved, inlineTools)
	subCount := 0
	for _, s := range imp.SubAgents {
		if strings.TrimSpace(s.Name) == "" || strings.TrimSpace(s.OrchestratorPrompt) == "" {
			Log("[orchestrate.agents] import: skipping sub-agent with missing name/prompt under %q", saved.Name)
			continue
		}
		s.ID = ""
		s.Owner = owner
		s.OwnedBy = saved.ID
		s.Created = time.Time{}
		s.Updated = time.Time{}
		makeImportedAgentInert(&s)
		subTools := s.Tools
		s.Tools = nil
		savedSub, serr := saveAgent(udb, s)
		if serr != nil {
			Log("[orchestrate.agents] import: sub-agent %q failed: %v", s.Name, serr)
			continue
		}
		queueImportedTools(udb, owner, savedSub, subTools)
		subCount++
	}
	return saved, subCount, nil
}

// makeImportedAgentInert resets what a recipe must not decide for the install
// it lands on. An import is somebody else's file, so it arrives as a new,
// private agent under the importer, exactly as if they had just created it:
//
//   - Reach: not published to everyone, not on inbound MCP, not on the
//     dashboard, shared with nobody, not reachable by Builder. Everyone and
//     MCPExposed need an admin's approval on every other path; a recipe that
//     set them would skip it. Usernames in a share list also name people on
//     ANOTHER install.
//   - Autonomy: no tools pre-approved for unattended runs, no authorized
//     identities (a master key for every "@" rule). Both are grants this
//     install never made.
//   - Safety: guardrails on and failing closed, the new-agent hook set when
//     the recipe has none, and nothing that WIDENS the tool-result scanner
//     (trusted sources, skipped tools, tightening turned off). Rules and scan
//     settings that only tighten travel unchanged; they are the recipe.
//
// The owner can change any of it afterwards through the normal controls,
// which is where each of these decisions belongs.
func makeImportedAgentInert(a *AgentRecord) {
	a.Exposed = false
	a.Everyone = false
	a.MCPExposed = false
	a.ShowOnDashboard = false
	a.AllowedUsers = nil
	a.AllowBuilderDispatch = false
	a.PendingApproval = false

	a.AutoApproveTools = nil
	a.AuthorizedIdentities = nil

	a.GuardrailsDisabled = false
	a.GuardrailFailClosed = defaultNewAgentFailClosed
	if len(a.GuardrailHooks) == 0 {
		a.GuardrailHooks = defaultNewAgentGuardrailHooks()
	}
	a.ScanTrustedSources = nil
	a.ScanToolsSkip = nil
	a.ScanTightenDisabled = false
}

// queueImportedTools lands a recipe's inline tools for review, scoped to the
// reborn agent. A tool is executable code from outside this install, so it
// goes to the PENDING pool, the same place a standalone tool import lands,
// and fires only after an admin approves it. Approval keeps the scope, so the
// tool joins this agent's kit and no other.
//
// A tool the importer ALREADY has, with an identical definition, is not new
// code: it just gains this agent in its scope. A same-named tool whose
// definition differs is kept aside in Orphaned tools, as the flatten
// migration does, rather than replacing the one already approved.
func queueImportedTools(udb Database, owner string, saved AgentRecord, tools []TempTool) {
	var orphans []OrphanedTempTool
	queued := 0
	for _, t := range tools {
		if existing, ok := UserToolByName(udb, owner, t.Name); ok {
			if tempToolDefEqual(t, existing.Tool) {
				if len(existing.ScopeAgents) > 0 && !existing.ScopedToAgent(saved.ID) {
					SetUserToolScopeAgents(udb, owner, t.Name,
						append(append([]string{}, existing.ScopeAgents...), saved.ID))
				}
				continue
			}
			orphans = append(orphans, OrphanedTempTool{
				Tool:            t,
				FormerAgentID:   saved.ID,
				FormerAgentName: saved.Name,
				OrphanedAt:      time.Now(),
			})
			continue
		}
		if err := QueuePendingTempToolScoped(udb, owner, t, "import", []string{saved.ID}); err != nil {
			Log("[orchestrate.agents] import %q: tool %q not queued: %v", saved.Name, t.Name, err)
			continue
		}
		queued++
	}
	if len(orphans) > 0 {
		AddOrphanedTempTools(udb, owner, orphans)
		Log("[orchestrate.agents] import %q: %d tool(s) diverged from your existing tools, imported copies are in Orphaned tools", saved.Name, len(orphans))
	}
	if queued > 0 {
		Log("[orchestrate.agents] import %q: %d tool(s) queued for approval", saved.Name, queued)
	}
}

// safeFilename returns a slug suitable for the Content-Disposition
// filename header. Strips anything that isn't alphanumeric, dash, or
// underscore; collapses runs to single dashes; falls back to "agent"
// when the result would be empty.
func safeFilename(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_'
		if ok {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "agent"
	}
	return out
}
