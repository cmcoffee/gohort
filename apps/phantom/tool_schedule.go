// schedule — the unified phantom scheduling tool. It collapses the old
// schedule_callback (timed messages) and create_watcher (change watchers) into
// one tool with a condition axis:
//
//   condition=always (default) -> a timed callback: fire on a one-shot or
//     recurring schedule and run `prompt` as a message to this conversation.
//     Backed by the unified core trigger engine (core/trigger.go).
//   condition=change           -> a background watcher: alert the owner only
//     when a tool's output (this chat by default) changes, no LLM until it does.
//     Backed by the event-monitor engine so it stays visible/manageable in the
//     owner's Agency console (Stage-2 scope limiter; the engines unify fully in
//     a later stage).
//
// list_schedule / cancel_schedule manage both, plus any in-flight legacy
// schedule_callback tasks still draining on the old path.

package phantom

import (
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func (T *Phantom) scheduleToolDef(chatID, handle string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "schedule",
			Description: "Schedule something for later. condition=\"always\" (default) fires on a timer and runs your prompt as a message to this conversation (reminders, follow-ups, recurring check-ins): set prompt plus a time (run_at or delay_seconds for one-shot, or interval_seconds/cron/random_window to repeat). condition=\"change\" sets up a background watcher that alerts the owner only when something changes (this conversation by default, or a tool via tool_name), with no LLM until it changes. Manage everything with list_schedule and cancel_schedule.",
			Parameters: map[string]ToolParam{
				"condition":        {Type: "string", Enum: []string{"always", "change"}, Description: "\"always\" (default): fire on a timer and run prompt. \"change\": watch and alert only when output changes."},
				"prompt":           {Type: "string", Description: "condition=always: what to say or do when it fires, as an instruction (e.g. \"remind me to call mom\")."},
				"run_at":           {Type: "string", Description: "condition=always: ISO8601 wall-clock time for a one-shot, e.g. 2026-06-12T09:00:00-07:00. Omit if using delay_seconds or a repeat."},
				"delay_seconds":    {Type: "number", Description: "condition=always: seconds from now for a one-shot (e.g. 30). Omit if using run_at or a repeat."},
				"interval_seconds": {Type: "number", Description: "Repeat every N seconds. condition=always: minimum 60. condition=change: how often to check, minimum 30."},
				"cron":             {Type: "string", Description: "condition=always: repeat on a calendar schedule in LOCAL time (the zone get_local_time/time_in_zone report) — use the time the user stated, do NOT convert to UTC. \"{day(s)} HH:MM\", e.g. \"daily 12:00\" (noon local), \"FRI 21:30\", \"weekdays 17:00\", \"APR-3 09:00\" (annual). Omit for one-shot or interval repeats."},
				"random_window":    {Type: "string", Description: "condition=always: repeat once per day at a random time within \"HH:MM-HH:MM\". Omit for fixed schedules."},
				"repeat_until":     {Type: "string", Description: "condition=always: a natural-language condition that stops the repeat when met, evaluated after each fire (e.g. \"until the user replies\", \"after 5 messages\")."},
				"tool_name":        {Type: "string", Description: "condition=change: the owner's tool to watch (its output is hashed each interval). Omit to watch THIS conversation."},
				"tool_args":        {Type: "object", Description: "condition=change: arguments passed to tool_name every check."},
				"notify":           {Type: "string", Description: "condition=change: how to alert (no LLM). One or more of \"direct\" (default — post back into THIS conversation, e.g. a group), \"text\" (text the owner's phone), \"channel\" (wake the channel agent). Comma-separate for multiple, e.g. \"direct,text\"."},
				"format_script":    {Type: "string", Description: "condition=change, optional: sandboxed python to format the alert (e.g. \"X joined\"). On stdin you get {\"prior\":...,\"current\":...,\"prior_status\":...,\"current_status\":...}: prior/current are the tool's body with the \"HTTP <code>\" status line split off so json.loads works directly, and *_status hold that status line if you want to react to errors. Print the alert to stdout. If it errors the built-in diff is used; if it deliberately prints nothing that change is skipped (no alert). Omit to just use the built-in diff."},
				"name":             {Type: "string", Description: "Optional short unique name. Auto-generated when omitted."},
			},
		},
		Handler: func(args map[string]any) (string, error) {
			condition := strings.ToLower(strings.TrimSpace(stringArgPhantom(args, "condition")))
			if condition == "change" {
				// Scope limiter: change-watchers stay on the event-monitor engine
				// so they remain visible and manageable in the owner's Agency
				// console. Only timed callbacks ride the unified trigger engine.
				return T.createWatcherToolDef(chatID, handle).Handler(args)
			}

			owner := phantomToolOwner(T.DB)
			if owner == "" {
				return "", fmt.Errorf("no bridge owner is configured, so I can't schedule this")
			}
			prompt := strings.TrimSpace(stringArgPhantom(args, "prompt"))
			if prompt == "" {
				return "", fmt.Errorf("prompt is required for a timed schedule (condition=always)")
			}

			// First-fire time: delay_seconds or run_at (one-shot seed).
			var runAt time.Time
			if v, ok := args["delay_seconds"]; ok && v != nil {
				var secs float64
				switch n := v.(type) {
				case float64:
					secs = n
				case int:
					secs = float64(n)
				}
				if secs <= 0 {
					return "", fmt.Errorf("delay_seconds must be positive")
				}
				runAt = time.Now().Add(time.Duration(secs) * time.Second)
			} else if s := strings.TrimSpace(stringArgPhantom(args, "run_at")); s != "" {
				t, err := time.Parse(time.RFC3339, s)
				if err != nil {
					return "", fmt.Errorf("run_at must be ISO8601 with timezone: %w", err)
				}
				if !t.After(time.Now().Add(-10 * time.Second)) {
					return "", fmt.Errorf("run_at must be in the future")
				}
				if t.Before(time.Now()) {
					t = time.Now().Add(2 * time.Second)
				}
				runAt = t
			}

			interval := 0
			if v, ok := args["interval_seconds"].(float64); ok {
				interval = int(v)
			}
			const minRepeatSeconds = 60
			if interval > 0 && interval < minRepeatSeconds {
				return "", fmt.Errorf("interval_seconds must be at least %d for a timed schedule", minRepeatSeconds)
			}
			cron := strings.TrimSpace(stringArgPhantom(args, "cron"))
			if cron != "" {
				if _, err := NextCronOccurrence(cron, time.Now()); err != nil {
					return "", fmt.Errorf("invalid cron: %w", err)
				}
			}
			window := strings.TrimSpace(stringArgPhantom(args, "random_window"))
			if window != "" {
				if _, _, _, _, err := ParseWindowBounds(window); err != nil {
					return "", fmt.Errorf("invalid random_window: %w", err)
				}
			}
			if runAt.IsZero() && interval == 0 && cron == "" && window == "" {
				return "", fmt.Errorf("set a time: run_at, delay_seconds, interval_seconds, cron, or random_window")
			}

			name := strings.TrimSpace(stringArgPhantom(args, "name"))
			if name == "" {
				name = "sched-" + NewEventToken()[:6]
			}
			if _, exists := GetScheduledTrigger(RootDB, owner, name); exists {
				return "", fmt.Errorf("a schedule named %q already exists, pick another name", name)
			}
			if _, exists := GetEventMonitor(RootDB, owner, name); exists {
				return "", fmt.Errorf("a watcher named %q already exists, pick another name", name)
			}

			tr := ScheduledTrigger{
				Name:            name,
				Owner:           owner,
				Gate:            GateAlways,
				Action:          ActionCallback,
				Prompt:          prompt,
				RunAt:           runAt,
				IntervalSeconds: interval,
				Cron:            cron,
				RandomWindow:    window,
				RepeatUntil:     strings.TrimSpace(stringArgPhantom(args, "repeat_until")),
				TargetKind:      "phantom_chat",
				TargetID:        chatID,
				TargetMeta:      handle,
				Created:         time.Now(),
			}
			SaveScheduledTrigger(RootDB, tr)
			if err := ScheduleTrigger(RootDB, tr); err != nil {
				DeleteScheduledTrigger(RootDB, owner, name)
				return "", fmt.Errorf("could not schedule: %w", err)
			}

			cur, _ := GetScheduledTrigger(RootDB, owner, name)
			msg := fmt.Sprintf("Scheduled %q for %s.", name, cur.NextRun.Local().Format("Jan 2, 2006 at 3:04 PM MST"))
			switch {
			case window != "":
				msg += fmt.Sprintf(" Repeats at a random time daily within %s.", window)
			case cron != "":
				msg += fmt.Sprintf(" Repeats on schedule: %s.", cron)
			case interval > 0:
				msg += fmt.Sprintf(" Repeats every %s.", formatDuration(interval))
			}
			if tr.RepeatUntil != "" {
				msg += fmt.Sprintf(" Stops when: %s.", tr.RepeatUntil)
			}
			return msg, nil
		},
	}
}

