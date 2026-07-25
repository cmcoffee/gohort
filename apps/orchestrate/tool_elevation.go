// Tool elevation — auto-promoting the right lazy tools into the direct
// catalog, so an agent whose job-critical tool sits in the shared pool isn't
// left choosing from whatever happens to be visible (the fetch_url-over-
// moltbook failure class: the mission said "Moltbook", the toolbox existed,
// and the model used the raw fetcher because that was the schema in front
// of it).
//
// Two tiers, both VISIBILITY-only — elevation never widens access. A lazy
// tool is already callable via load_tool; elevation just pays its schema
// cost up front when the evidence says it's the tool for the job:
//
//	Tier 1 — mention match: the turn's intent text (mission / latest user
//	         message) names the tool, or names the host its credential
//	         points at. Deterministic string matching, no embeddings.
//	Tier 2 — repeated-load promotion: the agent load_tool'd the same tool
//	         in enough distinct recent sessions that it's evidently kit.
//	         Also queues a one-time "scope it?" suggestion in the
//	         Authorizations pane so durable curation stays a human call.
package orchestrate

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	// toolElevateMentionCap bounds Tier-1 promotions per turn — a mission
	// that name-drops half the pool must not inline half the pool's schemas.
	toolElevateMentionCap = 3
	// toolLoadPromoteAt: distinct recent sessions with a load_tool of the
	// same tool before Tier 2 treats it as kit.
	toolLoadPromoteAt = 3
	// toolLoadHistoryCap: how many distinct sessions to remember per tool.
	toolLoadHistoryCap = 8

	toolLoadHistoryTable = "tool_load_history"
)

// toolLoadEntry is one remembered load_tool occurrence.
type toolLoadEntry struct {
	Session string    `json:"session"`
	At      time.Time `json:"at"`
}

// recordToolLoads notes that this agent load_tool'd the named tools in this
// session. One entry per (tool, session) — a turn that reloads the same tool
// five times is one signal, not five. Fires the Tier-2 scope suggestion the
// moment a tool crosses the promotion threshold.
func recordToolLoads(db Database, agentID, sessionID string, tools []string) {
	if db == nil || agentID == "" || len(tools) == 0 {
		return
	}
	hist := map[string][]toolLoadEntry{}
	db.Get(toolLoadHistoryTable, agentID, &hist)
	changed := false
	for _, name := range tools {
		entries := hist[name]
		dup := false
		for _, e := range entries {
			if e.Session == sessionID {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		before := distinctLoadSessions(entries)
		entries = append(entries, toolLoadEntry{Session: sessionID, At: time.Now()})
		if len(entries) > toolLoadHistoryCap {
			entries = entries[len(entries)-toolLoadHistoryCap:]
		}
		hist[name] = entries
		changed = true
		if before < toolLoadPromoteAt && distinctLoadSessions(entries) >= toolLoadPromoteAt {
			suggestToolScope(db, agentID, name, distinctLoadSessions(entries))
		}
	}
	if changed {
		db.Set(toolLoadHistoryTable, agentID, hist)
	}
}

func distinctLoadSessions(entries []toolLoadEntry) int {
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Session] = true
	}
	return len(seen)
}

// promotedByLoadHistory returns the tools Tier 2 treats as kit for this
// agent: loaded in >= toolLoadPromoteAt distinct recent sessions.
func promotedByLoadHistory(db Database, agentID string) map[string]bool {
	if db == nil || agentID == "" {
		return nil
	}
	hist := map[string][]toolLoadEntry{}
	db.Get(toolLoadHistoryTable, agentID, &hist)
	var out map[string]bool
	for name, entries := range hist {
		if distinctLoadSessions(entries) >= toolLoadPromoteAt {
			if out == nil {
				out = map[string]bool{}
			}
			out[name] = true
		}
	}
	return out
}

