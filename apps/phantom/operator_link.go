// Phantom side of the core PhantomLink seam: lets the Operator (and any other
// app) read this device's conversations and send messages through phantom's
// outbox, without importing phantom. core/phantom_link.go owns the interface.
//
// Single-tenant gate: phantom is one device owner today, so the link serves
// ONLY the bridge owner (the admin account phantomToolOwner resolves) and
// returns nothing for any other user. That keeps a non-owner user's Operator
// from reading or sending through this phantom even on a shared instance. When
// phantom goes multi-tenant this gate compares against the per-bridge key
// owner instead; nothing on the Operator side changes.

package phantom

import (
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

type phantomLinkImpl struct{ app *Phantom }

// registerOperatorLink wires phantom into the core PhantomLink registry. Called
// from RegisterRoutes once T.DB is set.
func (T *Phantom) registerOperatorLink() {
	RegisterPhantomLink(phantomLinkImpl{app: T})
}

// ownsBridge gates every link call to the single device owner.
func (p phantomLinkImpl) ownsBridge(owner string) bool {
	return owner != "" && owner == phantomToolOwner(p.app.DB)
}

func (p phantomLinkImpl) ListChats(owner string, limit int) ([]PhantomChatSummary, error) {
	if !p.ownsBridge(owner) {
		return nil, nil
	}
	var out []PhantomChatSummary
	for _, k := range p.app.DB.Keys(conversationTable) {
		var c Conversation
		if !p.app.DB.Get(conversationTable, k, &c) || c.AliasOf != "" {
			continue
		}
		s := PhantomChatSummary{ChatID: c.ChatID, Handle: c.Handle, DisplayName: c.DisplayName}
		if msgs := recentMessages(p.app.DB, c.ChatID, 1); len(msgs) > 0 {
			if t, err := time.Parse(time.RFC3339, msgs[len(msgs)-1].Timestamp); err == nil {
				s.LastAt = t
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt.After(out[j].LastAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (p phantomLinkImpl) ReadChat(owner, chatID string, limit int) ([]PhantomChatMessage, error) {
	if !p.ownsBridge(owner) {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	var out []PhantomChatMessage
	for _, m := range recentMessages(p.app.DB, chatID, limit) {
		at, _ := time.Parse(time.RFC3339, m.Timestamp)
		out = append(out, PhantomChatMessage{FromMe: m.Role == "assistant", Text: m.Text, At: at})
	}
	return out, nil
}

func (p phantomLinkImpl) SendToChat(owner, chatID, text string) error {
	if !p.ownsBridge(owner) {
		return fmt.Errorf("not authorized for this phantom bridge")
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("message text is required")
	}
	var c Conversation
	p.app.DB.Get(conversationTable, chatID, &c)
	enqueueOutbox(p.app.DB, OutboxItem{
		ID: newID(), ChatID: chatID, Handle: c.Handle, Text: text, Type: "reply", Created: now(),
	})
	return nil
}

func (p phantomLinkImpl) SendToHandle(owner, handle, text string) error {
	if !p.ownsBridge(owner) {
		return fmt.Errorf("not authorized for this phantom bridge")
	}
	handle = strings.TrimSpace(handle)
	if handle == "" || strings.TrimSpace(text) == "" {
		return fmt.Errorf("handle and text are required")
	}
	chatID := p.chatIDForHandle(handle)
	enqueueOutbox(p.app.DB, OutboxItem{
		ID: newID(), ChatID: chatID, Handle: handle, Text: text, Type: "reply", Created: now(),
	})
	return nil
}

func (p phantomLinkImpl) OwnerHandle(owner string) (string, bool) {
	if !p.ownsBridge(owner) {
		return "", false
	}
	cfg := defaultConfig(p.app.DB)
	h := strings.TrimSpace(cfg.OwnerHandle)
	return h, h != ""
}

// chatIDForHandle resolves an existing conversation for a handle (matching the
// primary handle or any alias) and returns its chat id; absent one, it falls
// back to the iMessage chat-id convention so a first outbound still addresses.
func (p phantomLinkImpl) chatIDForHandle(handle string) string {
	for _, k := range p.app.DB.Keys(conversationTable) {
		var c Conversation
		if !p.app.DB.Get(conversationTable, k, &c) {
			continue
		}
		if strings.EqualFold(c.Handle, handle) {
			return c.ChatID
		}
		for _, a := range c.AliasHandles {
			if strings.EqualFold(a, handle) {
				return c.ChatID
			}
		}
	}
	return "iMessage;-;" + handle
}
