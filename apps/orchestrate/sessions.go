package orchestrate

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// sessionTable returns the per-agent session table name. One bucket
// per agent keeps a delete-agent flow trivial (drop the bucket) and
// prevents any cross-agent leakage at the storage layer. The bucket
// prefix is intentionally still "orchestrate_sessions:" — matches the
// pre-merge layout so existing sessions stay accessible after the
// Template/Instance collapse migration (which reuses each instance's
// ID as the new agent ID).
func sessionTable(agentID string) string {
	return "orchestrate_sessions:" + agentID
}

// loadChatSession fetches a session from an agent's bucket.
func loadChatSession(db Database, agentID, sessionID string) (ChatSession, bool) {
	var s ChatSession
	if db == nil || agentID == "" || sessionID == "" {
		return s, false
	}
	ok := db.Get(sessionTable(agentID), sessionID, &s)
	if ok {
		s.AgentID = agentID
	}
	return s, ok
}

// saveChatSession upserts a session. Caller fills out fields; we stamp
// LastAt and assign an ID + Created if missing.
func saveChatSession(db Database, s ChatSession) (ChatSession, error) {
	if db == nil {
		return s, fmt.Errorf("db not initialized")
	}
	if s.AgentID == "" {
		return s, fmt.Errorf("AgentID is required")
	}
	now := time.Now()
	if s.ID == "" {
		s.ID = UUIDv4()
		s.Created = now
	}
	s.LastAt = now
	// Channel-model groundwork: every thread records its membership. Stage 1
	// has exactly one agent per thread (the lead); the human owner is implicit
	// in the user-scoped db. Additive — nothing reads it yet (see
	// docs/channel-model.md), so this just keeps the field populated for Stage 2.
	ensureLeadParticipant(&s)
	db.Set(sessionTable(s.AgentID), s.ID, s)
	return s, nil
}

// leadAgent returns the thread's lead agent — the agent that fields human
// input. In the 1:1 session case (Stage 1) that is the session's single
// AgentID. Falls back to the first agent participant for future shared threads.
func leadAgent(s ChatSession) string {
	if s.AgentID != "" {
		return s.AgentID
	}
	for _, p := range s.Participants {
		if p.Kind == ParticipantAgent && p.ID != "" {
			return p.ID
		}
	}
	return ""
}

// ensureLeadParticipant guarantees the session's lead agent is present in
// Participants exactly once. Idempotent; safe to call on every save.
func ensureLeadParticipant(s *ChatSession) {
	lead := leadAgent(*s)
	if lead == "" {
		return
	}
	for _, p := range s.Participants {
		if p.Kind == ParticipantAgent && p.ID == lead {
			return
		}
	}
	s.Participants = append(s.Participants, Participant{Kind: ParticipantAgent, ID: lead})
}

// ListChatSessions is the exported entry point for callers that need
// to enumerate an agent's sessions in a different per-user scope —
// notably phantom, which surfaces its phantom:<chatID>-scoped
// dispatches in Agency via the ExtraSessionsSource registry. Same
// shape as the unexported listChatSessions; just public.
func ListChatSessions(db Database, agentID string) []ChatSession {
	return listChatSessions(db, agentID)
}

// LoadChatSession is the exported variant of loadChatSession so
// external session sources (phantom) can resolve a session row
// back into a full transcript for Agency's load handler.
func LoadChatSession(db Database, agentID, sessionID string) (ChatSession, bool) {
	return loadChatSession(db, agentID, sessionID)
}

