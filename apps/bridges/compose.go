package bridges

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// Group composition — thread view + participant naming + aliases, lifted from
// phantom. Bridges stores identity + recent messages (transport-level), the
// agent owns persona/behavior.
//
// Every handler here takes a chat_id and answers 404 unless the conversation is
// the caller's (convoFor): missing and another user's read the same, so a chat
// id cannot be probed.

// handleConvInfo returns a conversation's identity (members + alias handles) for
// the member editor.
//
//	GET /bridges/api/conv-info/{chat_id} → Convo
func (T *Bridges) handleConvInfo(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	chatID := strings.TrimPrefix(r.URL.Path, "/api/conv-info/")
	if chatID == "" {
		http.Error(w, "chat_id required", http.StatusBadRequest)
		return
	}
	if _, ok := T.convoFor(chatID, user, RequestIsAdmin(r)); !ok {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	// Harvest participants from the thread so the roster is complete even for
	// senders we didn't learn live (derive-on-read, like phantom).
	c := T.syncMembersFromHistory(chatID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(c)
}

// handleConvUpdate applies edits to a conversation — renamed participants,
// member aliases, conversation-level alias handles (the chat's other ids), and
// its display name.
//
//	PATCH /bridges/api/conversation/{chat_id}  {members, alias_handles, display_name}
func (T *Bridges) handleConvUpdate(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	chatID := strings.TrimPrefix(r.URL.Path, "/api/conversation/")
	if chatID == "" {
		http.Error(w, "chat_id required", http.StatusBadRequest)
		return
	}
	// Edits apply to a conversation that exists and is the caller's; this no
	// longer creates one from a bare id, which would let a user claim a chat id
	// before its real owner's first message arrives.
	c, found := T.convoFor(chatID, user, RequestIsAdmin(r))
	if !found {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	// DELETE removes the conversation (and its thread) — used when folding a
	// duplicate into another chat via an alias handle.
	if r.Method == http.MethodDelete {
		T.deleteConvo(chatID)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"removed": true})
		return
	}
	var req struct {
		Members      *[]ConvMember `json:"members"`
		AliasHandles *[]string     `json:"alias_handles"`
		DisplayName  *string       `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Members != nil {
		c.Members = *req.Members
	}
	if req.AliasHandles != nil {
		clean := (*req.AliasHandles)[:0:len(*req.AliasHandles)]
		for _, h := range *req.AliasHandles {
			if h = strings.TrimSpace(h); h != "" {
				clean = append(clean, h)
			}
		}
		c.AliasHandles = clean
	}
	if req.DisplayName != nil {
		c.DisplayName = strings.TrimSpace(*req.DisplayName)
	}
	T.saveConvo(c)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(c)
}

// handleMessages returns a conversation's recent thread for the history view.
//
//	GET /bridges/api/messages/{chat_id} → [{role, display_name, text, timestamp}]
func (T *Bridges) handleMessages(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	chatID := strings.TrimPrefix(r.URL.Path, "/api/messages/")
	if chatID == "" {
		http.Error(w, "chat_id required", http.StatusBadRequest)
		return
	}
	if _, ok := T.convoFor(chatID, user, RequestIsAdmin(r)); !ok {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	msgs := T.recentMessages(chatID, 50)
	if msgs == nil {
		msgs = []StoredMessage{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(msgs)
}
