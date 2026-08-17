// Cross-scope enumeration of a user's tools, registered into core's seam so
// surfaces outside this app (Extensions > Tools) can show everything built
// for a user and what it's attached to.
//
// Two scopes live behind this app's knowledge: agent-scoped rows in the
// user's unified tool store (non-empty ScopeAgents), and session drafts
// (persist=false) keyed by chat-session id in a global table with no owner on
// the row. Resolving either to agent NAMES requires this app's agent records.

package orchestrate

import (
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// registerScopedToolLister installs the enumerator. Call once at startup.
func registerScopedToolLister(app *OrchestrateApp) {
	RegisterScopedToolLister(func(user string) []ScopedTool {
		return app.listScopedTools(user)
	})
	// Promotion shares the in-chat Tools modal's implementation, so a draft
	// kept from Extensions lands exactly where one kept from the chat does.
	ReapTrialTools = func(db Database, owner string) int { return app.reapTrialTools(db, owner) }
	RegisterScopedToolPromoter(func(user, agentID, sessionID, toolName, target string) (string, error) {
		if app == nil {
			return "", fmt.Errorf("orchestrate not initialized")
		}
		return app.promoteSessionDraft(UserDB(app.DB, user), user, agentID, sessionID, toolName, target)
	})
}

// listScopedTools walks the user's unified tool store, emitting one row per
// (agent-scoped tool, carrying agent), plus the drafts in each agent's chat
// sessions.
//
// Shadowing is MARKED, not filtered (see ScopeTool.Shadowed): the UI hides
// shadowed rows so nobody is invited to "keep" a tool they already have, while
// cleanupSessionDraftsByName needs precisely those to find and delete.
func (T *OrchestrateApp) listScopedTools(user string) []ScopedTool {
	if T == nil || strings.TrimSpace(user) == "" {
		return nil
	}
	udb := UserDB(T.DB, user)
	if udb == nil {
		return nil
	}
	// Parent names for sub-agents, resolved once: a listing shows "Parent > Sub"
	// so a specialist reads as one, not as a peer of its parent.
	agents := listAgents(udb, user)
	agentByID := map[string]AgentRecord{}
	nameByID := map[string]string{}
	for _, a := range agents {
		agentByID[a.ID] = a
		nameByID[a.ID] = a.Name
	}
	parentOf := func(a AgentRecord) string {
		if a.OwnedBy != "" {
			return nameByID[a.OwnedBy]
		}
		return ""
	}

	// One walk over the unified store: shared rows (empty ScopeAgents — pool
	// semantics) shadow same-named drafts everywhere; agent-scoped rows emit
	// one ScopedTool per carrying agent.
	pooled := map[string]bool{}
	bundled := map[string]map[string]bool{} // agent id → tool names scoped to it
	var out []ScopedTool
	for _, p := range LoadPersistentTempTools(udb, user) {
		if len(p.ScopeAgents) == 0 {
			pooled[p.Tool.Name] = true
			continue
		}
		for _, id := range p.ScopeAgents {
			agent, ok := agentByID[id]
			if !ok {
				continue // scoped to an agent that no longer exists
			}
			if bundled[id] == nil {
				bundled[id] = map[string]bool{}
			}
			bundled[id][p.Tool.Name] = true
			out = append(out, ScopedTool{
				Tool: p.Tool, Scope: ScopeAgentTool,
				AgentID: agent.ID, AgentName: agent.Name, ParentName: parentOf(agent),
				Trial: p.Tool.Trial,
				// One unified store: a scoped row can no longer coexist with a
				// pool row of the same name, so nothing shadows it.
				Shadowed: false,
			})
		}
	}
	for _, agent := range agents {
		parent := parentOf(agent)
		for _, s := range listChatSessions(udb, agent.ID) {
			for _, t := range LoadSessionTempTools(udb, s.ID) {
				out = append(out, ScopedTool{
					Tool: t, Scope: ScopeSessionTool,
					AgentID: agent.ID, AgentName: agent.Name, ParentName: parent,
					SessionID: s.ID, SessionTitle: strings.TrimSpace(s.Title),
					Shadowed: bundled[agent.ID][t.Name] || pooled[t.Name],
				})
			}
		}
	}
	// Stable order: agent, then scope (bundled before drafts), then name. The
	// UI groups on this order, so an unstable sort would reshuffle headings
	// between refreshes.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].AgentName != out[j].AgentName {
			return out[i].AgentName < out[j].AgentName
		}
		if out[i].Scope != out[j].Scope {
			return out[i].Scope == ScopeAgentTool
		}
		if out[i].SessionTitle != out[j].SessionTitle {
			return out[i].SessionTitle < out[j].SessionTitle
		}
		return out[i].Tool.Name < out[j].Tool.Name
	})
	return out
}

// reapTrialTools drops a user's unconfirmed authored tools once their TTL has
// elapsed, returning how many went.
//
// The session-scoped pool this replaced got cleanup for free — a tool tied to a
// conversation died with it — but paid for that with a scope nobody could see.
// Ephemerality is now an attribute, so the cleanup has to be explicit. Every
// removal is logged: a tool disappearing on its own is exactly the kind of
// thing that must leave a trail rather than being noticed weeks later.
//
// Only Trial tools with a TrialSince stamp are eligible. A confirmed tool, or
// one from before the stamp existed, is never touched — the reaper's failure
// mode has to be "left something behind", never "deleted work someone wanted".
func (T *OrchestrateApp) reapTrialTools(db Database, owner string) int {
	if T == nil || strings.TrimSpace(owner) == "" || TrialToolTTL <= 0 {
		return 0
	}
	udb := agentUserDB(db, owner)
	if udb == nil {
		return 0
	}
	cutoff := time.Now().Add(-TrialToolTTL)
	// Flattened namespace: unconfirmed tools are rows in the unified store
	// (any scope — shared or agent-scoped), not copies on agent records.
	dropped := []string{}
	for _, p := range LoadPersistentTempTools(udb, owner) {
		if !p.Tool.Trial || p.Tool.TrialSince.IsZero() || !p.Tool.TrialSince.Before(cutoff) {
			continue
		}
		if err := DeletePersistentTempTool(udb, owner, p.Tool.Name); err != nil {
			Log("[orchestrate.tools] reap failed for tool %q: %v", p.Tool.Name, err)
			continue
		}
		dropped = append(dropped, p.Tool.Name)
	}
	if len(dropped) > 0 {
		Log("[orchestrate.tools] reaped %d unconfirmed tool(s) after %s: %s",
			len(dropped), TrialToolTTL, strings.Join(dropped, ", "))
	}
	return len(dropped)
}