// listScheduleToolDef lists this conversation's timed schedules and the owner's
// watchers (plus any in-flight legacy scheduled tasks still draining).
func (T *Phantom) listScheduleToolDef(chatID string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "list_schedule",
			Description: "List this conversation's scheduled items and the owner's watchers, with the names needed to cancel them.",
			Parameters:  map[string]ToolParam{},
		},
		Handler: func(args map[string]any) (string, error) {
			owner := phantomToolOwner(T.DB)
			var sb strings.Builder
			n := 0
			for _, t := range ListScheduledTriggers(RootDB, owner) {
				if t.TargetID != chatID {
					continue
				}
				n++
				fmt.Fprintf(&sb, "%q: timed message, next %s", t.Name, t.NextRun.Local().Format("Jan 2, 2006 at 3:04 PM MST"))
				switch {
				case t.RandomWindow != "":
					fmt.Fprintf(&sb, ", repeats daily in %s", t.RandomWindow)
				case t.Cron != "":
					fmt.Fprintf(&sb, ", repeats %s", t.Cron)
				case t.IntervalSeconds > 0:
					fmt.Fprintf(&sb, ", repeats every %s", formatDuration(t.IntervalSeconds))
				}
				if t.RepeatUntil != "" {
					fmt.Fprintf(&sb, ", stops when %s", t.RepeatUntil)
				}
				sb.WriteString("\n")
			}
			for _, rec := range T.listTasksForChat(chatID) {
				n++
				fmt.Fprintf(&sb, "%q: %s (legacy)\n", rec.PhantomID, rec.Prompt)
			}
			for _, m := range ListEventMonitors(RootDB, owner) {
				n++
				what := m.Kind
				if m.Kind == EventKindWatch {
					what = "watch " + m.ToolName
					if m.ToolName == "read_phantom_chat" {
						what = "watch a chat"
					}
				}
				state := "active"
				if m.Paused {
					state = "paused"
				}
				fmt.Fprintf(&sb, "%q: %s, every %ds, notify=%s [%s]\n", m.Name, what, m.IntervalSeconds, m.Notify, state)
			}
			if n == 0 {
				return "Nothing scheduled and no watchers set up.", nil
			}
			return strings.TrimSpace(sb.String()), nil
		},
	}
}

