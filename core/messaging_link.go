// MessagingLink: the agent-aware seam onto the messaging transport (the bridges
// app), registered BY bridges so other apps — orchestrate's operator tools — can
// read conversations and send messages without importing bridges. Same registry
// shape as RegisterStandingRunner / RegisterEventWaker: core owns the seam, the
// transport supplies the behavior. (Formerly served by the now-retired phantom
// app, hence the historical PhantomLink name in older commits.)
//
// Owner-scoped at the interface even though the transport store is currently
// single-tenant (one device owner). Callers pass the gohort user; the transport
// decides whether that user owns the bridge and returns nothing otherwise.

package core

import (
	"sync"
	"time"
)

// MessagingChatSummary is one conversation in the bridge, for listing.
type MessagingChatSummary struct {
	ChatID      string    `json:"chat_id"`
	Handle      string    `json:"handle"`
	DisplayName string    `json:"display_name"`
	LastAt      time.Time `json:"last_at"`
}

// MessagingChatMessage is one message in a conversation. FromMe = sent by us
// (the device owner / phantom persona) rather than the other party.
type MessagingChatMessage struct {
	FromMe bool      `json:"from_me"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
}

// MessagingLink is implemented by the messaging transport (bridges) and consumed
// by orchestrate's operator tools (message_contact / notify_me / console).
type MessagingLink interface {
	// ListChats returns the owner's recent conversations (most recent first).
	ListChats(owner string, limit int) ([]MessagingChatSummary, error)
	// ReadChat returns recent messages from one conversation (oldest first).
	ReadChat(owner, chatID string, limit int) ([]MessagingChatMessage, error)
	// SendToChat enqueues an outbound message to an existing conversation.
	SendToChat(owner, chatID, text string) error
	// SendToHandle enqueues an outbound message to a phone/email handle
	// (resolving to an existing conversation when one matches, else starting a
	// new thread).
	SendToHandle(owner, handle, text string) error
	// DeliverMessage sends a message to a chat on the caller's behalf, routing
	// by whether phantom's persona is live for that chat:
	//   - persona ACTIVE (the chat is auto-reply enabled / phantom answers it):
	//     phantom's per-chat LLM composes the message in its established voice
	//     and conversation context from `text` (treated as the intent), then
	//     sends it.
	//   - persona INACTIVE (phantom doesn't answer that chat): `text` is sent
	//     verbatim — a raw passthrough.
	// images are base64-encoded attachments (the caller's generated files) to
	// send alongside the text; nil/empty for a text-only message. Any phantom
	// control markers (e.g. [ATTACH: ...]) in `text` are stripped before send
	// so agent-internal directives never leak into the message.
	// Prefer chatID (unambiguous; required for groups); handle is the
	// new-individual fallback. Returns the text actually delivered.
	DeliverMessage(owner, chatID, handle, text string, images []string) (sent string, err error)
	// OwnerHandle returns the device owner's own number (for self-notify), or
	// ("", false) when it isn't configured.
	OwnerHandle(owner string) (string, bool)
	// DescribeChat resolves one chat id to its summary (handle + display name)
	// so callers can render a human-readable recipient — e.g. an approval row
	// for a group addressed only by chat_id. ok is false when no such chat.
	DescribeChat(owner, chatID string) (MessagingChatSummary, bool)
	// ResolveRecipient turns a loose recipient string — a contact/group display
	// name, a handle (phone/email), or a chat_id, ANY identifier the caller saw
	// in ListChats — into a concrete chat. This lets a caller (the Operator's
	// LLM) address a recipient the natural way ("WiWee") instead of tracking an
	// opaque chat id. Returns the matched summary (ChatID set for a known
	// conversation, or just Handle for a brand-new handle-shaped recipient). ok
	// is false when the string matches no conversation and isn't handle-shaped.
	ResolveRecipient(owner, to string) (MessagingChatSummary, bool)
}

var (
	messagingLink   MessagingLink
	messagingLinkMu sync.RWMutex
)

// RegisterMessagingLink installs the bridge. Called once at startup by the active
// messaging transport (now bridges; formerly phantom).
func RegisterMessagingLink(p MessagingLink) {
	messagingLinkMu.Lock()
	messagingLink = p
	messagingLinkMu.Unlock()
}

// ActiveMessagingLink returns the registered bridge, or (nil, false) when phantom
// isn't running / hasn't registered yet.
func ActiveMessagingLink() (MessagingLink, bool) {
	messagingLinkMu.RLock()
	defer messagingLinkMu.RUnlock()
	return messagingLink, messagingLink != nil
}
