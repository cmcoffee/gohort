// Package keepgoing provides the keep_going chat tool. It's a control-
// flow signal — when the LLM needs another round to take action but
// isn't ready to call the actual tool yet (still gathering context,
// figuring out a body shape, deciding which credential to use), it
// calls keep_going. The tool returns a "Continue with your plan"
// message and the agent loop runs another round.
//
// Without this, the LLM's only way to signal "I'm not done" is to call
// a real tool. When it has nothing to call yet (still planning), it
// often emits text content like "let me try X" and the loop ends —
// because there's content but no tool call. keep_going gives the LLM
// a legitimate empty-handed continuation.
//
// Caps: nil (control flow only, no side effects).

package keepgoing

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterChatTool(&KeepGoingTool{}) }

type KeepGoingTool struct{}

func (t *KeepGoingTool) Name() string         { return "keep_going" }
func (t *KeepGoingTool) Caps() []Capability   { return nil }

func (t *KeepGoingTool) Desc() string {
	return "Signal that you need another round to take action, when you're not ready to call the actual tool yet (still planning, gathering context, deciding which credential to use). Calling keep_going returns a brief continuation message; the loop runs another round and you can call the real tool then. Use INSTEAD of saying 'let me try X' or 'one moment' in your reply text — that text reaches the user as if it's your final answer. keep_going keeps the conversation going invisibly. The reason param is optional but useful for your own next-round context."
}

func (t *KeepGoingTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"reason": {
			Type:        "string",
			Description: "Optional one-line note about what you're about to do next (visible only to you on the next round, not to the user). E.g. 'reading the API error to find the right field name' / 'pulling up the recipient phone number from the conversation'.",
		},
	}
}

func (t *KeepGoingTool) Run(args map[string]any) (string, error) {
	reason := strings.TrimSpace(StringArg(args, "reason"))
	msg := "Continue with the next step of your plan. Call the real tool now."
	if reason != "" {
		msg = fmt.Sprintf("Continue: %s. Call the real tool now.", reason)
	}
	return msg, nil
}
