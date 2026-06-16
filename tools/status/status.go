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

// IsFrameworkTool: send_status is round-shape infrastructure — the
// mid-turn channel every agent is told to use (the prose alongside a
// tool call is dropped). Marking it framework hides it from the
// curation pickers and the default worker pool; each conversational
// surface (orchestrate, phantom) force-includes it explicitly, exactly
// like stay_silent / keep_going. Always-wired, never user-toggleable.
func (t *SendStatusTool) IsFrameworkTool() bool { return true }

// Caps: nil — control-flow / messaging only, no side effects on the
// outside world. Same posture as stay_silent.
func (t *SendStatusTool) Caps() []Capability { return nil }

func (t *SendStatusTool) Desc() string {
	return "Post a brief one-line progress note to the user mid-turn — a heads-up before your final reply (a phase change, progress on slow work like downloads, multi-step tool chains, slow APIs, callbacks). Often you don't need to call this explicitly: a short sentence you write right before a tool call already surfaces as a live status. Reach for send_status when you want to post a progress note in a round where you are NOT also calling a tool. It does NOT replace your final reply — keep producing the actual answer for your last, tool-free turn."
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
