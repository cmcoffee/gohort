// Endpoints for session-scoped temp tools — the drafts the LLM
// authored mid-conversation via tool_def / add_tool. Surfaces them in
// the chat Tools modal alongside agent-bundled and global tools, and
// lets the admin promote keepers out of the per-session pool into
// either the agent's bundled Tools (agent-attached) or the user-wide
// persistent pool (global). Without this surface, a session draft
// either gets manually re-authored later (lossy) or admin-approved
// via the queue (only if the LLM remembered persist=true).

package orchestrate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// handleSessionToolsList returns the session-scoped temp tools for
// the given session as a JSON array. Used by the chat Tools modal to
// render the "Session tools" section.
//
// Filters out drafts whose name is already shadowed by a committed
// row in the user's unified tool store (shared or agent-scoped).
// add_tool writes BOTH a session draft AND a committed copy so the
// tool is callable mid-turn — but once the committed copy exists,
// the draft is just stale duplication. The runtime cleans it up in
// newToolSession when the next turn runs, but the chat Tools modal
// can open between turns and see the stale draft. Filter (and clean
// up on the fly) here too so the UI matches what's actually live.
func (T *OrchestrateApp) handleSessionToolsList(w http.ResponseWriter, r *http.Request, udb Database, user, agentID, sid string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	drafts := LoadSessionTempTools(udb, sid)

	// Build the "already committed" name set from the user's unified tool
	// store — one loop covers both scopes (shared rows AND agent-scoped rows;
	// names are unique per user). Either shadows a draft of the same name.
	committed := make(map[string]bool)
	if user != "" {
		for _, p := range LoadPersistentTempTools(udb, user) {
			committed[p.Tool.Name] = true
		}
	}

	out := make([]TempTool, 0, len(drafts))
	for _, t := range drafts {
		if committed[t.Name] {
			RemoveSessionTempTool(udb, sid, t.Name)
			continue
		}
		out = append(out, t)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleSessionToolAction processes a POST against a single named
// session tool. Two actions:
//
//	action=persist&global=true  → shared row in the user's unified tool store
//	action=persist&global=false → store row scoped to the session's agent
//	action=drop                 → just remove from session_temp_tools
//
// Persist actions also clear the session draft on success so the
// promoted tool doesn't double-register on subsequent rounds.
func (T *OrchestrateApp) handleSessionToolAction(w http.ResponseWriter, r *http.Request, udb Database, user, agentID, sid, name string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	action := strings.TrimSpace(r.URL.Query().Get("action"))
	if action == "" {
		http.Error(w, "action query param required (persist or drop)", http.StatusBadRequest)
		return
	}
	tools := LoadSessionTempTools(udb, sid)
	var found *TempTool
	for i := range tools {
		if tools[i].Name == name {
			tmp := tools[i]
			found = &tmp
			break
		}
	}
	if found == nil {
		http.Error(w, fmt.Sprintf("no session tool named %q", name), http.StatusNotFound)
		return
	}

	switch action {
	case "drop":
		RemoveSessionTempTool(udb, sid, name)
		Log("[orchestrate.session_tools] user %q dropped session tool %q (session %s)", user, name, sid)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "dropped", "name": name})

	case "persist":
		// Target: the user-wide pool, or the session's own agent. Both paths live
		// in promoteSessionDraft so this handler and the Extensions > My tools
		// surface promote identically — the agent-copy stripping and ownership
		// checks are exactly the parts you do not want two copies of.
		target := ScopeTargetAgent
		if strings.EqualFold(r.URL.Query().Get("global"), "true") {
			target = ScopeTargetGlobal
		}
		scope, err := T.promoteSessionDraft(udb, user, agentID, sid, name, target)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "persisted", "scope": scope, "name": name})

	default:
		http.Error(w, fmt.Sprintf("unknown action %q (expected persist or drop)", action), http.StatusBadRequest)
	}
}

// promoteSessionDraft moves a session draft out of its per-session pool into a
// durable scope and clears the draft.
//
// target ScopeTargetGlobal → a shared row in the unified store; anything else →
// a row scoped to the session's agent. Shared by the in-chat Tools modal and
// Extensions > My tools so both promote identically: the ownership check and
// the scoping step are exactly the parts you do not want a second, drifting
// copy of. Returns the scope actually written ("global" | "agent").
func (T *OrchestrateApp) promoteSessionDraft(udb Database, user, agentID, sid, name, target string) (string, error) {
	tools := LoadSessionTempTools(udb, sid)
	var found *TempTool
	for i := range tools {
		if tools[i].Name == name {
			tmp := tools[i]
			found = &tmp
			break
		}
	}
	if found == nil {
		return "", fmt.Errorf("no session tool named %q", name)
	}
	if target == ScopeTargetGlobal {
		if err := AdminPersistTempTool(udb, user, *found); err != nil {
			return "", fmt.Errorf("persist: %w", err)
		}
		RemoveSessionTempTool(udb, sid, name)
		// No agent-copy stripping anymore: the flattened store holds ONE row
		// per tool, so a redundant agent-bundled duplicate can't exist.
		Log("[orchestrate.session_tools] user %q persisted %q to USER-WIDE pool (session %s)", user, name, sid)
		return "global", nil
	}
	// Agent-attached: the tool becomes ONE row in the user's unified store,
	// scoped to the session's agent. The session's agent_id is the target.
	agent, ok := loadAgent(udb, agentID)
	if !ok {
		return "", fmt.Errorf("agent not found")
	}
	if agent.Owner != user {
		return "", fmt.Errorf("cannot attach a tool to an agent you don't own")
	}
	_, existed := UserToolByName(udb, user, name)
	if err := AdminPersistTempTool(udb, user, *found); err != nil {
		return "", fmt.Errorf("persist: %w", err)
	}
	// A fresh row lands shared by default — scope it to the target agent. A
	// pre-existing shared row stays shared (the agent already sees it there).
	if p, ok := UserToolByName(udb, user, name); ok && len(p.ScopeAgents) == 0 && !existed {
		if !SetUserToolScopeAgents(udb, user, name, []string{agent.ID}) {
			Log("[orchestrate.session_tools] warn: persisted %q but could not scope it to agent %q", name, agent.Name)
		}
	}
	RemoveSessionTempTool(udb, sid, name)
	verb := "attached"
	if existed {
		verb = "replaced"
	}
	Log("[orchestrate.session_tools] user %q %s %q to agent %q (session %s)", user, verb, name, agent.Name, sid)
	return "agent", nil

}
