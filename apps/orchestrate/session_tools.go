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
// Filters out drafts whose name is already shadowed by either the
// agent's bundled Tools[] or the user's persistent pool. add_tool
// writes BOTH a session draft AND a committed copy so the tool is
// callable mid-turn — but once the committed copy exists, the draft
// is just stale duplication. The runtime cleans it up in
// newToolSession when the next turn runs, but the chat Tools modal
// can open between turns and see the stale draft. Filter (and clean
// up on the fly) here too so the UI matches what's actually live.
func (T *OrchestrateApp) handleSessionToolsList(w http.ResponseWriter, r *http.Request, udb Database, user, agentID, sid string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	drafts := LoadSessionTempTools(udb, sid)

	// Build the "already committed" name set: agent.Tools entries
	// (agent-scoped) + persistent_temp_tools (user-wide). Either
	// shadows a draft of the same name.
	committed := make(map[string]bool)
	if agentID != "" {
		if agent, ok := loadAgent(udb, agentID); ok {
			for _, t := range agent.Tools {
				committed[t.Name] = true
			}
		}
	}
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
//	action=persist&global=true  → copy into user-wide persistent_temp_tools
//	action=persist&global=false → append to AgentRecord.Tools (agent-attached)
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
		global := strings.EqualFold(r.URL.Query().Get("global"), "true")
		if global {
			if err := AdminPersistTempTool(udb, user, *found); err != nil {
				http.Error(w, "persist: "+err.Error(), http.StatusInternalServerError)
				return
			}
			RemoveSessionTempTool(udb, sid, name)
			// add_tool writes BOTH to agent.Tools and session_temp_tools
			// so the new tool is callable mid-turn for verification.
			// When the user promotes that draft to the user-wide pool,
			// the agent-bundled copy becomes redundant — it would still
			// surface under "Custom tools bundled with this agent" AND
			// in the regular catalog (via persistent_temp_tools), as a
			// duplicate. Strip the agent-bundled entry so the user-wide
			// copy is the single source of truth.
			if agentID != "" {
				if agent, ok := loadAgent(udb, agentID); ok && agent.Owner == user {
					strippedToolBundle := agent.Tools[:0]
					removed := false
					for _, t := range agent.Tools {
						if t.Name == name {
							removed = true
							continue
						}
						strippedToolBundle = append(strippedToolBundle, t)
					}
					if removed {
						agent.Tools = strippedToolBundle
						if _, err := saveAgent(udb, agent); err != nil {
							Log("[orchestrate.session_tools] warn: persisted %q globally but could not strip agent-bundled copy on %q: %v", name, agent.Name, err)
						} else {
							Log("[orchestrate.session_tools] stripped redundant agent-bundled %q from agent %q after global persist", name, agent.Name)
						}
					}
				}
			}
			Log("[orchestrate.session_tools] user %q persisted %q to USER-WIDE pool (session %s)", user, name, sid)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "persisted", "scope": "global", "name": name})
			return
		}
		// Agent-attached: append (or replace by name) into the focused
		// agent's bundled Tools[]. The session's agent_id is the target.
		agent, ok := loadAgent(udb, agentID)
		if !ok {
			http.Error(w, "agent not found", http.StatusNotFound)
			return
		}
		if agent.Owner != user {
			http.Error(w, "cannot attach a tool to an agent you don't own", http.StatusForbidden)
			return
		}
		replaced := false
		for i := range agent.Tools {
			if agent.Tools[i].Name == name {
				agent.Tools[i] = *found
				replaced = true
				break
			}
		}
		if !replaced {
			agent.Tools = append(agent.Tools, *found)
		}
		if _, err := saveAgent(udb, agent); err != nil {
			http.Error(w, "save agent: "+err.Error(), http.StatusInternalServerError)
			return
		}
		RemoveSessionTempTool(udb, sid, name)
		verb := "attached"
		if replaced {
			verb = "replaced"
		}
		Log("[orchestrate.session_tools] user %q %s %q to agent %q (session %s)", user, verb, name, agent.Name, sid)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "persisted", "scope": "agent", "name": name})

	default:
		http.Error(w, fmt.Sprintf("unknown action %q (expected persist or drop)", action), http.StatusBadRequest)
	}
}
