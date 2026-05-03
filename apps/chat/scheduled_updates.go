// Scheduled chat updates — the LLM can use schedule_chat_update to set
// up a recurring task that posts back into the user's chat session
// (e.g. "every 30 minutes, fetch GME price and post if it moved >2%").
//
// When the scheduler fires a chat.scheduled_update task it:
//   1. Loads the chat session under its stored Username so auth scope
//      survives across fires.
//   2. Builds a synthesized agent loop with the session's history plus
//      the user-supplied task prompt as a final user turn.
//   3. Runs the same chat tool catalog (caps-filtered) the live UI
//      would have offered, using the chat AppCore's worker LLM.
//   4. Appends the model's reply as a new "assistant" turn in the
//      session — the next time the user opens the chat tab they see it.
//   5. Re-schedules itself for one interval later, unless the task was
//      cancelled or the session no longer exists.
//
// Guardrails:
//   - Min interval 60s (no once-per-second fire bombs).
//   - Max 5 active updates per session.
//   - Max 50 fires per task before auto-cancel (catches runaway loops).

package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const ChatScheduledUpdateKind = "chat.scheduled_update"

const (
	chatUpdateMinInterval = 60 * time.Second
	chatUpdateMaxPerSession = 5
	chatUpdateMaxFires      = 50
)

// chatUpdatePayload is what's stored in the scheduler for a recurring
// chat update. The handler re-schedules itself by emitting an updated
// payload with FireCount incremented.
type chatUpdatePayload struct {
	SessionID       string `json:"session_id"`
	Username        string `json:"username"`
	Prompt          string `json:"prompt"`
	IntervalSeconds int    `json:"interval_seconds"`
	FireCount       int    `json:"fire_count"`
	CreatedAt       string `json:"created_at"`
}

// chatRef is set by the chat agent's RegisterRoutes so the handler
// (which fires asynchronously, not in a request goroutine) has access
// to the LLM and tool catalog. Initialized once; nil until the chat
// app is loaded.
var chatRef *ChatAgent

// registerChatScheduledUpdates wires the scheduler handler. Called
// from the chat agent's RegisterRoutes so chatRef is set before the
// scheduler can fire any task.
func registerChatScheduledUpdates(c *ChatAgent) {
	chatRef = c
	RegisterScheduleHandler(ChatScheduledUpdateKind, handleChatScheduledUpdate)
}

// handleChatScheduledUpdate is the scheduler callback. Loads the chat
// session, runs the LLM with the prompt, appends the reply as a new
// assistant turn, and re-schedules for the next fire.
func handleChatScheduledUpdate(ctx context.Context, raw json.RawMessage) {
	var p chatUpdatePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		Log("[chat/scheduled] payload unmarshal failed: %v", err)
		return
	}
	if chatRef == nil {
		Log("[chat/scheduled] chat agent not initialized, dropping task for session %s", p.SessionID)
		return
	}

	// Auto-cancel if we've fired too many times — catches misbehaving
	// LLMs that scheduled something they didn't mean to.
	if p.FireCount >= chatUpdateMaxFires {
		Log("[chat/scheduled] task for session %s reached %d fires, auto-cancelling", p.SessionID, chatUpdateMaxFires)
		return
	}

	// Load session and verify it still exists + still belongs to the
	// user. If the user deleted it, drop the task silently.
	sess, ok := LoadChatSession(chatRef.AppCore.DB, p.SessionID)
	if !ok || sess.Username != p.Username || sess.Archived {
		Log("[chat/scheduled] session %s missing/changed/archived, dropping task", p.SessionID)
		return
	}

	// Build messages from history. Cap to last 30 turns so a long-
	// running tracker doesn't accumulate ever-growing context.
	history := sess.Messages
	if len(history) > 30 {
		history = history[len(history)-30:]
	}
	msgs := make([]Message, 0, len(history)+1)
	for _, m := range history {
		msgs = append(msgs, Message{Role: m.Role, Content: m.Content})
	}
	// Inject the scheduled task as a synthetic user turn the LLM can
	// see and act on. Mark it clearly so the model knows this isn't a
	// fresh request from the user — just a recurring ping.
	msgs = append(msgs, Message{
		Role: "user",
		Content: fmt.Sprintf(
			"[SCHEDULED UPDATE — fire %d, every %ds] %s",
			p.FireCount+1, p.IntervalSeconds, p.Prompt),
	})

	// Build a transient ToolSession with the same identity / DB / etc.
	// the live chat would have. Workspace is provisioned per-user so
	// tool calls during scheduled fires share the user's workspace.
	ts := &ToolSession{
		LLM:           chatRef.AppCore.LLM,
		LeadLLM:       chatRef.AppCore.LeadLLM,
		Username:      p.Username,
		DB:            chatRef.AppCore.DB,
		ChatSessionID: p.SessionID,
	}
	if ws, err := EnsureWorkspaceDir(p.Username); err == nil {
		ts.WorkspaceDir = ws
	}
	for _, persisted := range LoadPersistentTempTools(ts.DB, p.Username) {
		t := persisted.Tool
		_ = ts.AppendTempTool(&t)
	}

	// Resolve tools the same way live chat does: full catalog minus
	// known dangerous ones, then capability-filtered.
	toolNames := make([]string, 0, 64)
	for _, t := range allowedTools() {
		toolNames = append(toolNames, t.Name())
	}
	tools, err := GetAgentToolsWithSession(ts, toolNames...)
	if err != nil {
		Log("[chat/scheduled] tool resolve failed for session %s: %v", p.SessionID, err)
		return
	}
	tools = FilterToolsByCaps(tools, []Capability{CapRead, CapNetwork})

	resp, _, err := chatRef.AppCore.RunAgentLoop(ctx, msgs, AgentLoopConfig{
		SystemPrompt: chatRef.SystemPrompt(),
		Tools:        tools,
		MaxRounds:    8,
		AllowedCaps:  []Capability{CapRead, CapNetwork},
	})
	if err != nil {
		Log("[chat/scheduled] LLM error for session %s: %v", p.SessionID, err)
		// Don't drop the task on transient errors — re-schedule.
		reschedule(p)
		return
	}
	reply := ""
	if resp != nil {
		reply = strings.TrimSpace(resp.Content)
	}
	if reply == "" {
		Log("[chat/scheduled] empty LLM reply for session %s, skipping append (still re-scheduling)", p.SessionID)
		reschedule(p)
		return
	}

	// Append the reply as a new assistant turn in the session and save.
	sess.Messages = append(sess.Messages, ChatMessage{
		Role:    "assistant",
		Content: reply,
	})
	sess.LastAt = time.Now()
	SaveChatSession(chatRef.AppCore.DB, sess)
	Log("[chat/scheduled] posted update to session %s (fire %d, %d chars)", p.SessionID, p.FireCount+1, len(reply))

	reschedule(p)
}

