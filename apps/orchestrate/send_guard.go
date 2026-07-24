package orchestrate

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// sendGuardKey feeds AgentLoopConfig.SendGuardKey: it recognizes the
// side-effecting "message a contact/group" tools and returns a per-RECIPIENT
// key so the loop delivers only the first message to a given recipient per
// turn. This catches the "drafted several variations and fired them all"
// mistake (a model spamming the group chat with 4 near-identical jokes) that
// the identical-args loop guard misses, since the drafts differ.
//
// Keyed on recipient alone, not the tool or the text: message_contact and
// send_message to the same person collide, and varied text still collides. A
// blank recipient returns "" (not a guarded send — let it through and fail on
// its own validation). Reads both `to` and its `chat_id` alias.
func sendGuardKey(toolName string, args map[string]any) string {
	switch toolName {
	case "message_contact", "send_message":
		to := strings.TrimSpace(StringArg(args, "to"))
		if to == "" {
			to = strings.TrimSpace(StringArg(args, "chat_id"))
		}
		if to == "" {
			return ""
		}
		return "send:" + strings.ToLower(to)
	}
	return ""
}