// suggestToolScope queues a one-time Authorizations-pane suggestion to scope
// a repeatedly-loaded tool to this agent. Approving routes through
// handleApprove's "scope_tool" case, which ADDS the agent to the tool's
// ScopeAgents — making it first-class for this agent and (when the tool was
// shared) removing it from the general pool, which is exactly what the
// approval text warns.
func suggestToolScope(db Database, agentID, toolName string, sessions int) {
	rec, ok := loadAgent(db, agentID)
	if !ok {
		return
	}
	owner := rec.Owner
	if owner == "" {
		return
	}
	for _, ex := range ListAuthorizations(RootDB, owner) {
		if ex.Action == "scope_tool" && ex.Agent == agentID && ex.Brief == toolName {
			return // already pending
		}
	}
	SaveAuthorization(RootDB, Authorization{
		Owner:  owner,
		Action: "scope_tool",
		Agent:  agentID,
		Brief:  toolName,
		Text:   fmt.Sprintf("%q loaded %q in %d recent runs — it's evidently part of this agent's kit. Approve to scope the tool to it (always in its catalog; a shared tool leaves the general pool).", rec.Name, toolName, sessions),
	})
	Log("[orchestrate.elevate] suggested scoping %q to agent=%s (%d distinct sessions)", toolName, agentID, sessions)
}

// elevatedToolSet resolves which LAZY has-args tools get promoted into the
// direct catalog this turn. kit tools are excluded (already direct).
func (t *chatTurn) elevatedToolSet(sess *ToolSession, all []AgentToolDef, kit map[string]bool) map[string]string {
	elevated := map[string]string{} // name → reason (for the log line)
	// Trial (unconfirmed) tools stay lazy even when matched — elevation must
	// not out-promote the vouching gate. The Tier-2 scope suggestion still
	// fires for them; approving it is the vouch.
	// Tier 2 — proven-by-use kit.
	for name := range promotedByLoadHistory(t.udb, t.agent.ID) {
		if !kit[name] && !isTrialTool(sess, name) {
			elevated[name] = "repeated-load"
		}
	}
	// Tier 1 — the turn's intent names the tool (or its credential's host).
	intent := strings.ToLower(t.intentText)
	if intent == "" {
		return elevated
	}
	var names []string
	byName := map[string]AgentToolDef{}
	for _, td := range all {
		names = append(names, td.Tool.Name)
		byName[td.Tool.Name] = td
	}
	sort.Strings(names) // deterministic promotion order under the cap
	mentions := 0
	for _, name := range names {
		if mentions >= toolElevateMentionCap {
			break
		}
		if kit[name] || elevated[name] != "" || len(byName[name].Tool.Parameters) == 0 || isTrialTool(sess, name) {
			continue
		}
		if intentMentionsTool(intent, name, credentialHostFor(sess, name)) {
			elevated[name] = "mentioned"
			mentions++
		}
	}
	return elevated
}

// intentMentionsTool reports whether the lowercased intent text names the
// tool — by its exact name, its name with underscores as spaces ("get feed"
// for get_feed-style names), or the host of the credential it dispatches
// through ("moltbook.com" implies the moltbook toolbox).
func intentMentionsTool(intent, name, credHost string) bool {
	n := strings.ToLower(name)
	if strings.Contains(intent, n) {
		return true
	}
	if sp := strings.ReplaceAll(n, "_", " "); sp != n && strings.Contains(intent, sp) {
		return true
	}
	return credHost != "" && strings.Contains(intent, credHost)
}

// credentialHostFor resolves the lowercased host of the credential a custom
// tool dispatches through, or "" when it has none / it can't be parsed. The
// session's hydrated TempTool record carries the credential name.
func credentialHostFor(sess *ToolSession, toolName string) string {
	if sess == nil {
		return ""
	}
	lt := sess.LookupTempTool(toolName)
	if lt == nil || strings.TrimSpace(lt.Credential) == "" {
		return ""
	}
	c, ok := Secure().Load(strings.TrimSpace(lt.Credential))
	if !ok {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil || u == nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