// reschedule emits the next fire of a recurring chat update.
func reschedule(p chatUpdatePayload) {
	p.FireCount++
	if p.FireCount >= chatUpdateMaxFires {
		// Don't re-schedule past the cap — the next handler call would
		// just bail anyway, and we'd rather not pollute the scheduler.
		return
	}
	next := time.Now().Add(time.Duration(p.IntervalSeconds) * time.Second)
	if _, err := ScheduleTask(ChatScheduledUpdateKind, p, next); err != nil {
		Log("[chat/scheduled] reschedule failed for session %s: %v", p.SessionID, err)
	}
}

// ScheduleChatUpdate is the public helper the schedule_chat_update
// tool calls. Validates input, enforces guardrails, schedules.
func ScheduleChatUpdate(sessionID, username, prompt string, intervalSeconds int) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("schedule_chat_update requires a chat session")
	}
	if username == "" {
		return "", fmt.Errorf("schedule_chat_update requires an authenticated user")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	if intervalSeconds < int(chatUpdateMinInterval/time.Second) {
		return "", fmt.Errorf("interval must be at least %d seconds", int(chatUpdateMinInterval/time.Second))
	}
	// Cap active updates per session so a chatty LLM can't spam.
	active := ListChatUpdatesForSession(sessionID)
	if len(active) >= chatUpdateMaxPerSession {
		return "", fmt.Errorf("session %s already has %d active updates (cap %d) — cancel one first", sessionID, len(active), chatUpdateMaxPerSession)
	}
	p := chatUpdatePayload{
		SessionID:       sessionID,
		Username:        username,
		Prompt:          prompt,
		IntervalSeconds: intervalSeconds,
		FireCount:       0,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	next := time.Now().Add(time.Duration(intervalSeconds) * time.Second)
	id, err := ScheduleTask(ChatScheduledUpdateKind, p, next)
	if err != nil {
		return "", fmt.Errorf("schedule: %w", err)
	}
	return id, nil
}

// ChatUpdateInfo is a flat summary of a scheduled update for tool
// listing. Hides the scheduler internals from the LLM.
type ChatUpdateInfo struct {
	ID              string `json:"id"`
	Prompt          string `json:"prompt"`
	IntervalSeconds int    `json:"interval_seconds"`
	NextRunAt       string `json:"next_run_at"`
	FireCount       int    `json:"fire_count"`
}

// ListChatUpdatesForSession returns all scheduled updates targeting a
// given session, decoded from scheduler payloads.
func ListChatUpdatesForSession(sessionID string) []ChatUpdateInfo {
	var out []ChatUpdateInfo
	for _, t := range ListScheduledTasks(ChatScheduledUpdateKind) {
		var p chatUpdatePayload
		if json.Unmarshal(t.Payload, &p) != nil || p.SessionID != sessionID {
			continue
		}
		out = append(out, ChatUpdateInfo{
			ID:              t.ID,
			Prompt:          p.Prompt,
			IntervalSeconds: p.IntervalSeconds,
			NextRunAt:       t.RunAt,
			FireCount:       p.FireCount,
		})
	}
	return out
}

// CancelChatUpdate removes a scheduled update by ID. Verifies the
// task belongs to the given session before unscheduling so a tool
// call can't cancel a different session's task.
func CancelChatUpdate(sessionID, taskID string) error {
	if sessionID == "" || taskID == "" {
		return fmt.Errorf("session_id and task_id required")
	}
	for _, t := range ListScheduledTasks(ChatScheduledUpdateKind) {
		if t.ID != taskID {
			continue
		}
		var p chatUpdatePayload
		if json.Unmarshal(t.Payload, &p) != nil {
			return fmt.Errorf("task payload unreadable")
		}
		if p.SessionID != sessionID {
			return fmt.Errorf("task does not belong to this session")
		}
		UnscheduleTask(taskID)
		return nil
	}
	return fmt.Errorf("no scheduled update with id %q", taskID)
}
