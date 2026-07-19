// ChannelThreads: the agent-aware seam to the messaging transport (Bridges),
// registered BY the transport so orchestrate's channel-scoped chat tools can
// read conversations and send on an agent's channels WITHOUT orchestrate
// importing the transport (Bridges already imports orchestrate — this inverts
// the dependency, same shape as RegisterStandingRunner / RegisterChannelAgentRunner).
//
// Scope is the CALLER's job: a tool resolves the agent's channels via
// ListChannelsForAgent and filters Threads() to them (a whole-service binding,
// Address=="", widens the scope to every thread on that service; a per-contact
// binding narrows it to one). This seam just exposes the transport's stored
// threads + outbound for an owner; it has no notion of which agent is asking.
package core

import "sync"

// ChannelThreadInfo is one conversation visible through the transport, for
// listing. Owner-scoped at the seam (the transport decides what the user owns).
type ChannelThreadInfo struct {
	ChatID      string `json:"chat_id"`
	Service     string `json:"service"`
	Handle      string `json:"handle,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	LastAt      string `json:"last_at,omitempty"`
}

// ChannelLine is one message in a thread. Role is "user" (the other party) or
// "assistant" (the agent); Sender is the per-message author name when known.
type ChannelLine struct {
	Role      string `json:"role"`
	Sender    string `json:"sender,omitempty"`
	Text      string `json:"text"`
	Timestamp string `json:"timestamp,omitempty"`
}

// ChannelMember is one participant in a conversation: a learned display name +
// their handle (phone/email). This is the roster a group-bound agent needs to
// reach a specific participant by number — group messages are attributed by
// name, but the handle lives here.
type ChannelMember struct {
	Name   string `json:"name,omitempty"`
	Handle string `json:"handle,omitempty"`
}

// ChannelThreads is implemented by the messaging transport (Bridges) and
// consumed by orchestrate's channel-scoped chat tools.
type ChannelThreads interface {
	// Threads returns the owner's recent conversations across the transport.
	// The caller filters these to the asking agent's channels.
	Threads(owner string) []ChannelThreadInfo
	// Messages returns recent messages from one conversation (oldest first).
	Messages(owner, chatID string, limit int) []ChannelLine
	// Members returns the participant roster (name + handle) for one
	// conversation, learned from messages. Lets a group-bound agent resolve a
	// participant's number. Empty / single-entry for a 1:1 (the contact is the
	// thread itself).
	Members(owner, chatID string) []ChannelMember
	// Deliver enqueues an outbound message to a conversation (by chatID) or a
	// handle, on a service. images are base64 attachments (nil for text-only).
	// agentName is the display name of the agent that composed the message, used
	// for the optional outbound name tag (empty = untagged / unknown sender).
	Deliver(owner, service, chatID, handle, text, agentName string, images []string) error
}

var (
	channelThreads   ChannelThreads
	channelThreadsMu sync.RWMutex
)

// RegisterChannelThreads installs the transport seam. Call once at startup from
// the transport (Bridges).
func RegisterChannelThreads(c ChannelThreads) {
	channelThreadsMu.Lock()
	channelThreads = c
	channelThreadsMu.Unlock()
}

// ActiveChannelThreads returns the registered transport seam, or (nil, false)
// when no transport has registered yet.
func ActiveChannelThreads() (ChannelThreads, bool) {
	channelThreadsMu.RLock()
	defer channelThreadsMu.RUnlock()
	return channelThreads, channelThreads != nil
}
