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
			Description: "Manage RECURRING background tasks for THIS chat session — a task that re-runs at a fixed interval and appends its reply into this session (the user sees it when they next open the thread). Each fire runs as this agent, with its persona / tools / memory, like a live turn.\n" +
				"WHAT THIS IS: a recurring task on a timer. It is NOT a bridge, NOT a connector, and NOT an event monitor. When you tell the user, call it a \"recurring task\" (or \"scheduled check\") — never a \"bridge\" — and do NOT point them at the Bridges app. Once scheduled it appears in this agent's Schedules rail (beside the chat), where the user can see and cancel it.\n" +
				"Pick the action:\n" +
				"  action=\"schedule\" — set one up. Always: prompt (the directive run each fire, e.g. \"check the build, post if red\" — don't put timing in it). Then pick a pattern:\n" +
				"     pattern=\"fixed\" (default) — fires every interval_minutes (>=1).\n" +
				"     pattern=\"random\" — fires times_per_day at random moments inside a daily window (needs active_from + active_to), each at least min_gap_minutes apart. Use this to make polling feel organic instead of clockwork.\n" +
				"   Optional modifiers (either pattern): active_from/active_to (a daily HH:MM–HH:MM window, local time, outside which fires wait for the next window) and max_fires (auto-stop after this many total fires). Guardrails: min 1 min between fires, max 5 active tasks per session, max 50 fires total before auto-cancel.\n" +
				"  action=\"list\" — show active tasks for this session (id, cadence, fire count, prompt). Call before scheduling to avoid duplicates.\n" +
				"  action=\"cancel\" — stop one. Required: id (from schedule or list).\n" +
				"Use this for periodic polling / checks the agent runs itself. NOT for one-shot work, and NOT for dispatching to other agents.",
			Parameters: map[string]ToolParam{
				"action":           {Type: "string", Enum: []string{"schedule", "list", "cancel"}, Description: "schedule | list | cancel."},
				"prompt":           {Type: "string", Description: "(schedule) The recurring task as a directive the agent follows each fire. Don't include timing — that's the pattern params."},
				"pattern":          {Type: "string", Enum: []string{"fixed", "random"}, Description: "(schedule) fixed = every interval_minutes (default); random = times_per_day random moments inside the active window."},
				"interval_minutes": {Type: "integer", Description: "(schedule, fixed) How often the task fires, in minutes. Minimum 1."},
				"times_per_day":    {Type: "integer", Description: "(schedule, random) How many times to fire within the daily active window. 1–48."},
				"min_gap_minutes":  {Type: "integer", Description: "(schedule, random) Minimum minutes between consecutive fires. Defaults to the deployment minimum (1) if omitted."},
				"active_from":      {Type: "string", Description: "(schedule, optional) Daily window start, 24-hour HH:MM local time (e.g. 09:00). Set together with active_to. Required for random."},
				"active_to":        {Type: "string", Description: "(schedule, optional) Daily window end, 24-hour HH:MM local time (e.g. 17:30). Must be after active_from."},
				"max_fires":        {Type: "integer", Description: "(schedule, optional) Auto-stop after this many total fires. Capped at the deployment ceiling (50)."},
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
	spec := RecurringSpec{
		SessionID: t.session.ID,
		AgentID:   t.agent.ID,
		Username:  t.user,
		Prompt:    strings.TrimSpace(stringArg(args, "prompt")),
		Pattern:   strings.ToLower(strings.TrimSpace(stringArg(args, "pattern"))),
		MaxFires:  intFromArgs(args, "max_fires"),
	}
	if spec.Pattern == "" {
		spec.Pattern = RecurringFixed
	}
	// Optional daily active window (applies to both patterns; required by random).
	from := strings.TrimSpace(stringArg(args, "active_from"))
	to := strings.TrimSpace(stringArg(args, "active_to"))
	if (from == "") != (to == "") {
		return "", errors.New("active_from and active_to must be set together (or both omitted)")
	}
	if from != "" {
		fMin, err := parseHHMM(from)
		if err != nil {
			return "", err
		}
		tMin, err := parseHHMM(to)
		if err != nil {
			return "", err
		}
		spec.HasWindow = true
		spec.WindowFromMin = fMin
		spec.WindowToMin = tMin
	}
	switch spec.Pattern {
	case RecurringRandom:
		spec.TimesPerDay = intFromArgs(args, "times_per_day")
		spec.MinGapSeconds = intFromArgs(args, "min_gap_minutes") * 60
	default:
		spec.IntervalSeconds = intFromArgs(args, "interval_minutes") * 60
	}
	id, err := ScheduleOrchestrateUpdate(spec)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("SCHEDULED_OK id=%s — a recurring TASK now runs %s, appending its reply into this session each cycle. It also appears in this agent's Schedules rail, where the user can cancel it. When you confirm to the user, call it a \"recurring task\" (not a bridge/monitor) and don't send them to the Bridges app. Manage it with recurring(action=\"list\") or recurring(action=\"cancel\", id=%q).", id, specCadence(spec), id), nil
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
		ID        string `json:"id"`
		Prompt    string `json:"prompt"`
		Pattern   string `json:"pattern"`
		Cadence   string `json:"cadence"`
		FireCount int    `json:"fire_count"`
		CreatedAt string `json:"created_at"`
	}
	var rows []row
	for _, task := range ListScheduledTasks(OrchestrateScheduledUpdateKind) {
		var p orchUpdatePayload
		if json.Unmarshal(task.Payload, &p) != nil || p.SessionID != t.session.ID {
			continue
		}
		pattern := p.Pattern
		if pattern == "" {
			pattern = RecurringFixed
		}
		rows = append(rows, row{
			ID: task.ID, Prompt: p.Prompt,
			Pattern: pattern, Cadence: recurringDetail(p),
			FireCount: p.FireCount, CreatedAt: p.CreatedAt,
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
