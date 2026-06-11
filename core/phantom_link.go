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
	DescribeChat(owner, chatID string) (PhantomChatSummary, bool)
	// ResolveRecipient turns a loose recipient string — a contact/group display
	// name, a handle (phone/email), or a chat_id, ANY identifier the caller saw
	// in ListChats — into a concrete chat. This lets a caller (the Operator's
	// LLM) address a recipient the natural way ("WiWee") instead of tracking an
	// opaque chat id. Returns the matched summary (ChatID set for a known
	// conversation, or just Handle for a brand-new handle-shaped recipient). ok
	// is false when the string matches no conversation and isn't handle-shaped.
	ResolveRecipient(owner, to string) (PhantomChatSummary, bool)
	// StartGoalConversation asks phantom to run an autonomous, multi-turn
	// conversation with `handle` toward `goal`, on behalf of the caller (the
	// Operator). Phantom sends the opening message, then drives the exchange
	// as the contact replies (async — nobody blocks), and when the goal is met
	// or it's stuck it calls back into the caller's agent thread.
	//
	// Address the recipient by chatID (preferred — unambiguous; from ListChats)
	// or, when chatID is empty, by handle (a brand-new 1:1 not yet in a thread).
	//
	// operatorAgent + operatorThread name that back-edge target as plain data
	// so core needn't know the caller's session constants: phantom injects the
	// outcome as a turn into operatorThread (run under operatorAgent). Returns
	// a task id for correlation.
	StartGoalConversation(owner, chatID, handle, goal, operatorAgent, operatorThread string) (taskID string, err error)
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
