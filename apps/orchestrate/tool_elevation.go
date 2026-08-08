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
//	Tier 3 — prior success: the agent has already CALLED this tool and got
//	         a result back. One success is enough. Tiers 1 and 2 both miss
//	         the ordinary case where a user asks for the thing the tool
//	         does without naming it ("what's happening in the world today"
//	         → get_top_stories): the intent text doesn't match, and a tool
//	         the model reaches by improvising never accumulates load_tool
//	         history. A tool that has worked for this agent before is the
//	         strongest evidence available that it is the tool for the job,
//	         and it costs nothing to have been wrong — the schema is the
//	         only thing spent.
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

	// toolSuccessElevateCap bounds Tier-3 promotions per turn. Every lazy
	// tool the agent has ever used successfully would otherwise inline its
	// schema forever, which is the cost the lazy split exists to avoid.
	// Promotion order is most-recent-success first, so a tool that mattered
	// once last spring falls off in favour of the ones in current use.
	toolSuccessElevateCap = 5

	toolLoadHistoryTable    = "tool_load_history"
	toolSuccessHistoryTable = "tool_success_history"
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

// recordToolSuccess notes that this agent called toolName and got a result
// back (no error) in this session. One entry per (tool, session), same as the
// load history — a turn that calls the same tool five times is one signal.
//
// Only custom tools are recorded: elevation is a question about the lazy
// schema split, and registered tools are never lazy.
func recordToolSuccess(db Database, agentID, sessionID, toolName string) {
	if db == nil || agentID == "" || toolName == "" {
		return
	}
	hist := map[string][]toolLoadEntry{}
	db.Get(toolSuccessHistoryTable, agentID, &hist)
	entries := hist[toolName]
	for _, e := range entries {
		if e.Session == sessionID {
			return // already counted this session
		}
	}
	entries = append(entries, toolLoadEntry{Session: sessionID, At: time.Now()})
	if len(entries) > toolLoadHistoryCap {
		entries = entries[len(entries)-toolLoadHistoryCap:]
	}
	hist[toolName] = entries
	db.Set(toolSuccessHistoryTable, agentID, hist)
}

// promotedByPriorSuccess returns the tools Tier 3 elevates, most-recent
// success first and capped. A tool the agent has called successfully even
// once is treated as part of its working set.
func promotedByPriorSuccess(db Database, agentID string) []string {
	if db == nil || agentID == "" {
		return nil
	}
	hist := map[string][]toolLoadEntry{}
	db.Get(toolSuccessHistoryTable, agentID, &hist)
	if len(hist) == 0 {
		return nil
	}
	type lastUse struct {
		name string
		at   time.Time
	}
	var uses []lastUse
	for name, entries := range hist {
		var newest time.Time
		for _, e := range entries {
			if e.At.After(newest) {
				newest = e.At
			}
		}
		uses = append(uses, lastUse{name: name, at: newest})
	}
	// Most recent first; name breaks ties so the set is deterministic under
	// the cap (two successes in the same turn share a timestamp).
	sort.Slice(uses, func(i, j int) bool {
		if uses[i].at.Equal(uses[j].at) {
			return uses[i].name < uses[j].name
		}
		return uses[i].at.After(uses[j].at)
	})
	out := make([]string, 0, len(uses))
	for _, u := range uses {
		out = append(out, u.name)
	}
	return out
}

// suggestToolScope queues a one-time Permissions-pane suggestion to scope a
// repeatedly-loaded tool to this agent. Accepting routes through handleApprove's
// "scope_tool" case, which ADDS the agent to the tool's ScopeAgents — making it
// first-class for this agent and (when the tool was shared) removing it from the
// general pool, which is exactly what the text warns.
//
// It is an OFFER and must read as one (approvalIsSuggestion keeps it out of the
// rail badge and out of the conversation): the agent is already calling this
// tool — three sessions of it is the evidence — so nothing is blocked, nothing
// was refused, and ignoring this forever costs a little schema latency and
// nothing else.
//
// Fires ONCE per (agent, tool): the threshold test below trips only on the
// crossing, so a dismissed suggestion stays dismissed however many more times
// the tool is loaded. The pending-dedupe loop is the narrower guard, for a
// suggestion still sitting unanswered in the pane.
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
	var names []string
	byName := map[string]AgentToolDef{}
	for _, td := range all {
		names = append(names, td.Tool.Name)
		byName[td.Tool.Name] = td
	}
	// Tier 2 — proven-by-use kit.
	for name := range promotedByLoadHistory(t.udb, t.agent.ID) {
		if !kit[name] && !isTrialTool(sess, name) {
			elevated[name] = "repeated-load"
		}
	}
	// Tier 3 — the agent has called this tool successfully before. Filtered
	// against `all` because history outlives the tool: a name the agent no
	// longer has must not be elevated, or counted against the cap.
	promotions := 0
	for _, name := range promotedByPriorSuccess(t.udb, t.agent.ID) {
		if promotions >= toolSuccessElevateCap {
			break
		}
		td, have := byName[name]
		if !have || kit[name] || elevated[name] != "" || len(td.Tool.Parameters) == 0 || isTrialTool(sess, name) {
			continue
		}
		elevated[name] = "prior-success"
		promotions++
	}
	// Tier 1 — the turn's intent names the tool (or its credential's host).
	intent := strings.ToLower(t.intentText)
	if intent == "" {
		return elevated
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
