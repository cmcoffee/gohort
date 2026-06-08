// PhantomLink: the agent-aware bridge to the phantom (iMessage/SMS) app,
// registered BY phantom so other apps — the Operator — can read conversations
// and send messages without importing the phantom package. Same registry shape
// as RegisterStandingRunner / RegisterEventWaker: core owns the seam, phantom
// supplies the behavior.
//
// Owner-scoped at the interface even though phantom's store is currently
// single-tenant (one device owner). Callers pass the gohort user; phantom
// decides whether that user owns the bridge and returns nothing otherwise. When
// phantom goes multi-tenant (see the tenancy notes), only phantom's impl
// changes — every caller already threads owner through. See the Operator.

package core

import (
	"sync"
	"time"
)

// PhantomChatSummary is one conversation in the bridge, for listing.
type PhantomChatSummary struct {
	ChatID      string    `json:"chat_id"`
	Handle      string    `json:"handle"`
	DisplayName string    `json:"display_name"`
	LastAt      time.Time `json:"last_at"`
}

// PhantomChatMessage is one message in a conversation. FromMe = sent by us
// (the device owner / phantom persona) rather than the other party.
type PhantomChatMessage struct {
	FromMe bool      `json:"from_me"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
}

// PhantomLink is implemented by phantom and consumed by the Operator.
type PhantomLink interface {
	// ListChats returns the owner's recent conversations (most recent first).
	ListChats(owner string, limit int) ([]PhantomChatSummary, error)
	// ReadChat returns recent messages from one conversation (oldest first).
	ReadChat(owner, chatID string, limit int) ([]PhantomChatMessage, error)
	// SendToChat enqueues an outbound message to an existing conversation.
	SendToChat(owner, chatID, text string) error
	// SendToHandle enqueues an outbound message to a phone/email handle
	// (resolving to an existing conversation when one matches, else starting a
	// new thread).
	SendToHandle(owner, handle, text string) error
	// OwnerHandle returns the device owner's own number (for self-notify), or
	// ("", false) when it isn't configured.
	OwnerHandle(owner string) (string, bool)
}

var (
	phantomLink   PhantomLink
	phantomLinkMu sync.RWMutex
)

// RegisterPhantomLink installs the bridge. Call once at startup from phantom.
func RegisterPhantomLink(p PhantomLink) {
	phantomLinkMu.Lock()
	phantomLink = p
	phantomLinkMu.Unlock()
}

// ActivePhantomLink returns the registered bridge, or (nil, false) when phantom
// isn't running / hasn't registered yet.
func ActivePhantomLink() (PhantomLink, bool) {
	phantomLinkMu.RLock()
	defer phantomLinkMu.RUnlock()
	return phantomLink, phantomLink != nil
}
