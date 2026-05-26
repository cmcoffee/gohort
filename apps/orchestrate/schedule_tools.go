// LLM-facing tools for recurring background tasks — schedule_recurring,
// cancel_recurring, list_recurring. Closure-bound to the calling chatTurn
// so each tool picks up (user, agent, session) without arg plumbing.
//
// schedule_recurring is the only WRITE; the other two are inspection.
// Cap on active recurring tasks per session is enforced inside
// ScheduleOrchestrateUpdate; this layer just hands user-friendly
// errors back to the LLM.

package orchestrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func (t *chatTurn) scheduleRecurringToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "schedule_recurring",
			Description: "Set up a RECURRING background task that posts back into THIS session at a fixed interval. Example: \"every 30 minutes check the build status and post if it's red\" — call schedule_recurring with interval_minutes=30 and prompt=\"check the build status and post if it's red\". Each fire runs against this agent's persona / tools / memory, same as a live turn, and the reply appends to the session as an assistant message — the user sees it next time they open the session. Guardrails: minimum interval 1 minute, max 5 active recurring tasks per session, max 50 fires before auto-cancel. NOT for one-shot work and NOT for dispatching to other agents (use dispatch_to_agent for that). Call list_recurring first if unsure what's already scheduled.",
			Parameters: map[string]ToolParam{
				"prompt": {
					Type:        "string",
					Description: "The recurring task. Phrase it as a directive the agent will follow each fire (\"check the build, post if red\", \"fetch the latest GME price and post if it moved >2%\"). Don't include the interval — that's a separate arg.",
				},
				"interval_minutes": {
					Type:        "integer",
					Description: "How often the task fires, in minutes. Minimum 1.",
				},
			},
			Required: []string{"prompt", "interval_minutes"},
			Caps:     []Capability{CapWrite},
		},
		Handler: func(args map[string]any) (string, error) {
			if t.session == nil || t.session.ID == "" {
				return "", errors.New("schedule_recurring requires an active session — start a turn first")
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
			return fmt.Sprintf("SCHEDULED_OK id=%s every %d min. The task will fire in the background and append a reply to this session each cycle. Use list_recurring or cancel_recurring with this id later.", id, minutes), nil
		},
	}
}

func (t *chatTurn) listRecurringToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "list_recurring",
			Description: "List the active RECURRING background tasks for THIS session — id, interval, fire count, prompt. Use before schedule_recurring if the user asks for a recurring task and you want to check whether they already have something similar set up. NOT for sub-agents — use dispatch_to_agent for that.",
			Parameters:  map[string]ToolParam{},
			Caps:        []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			if t.session == nil || t.session.ID == "" {
				return "(no active session)", nil
			}
			updates := ListOrchestrateUpdates(t.session.ID)
			if len(updates) == 0 {
				return "(no recurring tasks for this session)", nil
			}
			// Need a stable JSON shape but include the scheduler task
			// id (which is the cancel key) — re-fetch with the ids.
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
		},
	}
}

func (t *chatTurn) cancelRecurringToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "cancel_recurring",
			Description: "Cancel a recurring task by id (the id returned by schedule_recurring, or visible via list_recurring). Use when the user says \"stop\" / \"cancel\" the recurring task.",
			Parameters: map[string]ToolParam{
				"id": {Type: "string", Description: "Scheduler task id of the recurring task to cancel."},
			},
			Required: []string{"id"},
			Caps:     []Capability{CapWrite},
		},
		Handler: func(args map[string]any) (string, error) {
			if t.session == nil || t.session.ID == "" {
				return "", errors.New("cancel_recurring requires an active session")
			}
			id := strings.TrimSpace(stringArg(args, "id"))
			if id == "" {
				return "", errors.New("id is required")
			}
			if err := CancelOrchestrateUpdate(t.session.ID, id); err != nil {
				return "", err
			}
			return fmt.Sprintf("CANCELLED ok. Recurring task %s removed.", id), nil
		},
	}
}