// listChatSessions returns all sessions for an agent, most-recently-used
// first. The AgentLoopPanel's session sidebar sorts client-side too,
// but ordering here keeps the wire payload tidy.
func listChatSessions(db Database, agentID string) []ChatSession {
	if db == nil {
		return nil
	}
	tbl := sessionTable(agentID)
	var out []ChatSession
	for _, k := range db.Keys(tbl) {
		// Ephemeral agents(run) dispatch continuity is stored as a session
		// keyed "dispatch:<parentSessID>:<target.ID>" so follow-ups re-thread.
		// It's internal plumbing, not a user-facing thread; keep it out of
		// the session rail.
		if strings.HasPrefix(k, "dispatch:") {
			continue
		}
		var s ChatSession
		if !db.Get(tbl, k, &s) {
			continue
		}
		// Don't leak messages into the listing payload — sidebar only
		// needs ID / Title / LastAt + the computed unread flag. Saves
		// bandwidth on long sessions.
		out = append(out, ChatSession{
			ID:      s.ID,
			AgentID: agentID,
			Title:   s.Title,
			Created: s.Created,
			LastAt:  s.LastAt,
			Unread:  s.LastAt.After(s.LastSeen),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt.After(out[j].LastAt) })
	return out
}

// renameChatSession sets a session's Title. Writes directly (NOT via
// saveChatSession) so it does NOT bump LastAt — renaming isn't activity and
// shouldn't reorder the session to the top. No-op if the session is missing or
// the name is blank.
func renameChatSession(db Database, agentID, sessionID, name string) {
	name = strings.TrimSpace(name)
	if db == nil || agentID == "" || sessionID == "" || name == "" {
		return
	}
	if len(name) > 120 {
		name = name[:120]
	}
	tbl := sessionTable(agentID)
	var s ChatSession
	if !db.Get(tbl, sessionID, &s) {
		return
	}
	s.Title = name
	db.Set(tbl, sessionID, s)
}

// markAllSessionsSeen clears the unread state on every session of an agent —
// the "mark all read" action. Like markSessionSeen it writes LastSeen only
// (never bumps LastAt), so it doesn't count as activity. Returns how many it
// updated.
func markAllSessionsSeen(db Database, agentID string) int {
	if db == nil || agentID == "" {
		return 0
	}
	tbl := sessionTable(agentID)
	n := 0
	for _, k := range db.Keys(tbl) {
		var s ChatSession
		if !db.Get(tbl, k, &s) {
			continue
		}
		if !s.LastAt.After(s.LastSeen) {
			continue
		}
		s.LastSeen = s.LastAt
		db.Set(tbl, k, s)
		n++
	}
	return n
}

// markSessionSeen records that the user has now viewed a session: it sets
// LastSeen to the session's current LastAt, clearing the unread state. It
// writes directly (NOT via saveChatSession) so it does NOT bump LastAt —
// viewing a thread is not activity on it. Idempotent and cheap: a no-op when
// the session is already seen or doesn't exist.
func markSessionSeen(db Database, agentID, sessionID string) {
	if db == nil || agentID == "" || sessionID == "" {
		return
	}
	tbl := sessionTable(agentID)
	var s ChatSession
	if !db.Get(tbl, sessionID, &s) {
		return
	}
	if !s.LastAt.After(s.LastSeen) {
		return // already seen
	}
	s.LastSeen = s.LastAt
	db.Set(tbl, sessionID, s)
}

// deleteChatSession removes one session from an agent's bucket and
// clears the side-table slots keyed by the same session id (authoring
// state, draft temp tools) so dead pointers don't accumulate.
func deleteChatSession(db Database, agentID, sessionID string) {
	if db == nil || agentID == "" || sessionID == "" {
		return
	}
	db.Unset(sessionTable(agentID), sessionID)
	clearAuthoringInProgress(db, sessionID)
	DeleteSessionTempTools(db, sessionID)
	// Also drop the rolling-summary / fold cursor for this session, so
	// clearing an orchestrator thread is a COMPLETE wipe. Otherwise the
	// summary (prepended to every turn) outlives the history and re-primes
	// the thread with its old context. No-op when no compact-state row exists.
	deleteCompactState(db, agentID, sessionID)
	// And the continuity archive: folded spans ingested for [history] recall
	// live under lcm:<agent>:<session> in this same db. Without this wipe,
	// "clear thread" left the conversation's content recoverable via recall —
	// a privacy hole — and the archive grew without bound.
	if n := WipeChunksBySourcePrefix(db, operatorLCMSource(agentID, sessionID)); n > 0 {
		Log("[orchestrate.sessions] dropped %d archived history chunk(s) for agent=%s session=%s", n, agentID, sessionID)
	}
}

// dropChatSessionBucket wipes every session for an agent. Called from
// the agent-delete path so removing an agent fully reclaims its
// storage.
func dropChatSessionBucket(db Database, agentID string) {
	if db == nil || agentID == "" {
		return
	}
	tbl := sessionTable(agentID)
	for _, k := range db.Keys(tbl) {
		db.Unset(tbl, k)
	}
	// Every session's continuity archive shares the lcm:<agent>: source
	// prefix — one sweep reclaims them all (operatorLCMSource with an empty
	// session id IS that prefix).
	if n := WipeChunksBySourcePrefix(db, operatorLCMSource(agentID, "")); n > 0 {
		Log("[orchestrate.sessions] dropped %d archived history chunk(s) for deleted agent=%s", n, agentID)
	}
}

// titleFromFirstMessage derives a short session title from the first
// user message. Used the moment we save a new session so the sidebar
// has something nameable; an LLM-generated title replaces it after
// the first turn completes (see generateSessionTitle below).
func titleFromFirstMessage(msg string) string {
	t := strings.TrimSpace(msg)
	if len(t) > 60 {
		t = t[:60] + "…"
	}
	if t == "" {
		t = "New session"
	}
	return t
}

// generateSessionTitle asks the worker LLM to produce a short,
// descriptive 4-7 word title for a conversation given its first
// user/assistant exchange. Falls back to the truncated first message
// on LLM error. Ports chat.GenerateSessionTitle for orchestrate so
// the sidebar surfaces what each conversation is actually about
// instead of "Tell me about how to…".
func generateSessionTitle(ctx context.Context, llm LLM, s ChatSession) string {
	if llm == nil || len(s.Messages) == 0 {
		return fallbackTitle(s)
	}
	var userMsg, assistantMsg string
	for _, m := range s.Messages {
		if userMsg == "" && m.Role == "user" {
			userMsg = m.Content
		} else if assistantMsg == "" && m.Role == "assistant" {
			assistantMsg = m.Content
		}
		if userMsg != "" && assistantMsg != "" {
			break
		}
	}
	if userMsg == "" {
		return fallbackTitle(s)
	}
	if len(userMsg) > 600 {
		userMsg = userMsg[:600]
	}
	if len(assistantMsg) > 600 {
		assistantMsg = assistantMsg[:600]
	}
	prompt := "Write a short, specific 4-7 word title for this chat. No quotes, no trailing period, no prefix like \"Title:\". Just the title itself.\n\nUser: " + userMsg
	if assistantMsg != "" {
		prompt += "\n\nAssistant: " + assistantMsg
	}
	resp, err := llm.Chat(ctx, []Message{{Role: "user", Content: prompt}},
		WithSystemPrompt("You name chat conversations with short descriptive titles. Reply with ONLY the title."),
		WithMaxTokens(64),
		WithRouteKey("app.orchestrate.title"),
		WithThink(false),
	)
	if err != nil || resp == nil {
		return fallbackTitle(s)
	}
	title := strings.TrimSpace(resp.Content)
	title = strings.Trim(title, "\"'.")
	if len(title) > 80 {
		title = title[:80]
	}
	if len(strings.Fields(title)) > 14 || title == "" {
		return fallbackTitle(s)
	}
	return title
}

// fallbackTitle derives a best-effort title from the session's first
// user message when the LLM can't be used.
func fallbackTitle(s ChatSession) string {
	for _, m := range s.Messages {
		if m.Role == "user" {
			t := strings.TrimSpace(m.Content)
			if words := strings.Fields(t); len(words) > 8 {
				t = strings.Join(words[:8], " ") + "…"
			}
			if t != "" {
				return t
			}
		}
	}
	return "New session"
}
