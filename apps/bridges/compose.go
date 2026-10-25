package bridges

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
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
func (T *Bridges) handleConvInfo(c *jsonapi.Call) (any, error) {
	found, _, err := T.ownConvo(c, "/api/conv-info/")
	if err != nil {
		return nil, err
	}
	// Harvest participants from the thread so the roster is complete even for
	// senders we didn't learn live (derive-on-read, like phantom).
	return T.syncMembersFromHistory(found.Owner, found.ChatID), nil
}

// ownConvo is the conversation the request's path names after prefix, when it
// is the caller's: a 400 with no chat id, a 404 for a missing one and for
// another user's alike.
func (T *Bridges) ownConvo(c *jsonapi.Call, prefix string) (Convo, string, error) {
	chatID := strings.TrimPrefix(c.R.URL.Path, prefix)
	if chatID == "" {
		return Convo{}, "", jsonapi.BadRequest("chat_id required")
	}
	cv, ok := T.convoFor(chatID, c.User, RequestIsAdmin(c.R))
	if !ok {
		return Convo{}, "", jsonapi.NotFound("conversation not found")
	}
	return cv, chatID, nil
}

// handleConvUpdate applies edits to a conversation — renamed participants,
// member aliases, conversation-level alias handles (the chat's other ids), and
// its display name.
//
//	PATCH /bridges/api/conversation/{chat_id}  {members, alias_handles, display_name}
func (T *Bridges) handleConvUpdate(c *jsonapi.Call, req convUpdateReq) (any, error) {
	// Edits apply to a conversation that exists and is the caller's; this no
	// longer creates one from a bare id, which would let a user claim a chat id
	// before its real owner's first message arrives.
	cv, _, err := T.ownConvo(c, "/api/conversation/")
	if err != nil {
		return nil, err
	}
	if req.Members != nil {
		cv.Members = *req.Members
	}
	if req.AliasHandles != nil {
		clean := (*req.AliasHandles)[:0:len(*req.AliasHandles)]
		for _, h := range *req.AliasHandles {
			if h = strings.TrimSpace(h); h != "" {
				clean = append(clean, h)
			}
		}
		cv.AliasHandles = clean
	}
	if req.DisplayName != nil {
		cv.DisplayName = strings.TrimSpace(*req.DisplayName)
	}
	T.saveConvo(cv)
	return cv, nil
}

// handleConvDelete removes the conversation (and its thread) — used when
// folding a duplicate into another chat via an alias handle.
func (T *Bridges) handleConvDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	cv, chatID, err := T.ownConvo(c, "/api/conversation/")
	if err != nil {
		return nil, err
	}
	T.deleteConvo(cv.Owner, chatID)
	return map[string]any{"removed": true}, nil
}

// convUpdateReq is what handleConvUpdate is sent: each field present is set,
// each one left out is kept.
type convUpdateReq struct {
	Members      *[]ConvMember `json:"members"`
	AliasHandles *[]string     `json:"alias_handles"`
	DisplayName  *string       `json:"display_name"`
}

// handleMessages returns a conversation's recent thread for the history view.
//
//	GET /bridges/api/messages/{chat_id} → [{role, display_name, text, timestamp}]
func (T *Bridges) handleMessages(c *jsonapi.Call) (any, error) {
	cv, chatID, err := T.ownConvo(c, "/api/messages/")
	if err != nil {
		return nil, err
	}
	msgs := T.recentMessages(cv.Owner, chatID, 50)
	if msgs == nil {
		msgs = []StoredMessage{}
	}
	return msgs, nil
}
