package bridges

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// channelThreadsImpl exposes Bridges' stored conversations + outbound to
// orchestrate's channel-scoped chat tools (list_chats / read_chat / send_message).
// Registered at startup so orchestrate can read/send over channels without
// importing Bridges. Single-owner deployment: the owner arg is informational.
type channelThreadsImpl struct{ T *Bridges }

func (c channelThreadsImpl) Threads(owner string) []ChannelThreadInfo {
	var out []ChannelThreadInfo
	for _, cv := range c.T.listConvos() {
		out = append(out, ChannelThreadInfo{
			ChatID:      cv.ChatID,
			Service:     cv.Service,
			Handle:      cv.Handle,
			DisplayName: firstNonEmpty(cv.DisplayName, cv.Handle, cv.ChatID),
			LastAt:      cv.LastAt,
		})
	}
	return out
}

func (c channelThreadsImpl) Members(owner, chatID string) []ChannelMember {
	// syncMembersFromHistory derives the full roster from the stored thread
	// (catches anyone who messaged but wasn't captured live), then returns it.
	conv := c.T.syncMembersFromHistory(chatID)
	out := make([]ChannelMember, 0, len(conv.Members))
	for _, m := range conv.Members {
		if m.Handle == "" {
			continue
		}
		out = append(out, ChannelMember{Name: m.Name, Handle: m.Handle})
	}
	return out
}

func (c channelThreadsImpl) Messages(owner, chatID string, limit int) []ChannelLine {
	if limit <= 0 {
		limit = 30
	}
	var out []ChannelLine
	for _, m := range c.T.recentMessages(chatID, limit) {
		out = append(out, ChannelLine{
			Role:      m.Role,
			Sender:    m.DisplayName,
			Text:      m.Text,
			Timestamp: m.Timestamp,
		})
	}
	return out
}

func (c channelThreadsImpl) Deliver(owner, service, chatID, handle, text string, images []string) error {
	svc := strings.TrimSpace(service)
	if svc == "" {
		// Caller didn't specify a transport (proactive send) — resolve it from
		// the conversation so a Telegram chat's message goes out Telegram, not
		// the iMessage default. Falls back to iMessage when the chat is unknown.
		if cv, ok := c.T.getConvo(chatID); ok && strings.TrimSpace(cv.Service) != "" {
			svc = cv.Service
		}
	}
	svc = firstNonEmpty(svc, "imessage")
	// notify_me + proactive sends address the owner/contact by HANDLE with no
	// chat id; resolve the handle to its conversation so the outbound carries a
	// chat id to route + thread on (the daemon delivers by chat id, and the
	// thread mirror below keys by ChatID — a blank id orphans it). Falls through
	// with chatID="" only when the handle has no thread yet; the daemon then
	// starts a fresh chat to the bare handle.
	if chatID == "" && strings.TrimSpace(handle) != "" {
		for _, cv := range c.T.listConvos() {
			if cv.Handle == handle || containsFold(cv.AliasHandles, handle) {
				chatID = cv.ChatID
				break
			}
		}
	}
	c.T.enqueueOutbox(OutboxItem{ChatID: chatID, Handle: handle, Service: svc, Text: text, Images: images, Type: "reply"})
	// Mirror the outbound into the thread store so the dashboard + read_chat see it.
	c.T.storeMessage(StoredMessage{ID: newToken()[:12], ChatID: chatID, Role: "assistant", Text: text})
	return nil
}
