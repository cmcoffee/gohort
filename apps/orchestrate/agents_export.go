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
// recipe. Memory does NOT travel — it's per-user-per-agent learning, not part
// of the persona contract.
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
	var imp agentExport
	if err := json.NewDecoder(r.Body).Decode(&imp); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
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
	// Recipes carry tools inline (both pre- and post-flatten exports). Hold
	// them aside: they fold into the unified store AFTER the save assigns the
	// reborn agent its id — the record itself stays tool-free.
	inlineTools := rec.Tools
	rec.Tools = nil
	saved, err := saveAgent(udb, rec)
	if err != nil {
		return AgentRecord{}, 0, err
	}
	foldImportedTools(udb, owner, &saved, inlineTools)
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
		subTools := s.Tools
		s.Tools = nil
		savedSub, serr := saveAgent(udb, s)
		if serr != nil {
			Log("[orchestrate.agents] import: sub-agent %q failed: %v", s.Name, serr)
			continue
		}
		foldImportedTools(udb, owner, &savedSub, subTools)
		subCount++
	}
	return saved, subCount, nil
}

// foldImportedTools lands a recipe's inline tools in the unified store scoped
// to the reborn agent, via the same conflict policy the flatten migration
// uses (identical dup → merge, diverged → orphan with provenance).
func foldImportedTools(udb Database, owner string, saved *AgentRecord, tools []TempTool) {
	if len(tools) == 0 {
		return
	}
	carrier := *saved
	carrier.Tools = tools
	moved, merged, orphaned := foldAgentToolsIntoStore(udb, owner, &carrier)
	if orphaned > 0 {
		Log("[orchestrate.agents] import %q: %d tool(s) diverged from your existing tools — imported copies are in Orphaned tools", saved.Name, orphaned)
	}
	_ = moved
	_ = merged
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
