package chat

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// ChatSessionsTable is the kvlite table name for persisted chat sessions.
const ChatSessionsTable = "chat_sessions"

// ChatMessage is one entry in a session's message history.
type ChatMessage struct {
	Role    string `json:"role"`    // "user" | "assistant"
	Content string `json:"content"`
}

// ChatSession is a persisted chat conversation. Sessions are scoped
// to a username; users only see and can act on their own sessions.
type ChatSession struct {
	ID       string        `json:"ID"`
	Title    string        `json:"Title,omitempty"`
	Username string        `json:"Username,omitempty"`
	Messages []ChatMessage `json:"Messages,omitempty"`
	Archived bool          `json:"Archived,omitempty"`
	Created  time.Time     `json:"Created"`
	LastAt   time.Time     `json:"LastAt"`
}

// GetDate satisfies the shared record interface so sessions can be
// sorted/listed by the same helpers other apps use.
func (s ChatSession) GetDate() string { return s.LastAt.Format(time.RFC3339) }

// LoadChatSession fetches a session by ID. Returns (session, true) on
// success, (zero, false) when the record doesn't exist or the DB is
// not set.
func LoadChatSession(db Database, id string) (ChatSession, bool) {
	var s ChatSession
	if db == nil || id == "" {
		return s, false
	}
	ok := db.Get(ChatSessionsTable, id, &s)
	return s, ok
}

// SaveChatSession upserts a session.
func SaveChatSession(db Database, s ChatSession) {
	if db == nil || s.ID == "" {
		return
	}
	db.Set(ChatSessionsTable, s.ID, s)
}

// DeleteChatSession removes a session from the store.
func DeleteChatSession(db Database, id string) {
	if db == nil || id == "" {
		return
	}
	db.Unset(ChatSessionsTable, id)
}

// ListChatSessionsForUser returns all sessions owned by the given
// username, most-recently-active first. Empty-username sessions
// (legacy or unauthenticated) are returned only to the empty-username
// caller. Nil DB returns an empty slice.
func ListChatSessionsForUser(db Database, username string) []ChatSession {
	if db == nil {
		return nil
	}
	keys := db.Keys(ChatSessionsTable)
	var out []ChatSession
	for _, k := range keys {
		var s ChatSession
		if !db.Get(ChatSessionsTable, k, &s) {
			continue
		}
		if s.Username != username {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt.After(out[j].LastAt) })
	return out
}

// SummaryEntry is the trimmed-for-listing form of a session — omits
// the full Messages slice so the sidebar can render a long list
// without pulling every history entry into memory.
type SummaryEntry struct {
	ID       string    `json:"ID"`
	Title    string    `json:"Title"`
	Archived bool      `json:"Archived,omitempty"`
	Created  time.Time `json:"Created"`
	LastAt   time.Time `json:"LastAt"`
	Preview  string    `json:"Preview,omitempty"` // short snippet of the first user message
}

// SessionSummaries returns lightweight summaries suitable for the
// sidebar. The Preview is the opening ~80 chars of the first user
// message, useful when a title hasn't been generated yet.
func SessionSummaries(sessions []ChatSession) []SummaryEntry {
	out := make([]SummaryEntry, 0, len(sessions))
	for _, s := range sessions {
		preview := ""
		for _, m := range s.Messages {
			if m.Role == "user" {
				preview = strings.TrimSpace(m.Content)
				if len(preview) > 80 {
					preview = preview[:80] + "…"
				}
				break
			}
		}
		title := s.Title
		if title == "" {
			title = preview
		}
		if title == "" {
			title = "New chat"
		}
		out = append(out, SummaryEntry{
			ID: s.ID, Title: title, Archived: s.Archived,
			Created: s.Created, LastAt: s.LastAt, Preview: preview,
		})
	}
	return out
}

// GenerateSessionTitle asks the worker LLM to produce a short,
// descriptive title (4-7 words) for a conversation given its first
// user/assistant exchange. Falls back to a trimmed snippet of the
// first user message on LLM error so a session always ends up with
// *some* title.
func GenerateSessionTitle(ctx context.Context, worker *Session, s ChatSession) string {
	if worker == nil || len(s.Messages) == 0 {
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
	resp, err := worker.Chat(ctx, []Message{{Role: "user", Content: prompt}},
		WithSystemPrompt("You name chat conversations with short descriptive titles. Reply with ONLY the title."),
		WithMaxTokens(64),
		WithThink(false))
	if err != nil {
		return fallbackTitle(s)
	}
	title := strings.TrimSpace(ResponseText(resp))
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
	return "New chat"
}

// sessionUsername returns the owning username for a session request,
// or the empty string when auth is not configured (single-user mode).
// All session scoping runs through this helper so auth-on and auth-off
// deployments behave consistently.
func sessionUsername(r *http.Request) string { return AuthCurrentUser(r) }
