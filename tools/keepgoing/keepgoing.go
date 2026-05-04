// Package keepgoing provides the keep_going chat tool — the proactive
// "I have more work to do" signal. When the LLM knows it needs another
// round but isn't ready to call the real tool yet (still planning, or
// the next step depends on context the current round just produced),
// it calls keep_going and the agent loop runs another round.
//
// This is the cheap path. There's also a reactive judge in the agent
// loop that catches cases where the LLM trailed off into "let me try
// X" without signalling continuation; keep_going is just the explicit,
// no-extra-call version.
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
	return "Call when you know you have more work to do but aren't ready to call the real tool yet (still planning the next step, or the next call depends on what you just learned). Returns a brief continuation message; the loop runs another round so you can take the real action then. The reason param is a private note to your next-round self."
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
