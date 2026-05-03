// Package status provides the send_status chat tool. The LLM uses it to
// deliver a progress update to the user mid-turn ("Working on it — this
// might take a minute") before its final reply is ready. Each app wires
// the delivery channel differently via ToolSession.StatusCallback:
//
//   - chat: SSE event rendered as an in-line status note in the
//     assistant bubble.
//   - phantom (iMessage): enqueued as its own outbox item so the user
//     receives a separate iMessage immediately, before the eventual
//     reply.
//
// The tool is generic — apps that don't set sess.StatusCallback get a
// graceful no-op (the LLM is told the status couldn't be delivered and
// can decide to inline the same wording in its eventual reply instead).
package status

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterChatTool(new(SendStatusTool)) }

// SendStatusTool emits an interim status message via sess.StatusCallback.
type SendStatusTool struct{}

func (t *SendStatusTool) Name() string { return "send_status" }

// Caps: nil — control-flow / messaging only, no side effects on the
// outside world. Same posture as stay_silent.
func (t *SendStatusTool) Caps() []Capability { return nil }

func (t *SendStatusTool) Desc() string {
	return "Send a brief in-progress status message to the user before your final reply is ready. Use when a request is going to take a while (long tool chain, large download, multi-step research) so the user knows you're still working. Examples: 'One moment, working on it.' / 'This may take a minute — searching now.' Do NOT use as a substitute for your final reply; you must still respond normally after."
}

func (t *SendStatusTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"text": {Type: "string", Description: "The status message to deliver. Keep it short (one sentence)."},
	}
}

// Run is the no-session fallback — there's no delivery channel without
// a session, so signal the failure plainly.
func (t *SendStatusTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("send_status requires a session with a StatusCallback wired up")
}

func (t *SendStatusTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("send_status requires a session")
	}
	text := strings.TrimSpace(StringArg(args, "text"))
	if text == "" {
		return "", fmt.Errorf("text is required")
	}
	if sess.StatusCallback == nil {
		// Caller didn't wire delivery. Tell the LLM so it doesn't think
		// the user actually received the status — better to inline the
		// same wording in the final reply than silently drop it.
		return "Status delivery is not available in this app. Skip send_status and include any progress wording in your final reply instead.", nil
	}
	sess.StatusCallback(text)
	return "Status sent. Continue working on the request and produce your final reply when ready.", nil
}
