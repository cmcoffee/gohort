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

// handleConvInfo returns a conversation's identity (members + alias handles) for
// the member editor.
//
//	GET /bridges/api/conv-info/{chat_id} → Convo
func (T *Bridges) handleConvInfo(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	chatID := strings.TrimPrefix(r.URL.Path, "/api/conv-info/")
	if chatID == "" {
		http.Error(w, "chat_id required", http.StatusBadRequest)
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
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	chatID := strings.TrimPrefix(r.URL.Path, "/api/conversation/")
	if chatID == "" {
		http.Error(w, "chat_id required", http.StatusBadRequest)
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
	c, _ := T.getConvo(chatID)
	c.ChatID = chatID
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
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	chatID := strings.TrimPrefix(r.URL.Path, "/api/messages/")
	if chatID == "" {
		http.Error(w, "chat_id required", http.StatusBadRequest)
		return
	}
	msgs := T.recentMessages(chatID, 50)
	if msgs == nil {
		msgs = []StoredMessage{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(msgs)
}
