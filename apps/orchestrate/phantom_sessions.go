// Phantom-dispatched session surfaces for Agency. Phantom runs agent
// dispatches under synthetic per-chat users ("phantom:<chatID>") so
// chat-A's recall doesn't leak into chat-B's. Those sessions land in
// each synthetic user's per-user DB and aren't visible to the agent
// owner via the normal session list (which reads only the logged-in
// user's bucket). These handlers expose them as a read-only view.
//
// Discovery uses core.ListForeignUsers — phantom registers a lister
// at startup that returns every chat's "phantom:<chatID>" id. We
// walk that list, scope to phantom IDs, and check each per-chat DB
// for sessions in the agent's bucket. Empty results return cleanly
// (phantom may not be loaded; lister returns nil).

package orchestrate

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

const phantomUserPrefix = "phantom:"

// phantomSessionRow is the per-row shape for the list endpoint. ChatID
// is the bare chat identifier (with "phantom:" stripped) so the UI can
// display it directly; SessionID is the deterministic dispatch ID for
// the get-one path.
type phantomSessionRow struct {
	ChatID    string `json:"chat_id"`
	SessionID string `json:"session_id"`
	Title     string `json:"title,omitempty"`
	Created   string `json:"created,omitempty"`
	LastAt    string `json:"last_at,omitempty"`
	Messages  int    `json:"messages"`
}

// handleAgentPhantomSessions returns every phantom-dispatched session
// the agent has across every phantom chat (GET), or wipes the entire
// per-chat session bucket for the agent (DELETE).
//
// DELETE without a chat_id query nukes every phantom session for the
// agent across all chats — the "blank out everything phantom has
// accumulated for this agent" admin operation. DELETE with
// chat_id=<id> scopes the wipe to one chat's sub-store. The contaminated-
// context recovery path the operator reaches for when an agent's
// phantom-side memory needs a hard reset.
func (T *OrchestrateApp) handleAgentPhantomSessions(w http.ResponseWriter, r *http.Request, user, agentID string) {
	if r.Method == http.MethodDelete {
		T.handlePhantomSessionsWipe(w, r, user, agentID)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if T.DB == nil {
		http.Error(w, "DB not initialized", http.StatusInternalServerError)
		return
	}
	// Owner gate against the caller's own DB — Agency users can't
	// peek at phantom sessions for agents they don't own.
	udb := UserDB(T.DB, user)
	if udb == nil {
		http.Error(w, "no user DB", http.StatusInternalServerError)
		return
	}
	agent, ok := loadAgent(udb, agentID)
	if !ok || (agent.Owner != user && agent.Owner != seedOwner) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]phantomSessionRow{})
		return
	}
	out := make([]phantomSessionRow, 0)
	for _, fuid := range ListForeignUsers() {
		if !strings.HasPrefix(fuid, phantomUserPrefix) {
			continue
		}
		chatDB := UserDB(T.DB, fuid)
		if chatDB == nil {
			continue
		}
		chatID := strings.TrimPrefix(fuid, phantomUserPrefix)
		for _, s := range listChatSessions(chatDB, agentID) {
			row := phantomSessionRow{
				ChatID:    chatID,
				SessionID: s.ID,
				Title:     s.Title,
			}
			if !s.Created.IsZero() {
				row.Created = s.Created.Format("2006-01-02T15:04:05Z07:00")
			}
			if !s.LastAt.IsZero() {
				row.LastAt = s.LastAt.Format("2006-01-02T15:04:05Z07:00")
			}
			// listChatSessions strips messages for the wire payload —
			// load the full session to count. Reasonable: phantom
			// dispatches typically have small histories, and this view
			// is operator-only.
			full, _ := loadChatSession(chatDB, agentID, s.ID)
			row.Messages = len(full.Messages)
			out = append(out, row)
		}
	}
	// Most-recent first across all chats so the operator sees fresh
	// dispatches at the top regardless of which chat fired them.
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt > out[j].LastAt })
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleAgentPhantomSessionOne returns the full session for one phantom
// dispatch (GET) or deletes it (DELETE), scoped to the per-chat sub-
// store via ?chat_id=<id>. DELETE is the per-row clear from the table.
func (T *OrchestrateApp) handleAgentPhantomSessionOne(w http.ResponseWriter, r *http.Request, user, agentID, sessionID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	chatID := strings.TrimSpace(r.URL.Query().Get("chat_id"))
	if chatID == "" {
		http.Error(w, "chat_id query param required", http.StatusBadRequest)
		return
	}
	if T.DB == nil {
		http.Error(w, "DB not initialized", http.StatusInternalServerError)
		return
	}
	// Owner gate against the caller's own DB.
	udb := UserDB(T.DB, user)
	if udb == nil {
		http.Error(w, "no user DB", http.StatusInternalServerError)
		return
	}
	agent, ok := loadAgent(udb, agentID)
	if !ok || (agent.Owner != user && agent.Owner != seedOwner) {
		http.NotFound(w, r)
		return
	}
	chatDB := UserDB(T.DB, phantomUserPrefix+chatID)
	if chatDB == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodDelete {
		deleteChatSession(chatDB, agentID, sessionID)
		Log("[orchestrate.phantom-sessions] wiped session chat=%s agent=%s session=%s by user=%s",
			chatID, agentID, sessionID, user)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	sess, found := loadChatSession(chatDB, agentID, sessionID)
	if !found {
		http.NotFound(w, r)
		return
	}
	// HistoryPanel expects a flat array of messages. Return just the
	// Messages slice; the row-table above already exposes the metadata
	// (title, created, last_at, count). Empty slice when the session
	// has no turns yet.
	if sess.Messages == nil {
		sess.Messages = []ChatMessage{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sess.Messages)
}

// handlePhantomSessionsWipe drops every phantom session for the agent,
// either across all chats (no chat_id query) or scoped to one chat
// (chat_id=<id>). Used by the Agency editor's "Wipe all phantom
// sessions" button when phantom-side state needs a hard reset that
// won't come back via the normal continuity model.
func (T *OrchestrateApp) handlePhantomSessionsWipe(w http.ResponseWriter, r *http.Request, user, agentID string) {
	if T.DB == nil {
		http.Error(w, "DB not initialized", http.StatusInternalServerError)
		return
	}
	udb := UserDB(T.DB, user)
	if udb == nil {
		http.Error(w, "no user DB", http.StatusInternalServerError)
		return
	}
	agent, ok := loadAgent(udb, agentID)
	if !ok || (agent.Owner != user && agent.Owner != seedOwner) {
		http.NotFound(w, r)
		return
	}
	chatScope := strings.TrimSpace(r.URL.Query().Get("chat_id"))
	wiped := 0
	for _, fuid := range ListForeignUsers() {
		if !strings.HasPrefix(fuid, phantomUserPrefix) {
			continue
		}
		thisChatID := strings.TrimPrefix(fuid, phantomUserPrefix)
		// Scope filter — when chat_id is set, only nuke that chat's
		// sub-store. Otherwise sweep every phantom chat.
		if chatScope != "" && thisChatID != chatScope {
			continue
		}
		chatDB := UserDB(T.DB, fuid)
		if chatDB == nil {
			continue
		}
		for _, s := range listChatSessions(chatDB, agentID) {
			deleteChatSession(chatDB, agentID, s.ID)
			wiped++
		}
	}
	Log("[orchestrate.phantom-sessions] wiped %d session(s) agent=%s chat_scope=%q by user=%s",
		wiped, agentID, chatScope, user)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"wiped": wiped})
}
