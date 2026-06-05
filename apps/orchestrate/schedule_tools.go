// LLM-facing tool for recurring background tasks — a single grouped
// `recurring` tool with action = schedule | list | cancel (mirrors the
// `video` / `agents` grouped-tool pattern). Closure-bound to the calling
// chatTurn so it picks up (user, agent, session) without arg plumbing.
//
// schedule is the only WRITE; list/cancel are inspection/teardown. Cap on
// active recurring tasks per session is enforced inside
// ScheduleOrchestrateUpdate; this layer just hands user-friendly errors
// back to the LLM.

package orchestrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// recurringToolDef is the grouped entry point for recurring background
// tasks. One schema instead of three (schedule_recurring / list_recurring
// / cancel_recurring), picked by `action`.
func (t *chatTurn) recurringToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "recurring",
			Description: "Manage RECURRING background tasks for THIS session — tasks that fire at a fixed interval and append their reply to the session (the user sees it next time they open it). Each fire runs against this agent's persona / tools / memory like a live turn. Pick the action:\n" +
				"  action=\"schedule\" — set one up. Required: prompt (the directive run each fire, e.g. \"check the build, post if red\" — don't include the interval) + interval_minutes (>=1). Guardrails: min 1 min, max 5 active per session, max 50 fires before auto-cancel.\n" +
				"  action=\"list\" — show active tasks (id, interval, fire count, prompt). Call before scheduling to avoid duplicates.\n" +
				"  action=\"cancel\" — stop one. Required: id (from schedule or list).\n" +
				"NOT for one-shot work, and NOT for dispatching to other agents.",
			Parameters: map[string]ToolParam{
				"action":           {Type: "string", Enum: []string{"schedule", "list", "cancel"}, Description: "schedule | list | cancel."},
				"prompt":           {Type: "string", Description: "(schedule) The recurring task as a directive the agent follows each fire. Don't include the interval — that's interval_minutes."},
				"interval_minutes": {Type: "integer", Description: "(schedule) How often the task fires, in minutes. Minimum 1."},
				"id":               {Type: "string", Description: "(cancel) Scheduler task id of the recurring task to cancel (from schedule or list)."},
			},
			Required: []string{"action"},
			Caps:     []Capability{CapRead, CapWrite},
		},
		Handler: func(args map[string]any) (string, error) {
			switch strings.ToLower(strings.TrimSpace(stringArg(args, "action"))) {
			case "schedule":
				return t.recurringSchedule(args)
			case "list":
				return t.recurringList()
			case "cancel":
				return t.recurringCancel(args)
			case "", "help":
				return "recurring actions: schedule (prompt + interval_minutes) | list | cancel (id).", nil
			default:
				return "", fmt.Errorf("unknown action %q for recurring — use schedule | list | cancel", stringArg(args, "action"))
			}
		},
	}
}

func (t *chatTurn) recurringSchedule(args map[string]any) (string, error) {
	if t.session == nil || t.session.ID == "" {
		return "", errors.New("recurring(schedule) requires an active session — start a turn first")
	}
	prompt := strings.TrimSpace(stringArg(args, "prompt"))
	minutes := intFromArgs(args, "interval_minutes")
	if minutes < 1 {
		return "", errors.New("interval_minutes must be >= 1")
	}
	id, err := ScheduleOrchestrateUpdate(t.session.ID, t.agent.ID, t.user, prompt, minutes*60)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("SCHEDULED_OK id=%s every %d min. The task will fire in the background and append a reply to this session each cycle. Use recurring(action=\"list\") or recurring(action=\"cancel\", id=%q) later.", id, minutes, id), nil
}

func (t *chatTurn) recurringList() (string, error) {
	if t.session == nil || t.session.ID == "" {
		return "(no active session)", nil
	}
	updates := ListOrchestrateUpdates(t.session.ID)
	if len(updates) == 0 {
		return "(no recurring tasks for this session)", nil
	}
	// Need a stable JSON shape but include the scheduler task id (which
	// is the cancel key) — re-fetch with the ids.
	type row struct {
		ID              string `json:"id"`
		Prompt          string `json:"prompt"`
		IntervalMinutes int    `json:"interval_minutes"`
		FireCount       int    `json:"fire_count"`
		CreatedAt       string `json:"created_at"`
	}
	var rows []row
	for _, task := range ListScheduledTasks(OrchestrateScheduledUpdateKind) {
		var p orchUpdatePayload
		if json.Unmarshal(task.Payload, &p) != nil || p.SessionID != t.session.ID {
			continue
		}
		rows = append(rows, row{
			ID: task.ID, Prompt: p.Prompt,
			IntervalMinutes: p.IntervalSeconds / 60,
			FireCount:       p.FireCount, CreatedAt: p.CreatedAt,
		})
	}
	b, _ := json.MarshalIndent(rows, "", "  ")
	return string(b), nil
}

func (t *chatTurn) recurringCancel(args map[string]any) (string, error) {
	if t.session == nil || t.session.ID == "" {
		return "", errors.New("recurring(cancel) requires an active session")
	}
	id := strings.TrimSpace(stringArg(args, "id"))
	if id == "" {
		return "", errors.New("id is required for recurring(cancel)")
	}
	if err := CancelOrchestrateUpdate(t.session.ID, id); err != nil {
		return "", err
	}
	return fmt.Sprintf("CANCELLED ok. Recurring task %s removed.", id), nil
}
