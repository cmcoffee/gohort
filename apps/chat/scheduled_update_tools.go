// LLM-facing tools for managing recurring chat updates. Registered
// at package init so they appear in the chat catalog automatically.
// They require sess.ChatSessionID and sess.Username — phantom and
// other apps that don't set these get a clear error.

package chat

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	RegisterChatTool(&ScheduleChatUpdateTool{})
	RegisterChatTool(&ListChatUpdatesTool{})
	RegisterChatTool(&CancelChatUpdateTool{})
}

// ----------------------------------------------------------------------
// schedule_chat_update
// ----------------------------------------------------------------------

type ScheduleChatUpdateTool struct{}

func (t *ScheduleChatUpdateTool) Name() string       { return "schedule_chat_update" }
func (t *ScheduleChatUpdateTool) Caps() []Capability { return nil } // control-flow only; the scheduled fire reuses the user's caps

func (t *ScheduleChatUpdateTool) Desc() string {
	return "Schedule a recurring update that posts back into this chat session. The update fires every N seconds; each fire runs you (the LLM) with the current chat history plus your instruction prompt as a synthetic user turn, and your reply lands as a new assistant turn the user sees on next visit. Use for trackers, monitors, periodic check-ins (e.g. \"every 30 minutes, fetch GME price and post if it moved >2%\", \"every 4 hours, check for new GitHub issues on repo X\"). Min interval 60s, max 5 active updates per session, max 50 fires per task before auto-cancel."
}

func (t *ScheduleChatUpdateTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"prompt": {
			Type:        "string",
			Description: "What to do on each fire. Phrased as instructions to yourself. Be specific about output format (one-line update, multi-paragraph briefing, etc.) and skip-conditions (\"only post if change > 2%\").",
		},
		"interval_seconds": {
			Type:        "integer",
			Description: "How often to fire, in seconds. Minimum 60. Practical examples: 1800 (30 min), 3600 (1 hr), 14400 (4 hr), 86400 (1 day).",
		},
	}
}

func (t *ScheduleChatUpdateTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("schedule_chat_update requires a chat session")
}

func (t *ScheduleChatUpdateTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil || sess.ChatSessionID == "" || sess.Username == "" {
		return "", fmt.Errorf("schedule_chat_update is only available in authenticated chat sessions")
	}
	prompt := strings.TrimSpace(StringArg(args, "prompt"))
	interval := IntArg(args, "interval_seconds")
	id, err := ScheduleChatUpdate(sess.ChatSessionID, sess.Username, prompt, interval)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Scheduled chat update %q. First fire in %d seconds; will continue until you cancel via cancel_chat_update or the auto-cancel cap is reached.", id, interval), nil
}

// ----------------------------------------------------------------------
// list_chat_updates
// ----------------------------------------------------------------------

type ListChatUpdatesTool struct{}

func (t *ListChatUpdatesTool) Name() string       { return "list_chat_updates" }
func (t *ListChatUpdatesTool) Caps() []Capability { return nil }

func (t *ListChatUpdatesTool) Desc() string {
	return "List the recurring updates currently scheduled for this chat session. Returns each update's task ID, prompt, interval, next run time, and fire count — useful for reviewing what you've set up before adding more or cancelling stale ones."
}

func (t *ListChatUpdatesTool) Params() map[string]ToolParam { return map[string]ToolParam{} }

func (t *ListChatUpdatesTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("list_chat_updates requires a chat session")
}

func (t *ListChatUpdatesTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil || sess.ChatSessionID == "" {
		return "", fmt.Errorf("list_chat_updates is only available in chat sessions")
	}
	updates := ListChatUpdatesForSession(sess.ChatSessionID)
	if len(updates) == 0 {
		return "No scheduled updates for this session.", nil
	}
	var b strings.Builder
	for i, u := range updates {
		fmt.Fprintf(&b, "%d. id=%s — every %ds (next: %s, fired %d times)\n   %s\n",
			i+1, u.ID, u.IntervalSeconds, u.NextRunAt, u.FireCount, u.Prompt)
	}
	return b.String(), nil
}

// ----------------------------------------------------------------------
// cancel_chat_update
// ----------------------------------------------------------------------

type CancelChatUpdateTool struct{}

func (t *CancelChatUpdateTool) Name() string       { return "cancel_chat_update" }
func (t *CancelChatUpdateTool) Caps() []Capability { return nil }

func (t *CancelChatUpdateTool) Desc() string {
	return "Cancel a recurring update by its task ID (from list_chat_updates). The update stops immediately; no further fires."
}

func (t *CancelChatUpdateTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"id": {Type: "string", Description: "The task ID of the update to cancel."},
	}
}

func (t *CancelChatUpdateTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("cancel_chat_update requires a chat session")
}

func (t *CancelChatUpdateTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil || sess.ChatSessionID == "" {
		return "", fmt.Errorf("cancel_chat_update is only available in chat sessions")
	}
	id := strings.TrimSpace(StringArg(args, "id"))
	if err := CancelChatUpdate(sess.ChatSessionID, id); err != nil {
		return "", err
	}
	return fmt.Sprintf("Cancelled scheduled update %q.", id), nil
}
