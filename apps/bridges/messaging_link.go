package bridges

import (
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// messagingLinkImpl makes Bridges the MessagingLink provider — the agent-facing seam
// the Operator's tools (message_contact / notify_me) and console / operator_wake
// use to read conversations and send, without importing bridges. Replaces phantom
// as this seam's implementation. Transport-only: DeliverMessage sends verbatim
// (the bound channel agent is the persona now; bridges doesn't compose). Backed by
// the same convo / message / outbox stores the channel tools use.
type messagingLinkImpl struct{ T *Bridges }

// ownsBridge gates every method to the single deployment owner. Bridges is
// single-tenant; an empty owner (or a match) passes, anything else is refused.
func (p messagingLinkImpl) ownsBridge(owner string) bool {
	o := strings.TrimSpace(p.T.bridgeOwner())
	owner = strings.TrimSpace(owner)
	return o == "" || owner == "" || owner == o
}

func bridgeParseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, strings.TrimSpace(s))
	return t
}

func (p messagingLinkImpl) convoSummary(c Convo) MessagingChatSummary {
	return MessagingChatSummary{
		ChatID:      c.ChatID,
		Handle:      c.Handle,
		DisplayName: firstNonEmpty(c.DisplayName, c.Handle, c.ChatID),
		LastAt:      bridgeParseTime(c.LastAt),
	}
}

func (p messagingLinkImpl) ListChats(owner string, limit int) ([]MessagingChatSummary, error) {
	out := []MessagingChatSummary{}
	if !p.ownsBridge(owner) {
		return out, nil
	}
	for _, c := range p.T.listConvos() { // newest first
		out = append(out, p.convoSummary(c))
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (p messagingLinkImpl) ReadChat(owner, chatID string, limit int) ([]MessagingChatMessage, error) {
	out := []MessagingChatMessage{}
	if !p.ownsBridge(owner) {
		return out, nil
	}
	for _, m := range p.T.recentMessages(chatID, limit) { // oldest first
		out = append(out, MessagingChatMessage{
			FromMe: m.Role == "assistant",
			Text:   m.Text,
			At:     bridgeParseTime(m.Timestamp),
		})
	}
	return out, nil
}

// deliver routes every send through the same outbox path as channel replies:
// service resolved from the conversation, markdown flattened per service, the
// message mirrored into the thread store.
func (p messagingLinkImpl) deliver(owner, chatID, handle, text string, images []string) error {
	return channelThreadsImpl{p.T}.Deliver(owner, "", chatID, handle, text, images)
}

func (p messagingLinkImpl) SendToChat(owner, chatID, text string) error {
	handle := ""
	if c, ok := p.T.getConvo(chatID); ok {
		handle = c.Handle
	}
	return p.deliver(owner, chatID, handle, text, nil)
}

func (p messagingLinkImpl) SendToHandle(owner, handle, text string) error {
	return p.deliver(owner, p.chatIDForHandle(handle), handle, text, nil)
}

func (p messagingLinkImpl) DeliverMessage(owner, chatID, handle, text string, images []string) (string, error) {
	if err := p.deliver(owner, chatID, handle, text, images); err != nil {
		return "", err
	}
	return text, nil
}

func (p messagingLinkImpl) OwnerHandle(owner string) (string, bool) {
	if !p.ownsBridge(owner) {
		return "", false
	}
	h := strings.TrimSpace(p.T.config().SelfHandle)
	return h, h != ""
}

func (p messagingLinkImpl) DescribeChat(owner, chatID string) (MessagingChatSummary, bool) {
	if !p.ownsBridge(owner) {
		return MessagingChatSummary{}, false
	}
	c, ok := p.T.getConvo(chatID)
	if !ok {
		return MessagingChatSummary{}, false
	}
	return p.convoSummary(c), true
}

// ResolveRecipient turns a loose recipient string (display name, handle, chat id,
// or an alias) into a concrete chat. Falls back to a bare handle when the string
// is handle-shaped (phone/email) but matches no existing conversation.
func (p messagingLinkImpl) ResolveRecipient(owner, to string) (MessagingChatSummary, bool) {
	if !p.ownsBridge(owner) {
		return MessagingChatSummary{}, false
	}
	to = strings.TrimSpace(to)
	if to == "" {
		return MessagingChatSummary{}, false
	}
	for _, c := range p.T.listConvos() {
		if c.ChatID == to || c.Handle == to ||
			strings.EqualFold(strings.TrimSpace(c.DisplayName), to) ||
			containsFold(c.AliasHandles, to) {
			return p.convoSummary(c), true
		}
	}
	if looksLikeHandle(to) {
		return MessagingChatSummary{Handle: to, DisplayName: to}, true
	}
	return MessagingChatSummary{}, false
}

// chatIDForHandle returns the chat id of the conversation whose handle (or alias)
// matches, or "" when none — a handle-only send the connector starts fresh.
func (p messagingLinkImpl) chatIDForHandle(handle string) string {
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return ""
	}
	for _, c := range p.T.listConvos() {
		if c.Handle == handle || containsFold(c.AliasHandles, handle) {
			return c.ChatID
		}
	}
	return ""
}

func containsFold(ss []string, s string) bool {
	s = strings.TrimSpace(s)
	for _, x := range ss {
		if strings.EqualFold(strings.TrimSpace(x), s) {
			return true
		}
	}
	return false
}

// looksLikeHandle reports whether a string is phone/email-shaped, so a recipient
// not yet in a conversation can still be addressed directly (a fresh 1:1).
func looksLikeHandle(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.Contains(s, "@") {
		return true // email
	}
	digits := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '+' || r == '-' || r == '(' || r == ')' || r == ' ':
		default:
			return false
		}
	}
	return digits >= 5
}
