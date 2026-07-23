// Cross-scope enumeration of a user's tools, registered into core's seam so
// surfaces outside this app (Extensions > My tools) can show everything built
// for a user and what it's attached to.
//
// Two scopes live behind this app's knowledge: tools bundled onto an agent
// record, and session drafts (persist=false) keyed by chat-session id in a
// global table with no owner on the row. Both can only be scoped to a user by
// walking that user's agents.

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

// listScopedTools walks the user's agents, collecting each agent's bundled
// tools and the drafts in each of its chat sessions.
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
	// The user-wide pool shadows every scope below it; read it once.
	pooled := map[string]bool{}
	for _, p := range LoadPersistentTempTools(udb, user) {
		pooled[p.Tool.Name] = true
	}

	var out []ScopedTool
	for _, agent := range listAgents(udb, user) {
		bundled := map[string]bool{}
		for _, t := range agent.Tools {
			bundled[t.Name] = true
			out = append(out, ScopedTool{
				Tool: t, Scope: ScopeAgentTool,
				AgentID: agent.ID, AgentName: agent.Name,
				Trial: t.Trial,
				// An agent copy duplicated in the user's pool is redundant, but
				// it is NOT stale the way a draft is — the agent genuinely holds
				// it. Marked so a UI can choose; nothing deletes it.
				Shadowed: pooled[t.Name],
			})
		}
		for _, s := range listChatSessions(udb, agent.ID) {
			for _, t := range LoadSessionTempTools(udb, s.ID) {
				out = append(out, ScopedTool{
					Tool: t, Scope: ScopeSessionTool,
					AgentID: agent.ID, AgentName: agent.Name,
					SessionID: s.ID, SessionTitle: strings.TrimSpace(s.Title),
					Shadowed: bundled[t.Name] || pooled[t.Name],
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
	reaped := 0
	for _, agent := range listAgents(udb, owner) {
		kept := agent.Tools[:0:0]
		dropped := []string{}
		for _, tl := range agent.Tools {
			if tl.Trial && !tl.TrialSince.IsZero() && tl.TrialSince.Before(cutoff) {
				dropped = append(dropped, tl.Name)
				continue
			}
			kept = append(kept, tl)
		}
		if len(dropped) == 0 {
			continue
		}
		agent.Tools = kept
		if _, err := saveAgent(udb, agent); err != nil {
			Log("[orchestrate.tools] reap failed for agent %q: %v", agent.Name, err)
			continue
		}
		reaped += len(dropped)
		Log("[orchestrate.tools] reaped %d unconfirmed tool(s) from agent %q after %s: %s",
			len(dropped), agent.Name, TrialToolTTL, strings.Join(dropped, ", "))
	}
	return reaped
}
