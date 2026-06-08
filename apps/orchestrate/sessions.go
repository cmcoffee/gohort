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
	db.Set(sessionTable(s.AgentID), s.ID, s)
	return s, nil
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
		// needs ID / Title / LastAt. Saves bandwidth on long sessions.
		out = append(out, ChatSession{
			ID:      s.ID,
			AgentID: agentID,
			Title:   s.Title,
			Created: s.Created,
			LastAt:  s.LastAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt.After(out[j].LastAt) })
	return out
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
