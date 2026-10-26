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
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
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

// phantomSessionsRoute returns every phantom-dispatched session
// the agent has across every phantom chat (GET), or wipes the entire
// per-chat session bucket for the agent (DELETE).
//
// DELETE without a chat_id query nukes every phantom session for the
// agent across all chats — the "blank out everything phantom has
// accumulated for this agent" admin operation. DELETE with
// chat_id=<id> scopes the wipe to one chat's sub-store. The contaminated-
// context recovery path the operator reaches for when an agent's
// phantom-side memory needs a hard reset.
func (T *OrchestrateApp) phantomSessionsRoute() http.HandlerFunc {
	return jsonapi.Methods{
		"GET":    jsonapi.Get(T.DB, T.phantomSessionsList),
		"DELETE": jsonapi.Delete(T.DB, T.phantomSessionsWipe),
	}.Serve
}

// phantomSessionsList is the list itself; an agent that is not the caller's
// lists nothing rather than answering 404.
func (T *OrchestrateApp) phantomSessionsList(c *jsonapi.Call) (any, error) {
	user, udb, agentID := c.User, c.UDB, c.R.PathValue("id")
	// Owner gate against the caller's own DB — Agency users can't
	// peek at phantom sessions for agents they don't own.
	agent, ok := loadAgent(udb, agentID)
	if !ok || (agent.Owner != user && agent.Owner != seedOwner) {
		return []phantomSessionRow{}, nil
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
	return out, nil
}

// phantomSessionRoute returns the full session for one phantom
// dispatch (GET) or deletes it (DELETE), scoped to the per-chat sub-
// store via ?chat_id=<id>. DELETE is the per-row clear from the table.
func (T *OrchestrateApp) phantomSessionRoute() http.HandlerFunc {
	return jsonapi.Methods{
		"GET": jsonapi.Get(T.DB, func(c *jsonapi.Call) (any, error) {
			chatDB, agentID, sessionID, err := T.phantomSession(c)
			if err != nil {
				return nil, err
			}
			sess, found := loadChatSession(chatDB, agentID, sessionID)
			if !found {
				return nil, errNotFound
			}
			// HistoryPanel expects a flat array of messages. Return just the
			// Messages slice; the row-table above already exposes the metadata
			// (title, created, last_at, count). Empty slice when the session
			// has no turns yet.
			if sess.Messages == nil {
				sess.Messages = []ChatMessage{}
			}
			return sess.Messages, nil
		}),
		"DELETE": jsonapi.Delete(T.DB, func(c *jsonapi.Call, _ struct{}) (any, error) {
			chatDB, agentID, sessionID, err := T.phantomSession(c)
			if err != nil {
				return nil, err
			}
			deleteChatSession(chatDB, agentID, sessionID)
			Log("[orchestrate.phantom-sessions] wiped session chat=%s agent=%s session=%s by user=%s",
				strings.TrimSpace(c.R.URL.Query().Get("chat_id")), agentID, sessionID, c.User)
			return jsonapi.Empty(http.StatusNoContent), nil
		}),
	}.Serve
}

// phantomSession resolves /phantom-sessions/{item}?chat_id= to the chat's
// store, for an agent the caller owns.
func (T *OrchestrateApp) phantomSession(c *jsonapi.Call) (chatDB Database, agentID, sessionID string, err error) {
	agentID, sessionID = c.R.PathValue("id"), c.R.PathValue("item")
	chatID := strings.TrimSpace(c.R.URL.Query().Get("chat_id"))
	if chatID == "" {
		return nil, "", "", jsonapi.BadRequest("chat_id query param required")
	}
	// Owner gate against the caller's own DB.
	agent, ok := loadAgent(c.UDB, agentID)
	if !ok || (agent.Owner != c.User && agent.Owner != seedOwner) {
		return nil, "", "", errNotFound
	}
	chatDB = UserDB(T.DB, phantomUserPrefix+chatID)
	if chatDB == nil {
		return nil, "", "", errNotFound
	}
	return chatDB, agentID, sessionID, nil
}

// phantomSessionsWipe drops every phantom session for the agent,
// either across all chats (no chat_id query) or scoped to one chat
// (chat_id=<id>). Used by the Agency editor's "Wipe all phantom
// sessions" button when phantom-side state needs a hard reset that
// won't come back via the normal continuity model.
func (T *OrchestrateApp) phantomSessionsWipe(c *jsonapi.Call, _ struct{}) (any, error) {
	user, agentID := c.User, c.R.PathValue("id")
	agent, ok := loadAgent(c.UDB, agentID)
	if !ok || (agent.Owner != user && agent.Owner != seedOwner) {
		return nil, errNotFound
	}
	chatScope := strings.TrimSpace(c.R.URL.Query().Get("chat_id"))
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
	return map[string]any{"wiped": wiped}, nil
}
