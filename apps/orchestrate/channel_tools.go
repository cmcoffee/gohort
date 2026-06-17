package orchestrate

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// channelChatTools builds the CHANNEL-SCOPED messaging tools for an agent that
// has channels — list_chats / read_chat / send_message. Scope is the agent's
// bound channels: a whole-service binding (Address=="") widens to every thread
// on that service; a per-contact binding narrows to that chat. The "global view"
// is just the special case of holding a whole-service channel — no separate
// mode. Returns nil when the agent has no channels or no messaging transport
// (Bridges) has registered.
func channelChatTools(sess *ToolSession, owner, agentID string) []AgentToolDef {
	chans := ListChannelsForAgent(RootDB, owner, agentID)
	if len(chans) == 0 {
		return nil
	}
	ct, ok := ActiveChannelThreads()
	if !ok {
		return nil
	}
	wholeService := map[string]bool{}
	addrSet := map[string]bool{}
	for _, ch := range chans {
		if ch.Service == "" {
			continue // inert channel — no source hooked, nothing to read/send
		}
		if ch.Address == "" {
			wholeService[ch.Service] = true
		} else {
			addrSet[ch.Address] = true
		}
	}
	inScope := func(t ChannelThreadInfo) bool {
		if wholeService[t.Service] {
			return true
		}
		return (t.ChatID != "" && addrSet[t.ChatID]) || (t.Handle != "" && addrSet[t.Handle])
	}
	scopedThreads := func() []ChannelThreadInfo {
		var out []ChannelThreadInfo
		for _, t := range ct.Threads(owner) {
			if inScope(t) {
				out = append(out, t)
			}
		}
		return out
	}
	// resolve turns a loose recipient (display name / handle / chat_id) into a
	// concrete (chatID, handle) WITHIN the agent's scope. A whole-service channel
	// also allows a brand-new handle not yet in a thread; a per-contact channel
	// can only reach its bound chats.
	resolve := func(to string) (chatID, handle string, ok bool) {
		for _, t := range scopedThreads() {
			if to == t.ChatID || (t.Handle != "" && to == t.Handle) || (t.DisplayName != "" && strings.EqualFold(to, t.DisplayName)) {
				return t.ChatID, t.Handle, true
			}
		}
		if len(wholeService) > 0 && looksLikeHandle(to) {
			return "", to, true
		}
		return "", "", false
	}

	return []AgentToolDef{
		{
			Tool: Tool{
				Name:        "list_chats",
				Description: "List the conversations on THIS agent's channels — display name, handle, and chat id — so you can read or message one. Scoped to your channels only (you can't see chats outside them). Read-only.",
				Parameters:  map[string]ToolParam{"limit": {Type: "number", Description: "Max conversations (default 20)."}},
			},
			Handler: func(args map[string]any) (string, error) {
				limit := oArgInt(args, "limit")
				if limit <= 0 {
					limit = 20
				}
				threads := scopedThreads()
				if len(threads) == 0 {
					return "No conversations on your channels yet.", nil
				}
				if len(threads) > limit {
					threads = threads[:limit]
				}
				var b strings.Builder
				for _, t := range threads {
					name := chFirst(t.DisplayName, t.Handle, t.ChatID)
					fmt.Fprintf(&b, "- %s", name)
					if t.Handle != "" && t.Handle != name {
						fmt.Fprintf(&b, " (%s)", t.Handle)
					}
					fmt.Fprintf(&b, " [%s, chat_id: %s]", ServiceDisplayName(t.Service), t.ChatID)
					if t.LastAt != "" {
						fmt.Fprintf(&b, " last %s", t.LastAt)
					}
					b.WriteString("\n")
				}
				return strings.TrimSpace(b.String()), nil
			},
		},
		{
			Tool: Tool{
				Name:        "read_chat",
				Description: "Read recent messages from one conversation on your channels. Use a chat_id from list_chats. Read-only.",
				Parameters: map[string]ToolParam{
					"chat_id": {Type: "string", Description: "The conversation's chat id (from list_chats)."},
					"limit":   {Type: "number", Description: "How many recent messages (default 20)."},
				},
				Required: []string{"chat_id"},
			},
			Handler: func(args map[string]any) (string, error) {
				chatID := strings.TrimSpace(oArgStr(args, "chat_id"))
				if chatID == "" {
					return "", fmt.Errorf("chat_id is required")
				}
				// Enforce scope — only chats on this agent's channels are readable.
				ok := false
				for _, t := range scopedThreads() {
					if t.ChatID == chatID {
						ok = true
						break
					}
				}
				if !ok {
					return "", fmt.Errorf("no chat %q on your channels — use a chat_id from list_chats", chatID)
				}
				msgs := ct.Messages(owner, chatID, oArgInt(args, "limit"))
				if len(msgs) == 0 {
					return "No messages in that conversation yet.", nil
				}
				var b strings.Builder
				for _, m := range msgs {
					who := m.Sender
					if who == "" {
						if m.Role == "assistant" {
							who = "me"
						} else {
							who = "them"
						}
					}
					ts := ""
					if m.Timestamp != "" {
						ts = "[" + m.Timestamp + "] "
					}
					fmt.Fprintf(&b, "%s%s: %s\n", ts, who, m.Text)
				}
				return strings.TrimSpace(b.String()), nil
			},
		},
		{
			Tool: Tool{
				Name:        "send_message",
				Description: "Send a message OUT over one of this agent's channels — to a contact or group on your channels. Set `to` to a display name, handle (phone/email), or chat_id from list_chats. Scoped to your channels only. Contacting a real person is consequential, so it queues for the user's approval unless they've pre-authorized that recipient.",
				Parameters: map[string]ToolParam{
					"to":   {Type: "string", Description: "Recipient as shown by list_chats: a display name, handle (phone/email), or chat_id."},
					"text": {Type: "string", Description: "The message to send. To include a file you generated, save it to your workspace and call workspace(action=\"attach\", path=\"<file>\") first — it rides along."},
				},
				Required: []string{"to", "text"},
			},
			Handler: func(args map[string]any) (string, error) {
				to := strings.TrimSpace(oArgStr(args, "to"))
				text := strings.TrimSpace(oArgStr(args, "text"))
				if to == "" || text == "" {
					return "", fmt.Errorf("to and text are required")
				}
				chatID, handle, ok := resolve(to)
				if !ok {
					return "", fmt.Errorf("no recipient %q on your channels — use a name, handle, or chat_id from list_chats", to)
				}
				recip := operatorRecipientKey(chatID, handle)
				label := chFirst(handle, chatID)
				images := collectMessageAttachments(sess, text)
				if IsContactBlocked(RootDB, owner, recip) {
					return fmt.Sprintf("Messaging %s is blocked in the user's permission settings — not sent.", label), nil
				}
				if IsContactPreAuthorized(RootDB, owner, recip) {
					if _, err := operatorDeliverMessage(owner, chatID, handle, text, images); err != nil {
						return "", err
					}
					return fmt.Sprintf("Sent to %s (you've pre-authorized this recipient).", label), nil
				}
				a := SaveAuthorization(RootDB, Authorization{
					Owner: owner, Action: "send_message", ChatID: chatID, Handle: handle, Text: text, Images: images,
				})
				return fmt.Sprintf("Queued a message to %s for the user's approval (id %s) — it sends once approved.", label, a.ID), nil
			},
		},
	}
}

// looksLikeHandle reports whether a string looks like a phone/email handle, so a
// whole-service channel can address a brand-new recipient not yet in a thread.
func looksLikeHandle(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.Contains(s, "@") {
		return true
	}
	c := s[0]
	return c == '+' || (c >= '0' && c <= '9')
}

// chFirst returns the first non-blank string.
func chFirst(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
