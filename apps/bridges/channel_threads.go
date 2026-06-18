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
	c.T.enqueueOutbox(OutboxItem{ChatID: chatID, Handle: handle, Service: svc, Text: text, Images: images, Type: "reply"})
	// Mirror the outbound into the thread store so the dashboard + read_chat see it.
	c.T.storeMessage(StoredMessage{ID: newToken()[:12], ChatID: chatID, Role: "assistant", Text: text})
	return nil
}