// cancelScheduleToolDef cancels a scheduled item or watcher by name, or "all".
func (T *Phantom) cancelScheduleToolDef(chatID string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "cancel_schedule",
			Description: "Cancel a scheduled item or watcher by name (from list_schedule), or \"all\" to cancel everything for this conversation plus all watchers.",
			Parameters: map[string]ToolParam{
				"name": {Type: "string", Description: "The name to cancel (from list_schedule), or \"all\"."},
			},
			Required: []string{"name"},
		},
		Handler: func(args map[string]any) (string, error) {
			owner := phantomToolOwner(T.DB)
			name := strings.TrimSpace(stringArgPhantom(args, "name"))
			if name == "" {
				return "", fmt.Errorf("name is required")
			}
			if strings.ToLower(name) == "all" {
				count := 0
				for _, t := range ListScheduledTriggers(RootDB, owner) {
					if t.TargetID == chatID {
						DeleteScheduledTrigger(RootDB, owner, t.Name)
						count++
					}
				}
				for _, rec := range T.listTasksForChat(chatID) {
					UnscheduleTask(rec.SchedulerID)
					T.untrackTask(rec.PhantomID)
					count++
				}
				for _, m := range ListEventMonitors(RootDB, owner) {
					DeleteEventMonitor(RootDB, owner, m.Name)
					count++
				}
				if count == 0 {
					return "Nothing to cancel.", nil
				}
				return fmt.Sprintf("Cancelled %d item(s).", count), nil
			}
			if _, ok := GetScheduledTrigger(RootDB, owner, name); ok {
				DeleteScheduledTrigger(RootDB, owner, name)
				return fmt.Sprintf("Cancelled schedule %q.", name), nil
			}
			if _, ok := GetEventMonitor(RootDB, owner, name); ok {
				DeleteEventMonitor(RootDB, owner, name)
				return fmt.Sprintf("Cancelled watcher %q.", name), nil
			}
			var rec PhantomTaskRecord
			if T.DB.Get(phantomTasksTable, name, &rec) && rec.ChatID == chatID {
				UnscheduleTask(rec.SchedulerID)
				T.untrackTask(rec.PhantomID)
				return fmt.Sprintf("Cancelled %q.", rec.Prompt), nil
			}
			return "", fmt.Errorf("no schedule or watcher named %q, use list_schedule to see current items", name)
		},
	}
}
