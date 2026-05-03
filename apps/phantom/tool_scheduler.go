package phantom

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const phantomTaskKind = "phantom.callback"

// phantomCallPayload is the stored data for a scheduled phantom message.
type phantomCallPayload struct {
	ChatID             string `json:"chat_id"`
	Handle             string `json:"handle"`
	Prompt             string `json:"prompt"`
	RepeatSeconds      int    `json:"repeat_seconds,omitempty"`       // >0 = reschedule by fixed interval
	RepeatCron         string `json:"repeat_cron,omitempty"`          // e.g. "FRI 21:30" — reschedule to next calendar occurrence
	RepeatRandomWindow string `json:"repeat_random_window,omitempty"` // "HH:MM-HH:MM" — fire at a random time within this daily window
	RepeatUntil        string `json:"repeat_until,omitempty"`         // natural-language stopping condition, evaluated by LLM after each fire
	RepeatCount        int    `json:"repeat_count,omitempty"`         // number of times this task has already fired (incremented each fire)
	PhantomTaskID string `json:"phantom_task_id,omitempty"` // our short ID, links to phantom_tasks record
	IsProactive   bool   `json:"is_proactive,omitempty"`   // set by admin config, never by the LLM tool
}

// rescheduleProactive cancels any existing proactive task for the given
// conversation and schedules a new one. Stores the scheduler task ID so
// it can be cancelled later. This is the single authority for proactive
// task lifecycle — both syncProactiveTasks and fireScheduledCall use it.
func (T *Phantom) rescheduleProactive(chatID, handle string, cfg PhantomConfig) {
	// Cancel whatever is currently scheduled for this conversation.
	var oldID string
	if T.DB.Get(proactiveIDsTable, chatID, &oldID) && oldID != "" {
		UnscheduleTask(oldID)
		T.DB.Unset(proactiveIDsTable, chatID)
	}
	if !cfg.ProactiveEnabled || cfg.ProactiveWindow == "" {
		return
	}
	hours := windowDurationHours(cfg.ProactiveWindow)
	n := proactiveDailyN(T.DB, chatID, cfg.ProactiveMaxPerDay, hours)
	fired := proactiveTodayCount(T.DB, chatID)
	next, err := nextRandomWindowTime(cfg.ProactiveWindow, time.Now(), n, fired)
	if err != nil {
		Log("[phantom/proactive] window error for %s: %v", chatID, err)
		return
	}
	payload := phantomCallPayload{
		ChatID:      chatID,
		Handle:      handle,
		Prompt:      cfg.ProactivePrompt,
		IsProactive: true,
	}
	sid, err := ScheduleTask(phantomTaskKind, payload, next)
	if err != nil {
		Log("[phantom/proactive] schedule error for %s: %v", chatID, err)
		return
	}
	T.DB.Set(proactiveIDsTable, chatID, sid)
	Log("[phantom/proactive] scheduled proactive for %s at %s", chatID, next.Local().Format("Mon Jan 2 3:04 PM"))
}

// syncProactiveTasks is called after proactive config changes. For opted-in
// conversations it cancel-and-replaces the existing task; for opted-out or
// globally disabled conversations it cancels any lingering task.
func (T *Phantom) syncProactiveTasks(cfg PhantomConfig) {
	// Build set of tracked scheduler IDs so we can identify orphans.
	tracked := make(map[string]bool)
	for _, k := range T.DB.Keys(proactiveIDsTable) {
		var sid string
		if T.DB.Get(proactiveIDsTable, k, &sid) && sid != "" {
			tracked[sid] = true
		}
	}

	// Cancel orphaned proactive tasks left over from the old versioning system
	// or from races. Any task with IsProactive=true that isn't in proactiveIDsTable
	// is an orphan.
	for _, task := range ListScheduledTasks(phantomTaskKind) {
		if tracked[task.ID] {
			continue
		}
		var p phantomCallPayload
		if err := json.Unmarshal(task.Payload, &p); err != nil || !p.IsProactive {
			continue
		}
		UnscheduleTask(task.ID)
		Log("[phantom/proactive] cleaned up orphaned task %s for %s", task.ID, p.ChatID)
	}

	for _, k := range T.DB.Keys(conversationTable) {
		var conv Conversation
		if !T.DB.Get(conversationTable, k, &conv) {
			continue
		}
		if cfg.ProactiveEnabled && conv.ProactiveEnabled {
			T.rescheduleProactive(conv.ChatID, conv.Handle, cfg)
		} else {
			var oldID string
			if T.DB.Get(proactiveIDsTable, conv.ChatID, &oldID) && oldID != "" {
				UnscheduleTask(oldID)
				T.DB.Unset(proactiveIDsTable, conv.ChatID)
			}
		}
	}
}

// trackTask stores a record in phantom_tasks so the LLM can list and cancel it.
// Only called for LLM-created tasks (not proactive ones).
func (T *Phantom) trackTask(schedulerID string, p phantomCallPayload, runAt time.Time) {
	if T.DB == nil || p.PhantomTaskID == "" {
		return
	}
	var repeat string
	switch {
	case p.RepeatRandomWindow != "":
		repeat = "random window " + p.RepeatRandomWindow + " daily"
	case p.RepeatCron != "":
		repeat = "cron: " + p.RepeatCron
	case p.RepeatSeconds > 0:
		repeat = "every " + formatDuration(p.RepeatSeconds)
	}
	T.DB.Set(phantomTasksTable, p.PhantomTaskID, PhantomTaskRecord{
		PhantomID:   p.PhantomTaskID,
		SchedulerID: schedulerID,
		ChatID:      p.ChatID,
		Prompt:      p.Prompt,
		RunAt:       runAt.UTC().Format(time.RFC3339),
		Repeat:      repeat,
		Until:       p.RepeatUntil,
	})
}

// untrackTask removes a phantom_tasks record by our short ID.
func (T *Phantom) untrackTask(phantomID string) {
	if T.DB != nil && phantomID != "" {
		T.DB.Unset(phantomTasksTable, phantomID)
	}
}

// listTasksForChat returns all non-proactive tracked tasks for a conversation.
func (T *Phantom) listTasksForChat(chatID string) []PhantomTaskRecord {
	if T.DB == nil {
		return nil
	}
	var out []PhantomTaskRecord
	for _, k := range T.DB.Keys(phantomTasksTable) {
		var rec PhantomTaskRecord
		if T.DB.Get(phantomTasksTable, k, &rec) && rec.ChatID == chatID {
			out = append(out, rec)
		}
	}
	return out
}

// registerSchedulerHandler wires the phantom handler into the global scheduler.
// Called from RegisterRoutes once T.DB and T.LLM are set.
func (T *Phantom) registerSchedulerHandler() {
	RegisterScheduleHandler(phantomTaskKind, func(ctx context.Context, raw json.RawMessage) {
		var p phantomCallPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			Log("[phantom/scheduler] bad payload: %v", err)
			return
		}
		T.fireScheduledCall(ctx, p)
	})

	// Migrate any tasks stored in the old per-app DB table into the global scheduler.
	migratePhantomScheduled(T.DB)

	// Register a reconciler that ensures all opted-in conversations have a
	// scheduled proactive task. If a task was lost (e.g. handler crashed after
	// removing it but before re-scheduling), this recreates it.
	SetPostSchedulerStart(func() {
		RegisterReconciler("phantom/proactive", func(ctx context.Context) error {
			sdb := GetSchedDB()
			if sdb == nil {
				return nil
			}
			_ = sdb // used implicitly via ListScheduledTasks
			cfg := defaultConfig(T.DB)
			if !cfg.ProactiveEnabled || cfg.ProactiveWindow == "" {
				return nil
			}
			for _, k := range T.DB.Keys(conversationTable) {
				var conv Conversation
				if !T.DB.Get(conversationTable, k, &conv) || !conv.ProactiveEnabled || conv.ChatID == "" {
					continue
				}
				// Check if a task already exists in the scheduler for this conversation.
				hasTask := false
				for _, st := range ListScheduledTasks(phantomTaskKind) {
					var p phantomCallPayload
					if json.Unmarshal(st.Payload, &p) == nil && p.ChatID == conv.ChatID && p.IsProactive {
						hasTask = true
						break
					}
				}
				if !hasTask {
					Log("[phantom/reconciler] rescheduling missing proactive task for %s", conv.ChatID)
					T.rescheduleProactive(conv.ChatID, conv.Handle, cfg)
				}
			}
			return nil
		})
	})
}

// migratePhantomScheduled moves ScheduledCall records from the old relay_scheduled
// table (stored in phantom's own DB) into the global scheduler. No-op once drained.
func migratePhantomScheduled(db Database) {
	if db == nil {
		return
	}
	const oldTable = "relay_scheduled"
	keys := db.Keys(oldTable)
	if len(keys) == 0 {
		return
	}
	Log("[phantom] migrating %d scheduled task(s) to global scheduler", len(keys))
	for _, k := range keys {
		var old struct {
			ChatID string `json:"chat_id"`
			Handle string `json:"handle"`
			Prompt string `json:"prompt"`
			RunAt  string `json:"run_at"`
		}
		if !db.Get(oldTable, k, &old) {
			continue
		}
		runAt, err := time.Parse(time.RFC3339, old.RunAt)
		if err != nil || !runAt.After(time.Now()) {
			db.Unset(oldTable, k)
			continue
		}
		if _, err := ScheduleTask(phantomTaskKind, phantomCallPayload{
			ChatID: old.ChatID,
			Handle: old.Handle,
			Prompt: old.Prompt,
		}, runAt); err != nil {
			Log("[phantom] migration schedule error: %v", err)
			continue
		}
		db.Unset(oldTable, k)
	}
}

// formatDuration returns a human-readable description of a seconds interval.
func formatDuration(secs int) string {
	switch {
	case secs%86400 == 0:
		d := secs / 86400
		if d == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", d)
	case secs%3600 == 0:
		h := secs / 3600
		if h == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", h)
	case secs%60 == 0:
		m := secs / 60
		if m == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", m)
	default:
		return fmt.Sprintf("%d seconds", secs)
	}
}

var weekdayNames = map[string]time.Weekday{
	"sun": time.Sunday, "sunday": time.Sunday,
	"mon": time.Monday, "monday": time.Monday,
	"tue": time.Tuesday, "tuesday": time.Tuesday,
	"wed": time.Wednesday, "wednesday": time.Wednesday,
	"thu": time.Thursday, "thursday": time.Thursday,
	"fri": time.Friday, "friday": time.Friday,
	"sat": time.Saturday, "saturday": time.Saturday,
}

var monthNames = map[string]time.Month{
	"jan": time.January, "january": time.January,
	"feb": time.February, "february": time.February,
	"mar": time.March, "march": time.March,
	"apr": time.April, "april": time.April,
	"may": time.May,
	"jun": time.June, "june": time.June,
	"jul": time.July, "july": time.July,
	"aug": time.August, "august": time.August,
	"sep": time.September, "september": time.September,
	"oct": time.October, "october": time.October,
	"nov": time.November, "november": time.November,
	"dec": time.December, "december": time.December,
}

// nextCronOccurrence returns the next time after `from` that matches the cron spec.
// Spec format: "{days} {HH:MM}" where days is one of:
//   - a weekday name or comma-separated list: "FRI", "MON,WED,FRI"
//   - "daily" / "everyday" — every day
//   - "weekdays"           — Monday–Friday
//   - "weekends"           — Saturday–Sunday
//   - "MON-DD" month-day   — annual date, e.g. "APR-3", "DEC-25"
func nextCronOccurrence(spec string, from time.Time) (time.Time, error) {
	parts := strings.Fields(strings.TrimSpace(spec))
	if len(parts) != 2 {
		return time.Time{}, fmt.Errorf("invalid cron spec %q: expected 'DAY(S) HH:MM'", spec)
	}
	t, err := time.Parse("15:04", parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid time %q in cron spec: use HH:MM (24-hour)", parts[1])
	}
	hour, min := t.Hour(), t.Minute()

	// Month-day pattern: "APR-3", "DEC-25", etc. — annual repeat.
	if idx := strings.Index(parts[0], "-"); idx > 0 {
		monthStr := strings.ToLower(parts[0][:idx])
		month, ok := monthNames[monthStr]
		if !ok {
			return time.Time{}, fmt.Errorf("unknown month %q in cron spec", parts[0][:idx])
		}
		var day int
		if _, err := fmt.Sscanf(parts[0][idx+1:], "%d", &day); err != nil || day < 1 || day > 31 {
			return time.Time{}, fmt.Errorf("invalid day %q in cron spec", parts[0][idx+1:])
		}
		// Try this year, then next year.
		for _, year := range []int{from.Year(), from.Year() + 1} {
			c := time.Date(year, month, day, hour, min, 0, 0, from.Location())
			if c.Month() != month {
				continue // day overflowed (e.g. Feb-30)
			}
			if c.After(from) {
				return c, nil
			}
		}
		return time.Time{}, fmt.Errorf("no occurrence found for cron spec %q", spec)
	}

	daySet := make(map[time.Weekday]bool)
	switch strings.ToLower(parts[0]) {
	case "daily", "everyday":
		for d := time.Sunday; d <= time.Saturday; d++ {
			daySet[d] = true
		}
	case "weekdays":
		for _, d := range []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday} {
			daySet[d] = true
		}
	case "weekends":
		daySet[time.Saturday] = true
		daySet[time.Sunday] = true
	default:
		for _, token := range strings.Split(parts[0], ",") {
			wd, ok := weekdayNames[strings.ToLower(strings.TrimSpace(token))]
			if !ok {
				return time.Time{}, fmt.Errorf("unknown day %q in cron spec", token)
			}
			daySet[wd] = true
		}
	}

	// Search up to 8 days ahead for the next matching slot.
	base := time.Date(from.Year(), from.Month(), from.Day(), hour, min, 0, 0, from.Location())
	for i := 0; i <= 7; i++ {
		c := base.AddDate(0, 0, i)
		if daySet[c.Weekday()] && c.After(from) {
			return c, nil
		}
	}
	return time.Time{}, fmt.Errorf("no occurrence found for cron spec %q", spec)
}

// schedulerToolDef returns an AgentToolDef that lets the LLM schedule a future callback.
func (T *Phantom) schedulerToolDef(chatID, handle string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "schedule_callback",
			Description: "Schedule a future message to be sent to this conversation at the specified time. " +
				"Use for reminders, follow-ups, or any deferred action the user requests. " +
				"For short relative delays (e.g. 'in 30 seconds', 'in 5 minutes') prefer delay_seconds. " +
				"For wall-clock times (e.g. 'tomorrow at 9am') use run_at.",
			Parameters: map[string]ToolParam{
				"run_at": {
					Type:        "string",
					Description: "When to run, as ISO8601 with timezone (e.g. 2026-04-27T09:00:00-07:00). Use for specific wall-clock times. Omit if using delay_seconds.",
				},
				"delay_seconds": {
					Type:        "number",
					Description: "Seconds from now to run. Use for relative delays like '30 seconds' or '5 minutes'. Omit if using run_at.",
				},
				"repeat_seconds": {
					Type:        "number",
					Description: "If set, repeat the callback every N seconds after each fire. Minimum 60. E.g. 3600 = every hour. Omit for one-shot or cron-style repeats.",
				},
				"repeat_cron": {
					Type:        "string",
					Description: `Repeat on a calendar schedule: "{day(s)} {HH:MM}". Days: a weekday name or comma-separated list (MON, TUE, WED, THU, FRI, SAT, SUN), "daily", "weekdays", "weekends", or an annual month-day like "APR-3" or "DEC-25". Time is 24-hour HH:MM. Examples: "FRI 21:30", "MON,WED,FRI 09:00", "daily 08:00", "weekdays 17:00", "APR-3 09:00" (every April 3rd). Use the annual format for birthdays and yearly events. Omit for one-shot or fixed-interval repeats.`,
				},
				"repeat_random_window": {
					Type:        "string",
					Description: "Send at a random time within a daily time window, format 'HH:MM-HH:MM' (24-hour). Each fire picks a new unpredictable time within the window. Makes the persona feel human rather than robotic. E.g. '09:00-22:00' fires once per day at a random time between 9am and 10pm. Combine with repeat_until to stop eventually.",
				},
				"repeat_until": {
					Type:        "string",
					Description: "A natural-language condition that stops the repeat when met. Evaluated by AI after each fire using the current time, fire count, and recent conversation. Examples: \"after 5 messages\", \"until the user replies\", \"for the next 10 minutes\", \"until tomorrow morning\". Use with repeat_seconds, repeat_cron, or repeat_random_window. Omit for indefinite repeating.",
				},
				"prompt": {
					Type:        "string",
					Description: "Instructions for what the AI should say or do at the scheduled time.",
				},
			},
			Required: []string{"prompt"},
		},
		Handler: func(args map[string]any) (string, error) {
			prompt, _ := args["prompt"].(string)
			if prompt == "" {
				return "", fmt.Errorf("prompt is required")
			}

			var runAt time.Time
			if delaySecs, ok := args["delay_seconds"]; ok && delaySecs != nil {
				var secs float64
				switch v := delaySecs.(type) {
				case float64:
					secs = v
				case int:
					secs = float64(v)
				}
				if secs <= 0 {
					return "", fmt.Errorf("delay_seconds must be positive")
				}
				runAt = time.Now().Add(time.Duration(secs) * time.Second)
			} else {
				runAtStr, _ := args["run_at"].(string)
				if runAtStr == "" {
					return "", fmt.Errorf("either run_at or delay_seconds is required")
				}
				var err error
				runAt, err = time.Parse(time.RFC3339, runAtStr)
				if err != nil {
					return "", fmt.Errorf("run_at must be ISO8601 with timezone: %w", err)
				}
				// Allow a 10s grace window for clock skew / LLM processing time.
				if !runAt.After(time.Now().Add(-10 * time.Second)) {
					return "", fmt.Errorf("run_at must be in the future")
				}
				if runAt.Before(time.Now()) {
					runAt = time.Now().Add(2 * time.Second)
				}
			}

			const minRepeatSeconds = 60
			var repeatSecs int
			if v, ok := args["repeat_seconds"]; ok && v != nil {
				switch n := v.(type) {
				case float64:
					repeatSecs = int(n)
				case int:
					repeatSecs = n
				}
				if repeatSecs > 0 && repeatSecs < minRepeatSeconds {
					return "", fmt.Errorf("repeat_seconds must be at least %d", minRepeatSeconds)
				}
			}
			repeatCron, _ := args["repeat_cron"].(string)
			repeatCron = strings.TrimSpace(repeatCron)
			// Validate cron spec eagerly so errors surface at schedule time.
			if repeatCron != "" {
				if _, err := nextCronOccurrence(repeatCron, time.Now()); err != nil {
					return "", fmt.Errorf("invalid repeat_cron: %w", err)
				}
			}
			repeatUntil, _ := args["repeat_until"].(string)
			repeatUntil = strings.TrimSpace(repeatUntil)
			repeatRandomWindow, _ := args["repeat_random_window"].(string)
			repeatRandomWindow = strings.TrimSpace(repeatRandomWindow)
			if repeatRandomWindow != "" {
				if _, _, _, _, werr := parseWindowBounds(repeatRandomWindow); werr != nil {
					return "", fmt.Errorf("invalid repeat_random_window: %w", werr)
				}
			}

			phantomTaskID := newID()
			payload := phantomCallPayload{
				ChatID:             chatID,
				Handle:             handle,
				Prompt:             prompt,
				RepeatSeconds:      repeatSecs,
				RepeatCron:         repeatCron,
				RepeatRandomWindow: repeatRandomWindow,
				RepeatUntil:        repeatUntil,
				PhantomTaskID:      phantomTaskID,
			}
			sid, err := ScheduleTask(phantomTaskKind, payload, runAt)
			if err != nil {
				return "", err
			}
			T.trackTask(sid, payload, runAt)
			msg := fmt.Sprintf("Scheduled for %s.", runAt.Local().Format("Jan 2, 2006 at 3:04 PM MST"))
			switch {
			case repeatRandomWindow != "":
				msg += fmt.Sprintf(" Repeats at random times within %s daily.", repeatRandomWindow)
			case repeatCron != "":
				msg += fmt.Sprintf(" Repeats on schedule: %s.", repeatCron)
			case repeatSecs > 0:
				msg += fmt.Sprintf(" Repeats every %s.", formatDuration(repeatSecs))
			}
			if repeatUntil != "" {
				msg += fmt.Sprintf(" Stops when: %s.", repeatUntil)
			}
			return msg, nil
		},
	}
}

// parseWindowBounds parses a "HH:MM-HH:MM" window spec into (startHour, startMin, endHour, endMin).
func parseWindowBounds(spec string) (sh, sm, eh, em int, err error) {
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return 0, 0, 0, 0, fmt.Errorf("invalid window %q: expected HH:MM-HH:MM", spec)
	}
	var st, et time.Time
	if st, err = time.Parse("15:04", strings.TrimSpace(parts[0])); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("invalid start time in window %q: use HH:MM", spec)
	}
	if et, err = time.Parse("15:04", strings.TrimSpace(parts[1])); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("invalid end time in window %q: use HH:MM", spec)
	}
	return st.Hour(), st.Minute(), et.Hour(), et.Minute(), nil
}

// nextRandomWindowTime returns the next fire time for a HH:MM-HH:MM daily
// window using slot-based distribution: the window is split into N equal
// slots and the next fire is placed at a random instant inside the slot
// matching firedSoFar. If that slot is already in the past (mid-day enable,
// missed fire, server restart), it advances to the next valid slot. When N
// slots are exhausted today, it rolls over to slot 0 of tomorrow's window.
//
// minGap is a safety margin to prevent back-to-back fires when slots are
// short — the chosen instant is shifted into the next slot if it would land
// inside (now + minGap).
//
// N must be ≥ 1; firedSoFar should be the number of proactive fires already
// completed in today's window.
func nextRandomWindowTime(spec string, from time.Time, n, firedSoFar int) (time.Time, error) {
	sh, sm, eh, em, err := parseWindowBounds(spec)
	if err != nil {
		return time.Time{}, err
	}
	if n < 1 {
		n = 1
	}

	const minGap = 20 * time.Minute

	loc := from.Location()
	windowStart := func(day time.Time) time.Time {
		return time.Date(day.Year(), day.Month(), day.Day(), sh, sm, 0, 0, loc)
	}
	windowEnd := func(day time.Time) time.Time {
		return time.Date(day.Year(), day.Month(), day.Day(), eh, em, 0, 0, loc)
	}

	earliest := from.Add(minGap)

	// pickInSlot returns a random time within slot[i] of [wStart, wEnd], shifting
	// past `earliest` if needed. Returns (time, true) if a valid time was found,
	// (zero, false) if even the slot's tail is before earliest (slot is in past).
	pickInSlot := func(wStart, wEnd time.Time, i int) (time.Time, bool) {
		span := wEnd.Sub(wStart)
		slotWidth := span / time.Duration(n)
		slotStart := wStart.Add(slotWidth * time.Duration(i))
		slotEnd := wStart.Add(slotWidth * time.Duration(i+1))
		// Bound the effective window of this slot by the earliest-allowed time.
		if slotEnd.Before(earliest) {
			return time.Time{}, false
		}
		effective := slotStart
		if earliest.After(effective) {
			effective = earliest
		}
		if !effective.Before(slotEnd) {
			return time.Time{}, false
		}
		jitterRange := slotEnd.Sub(effective)
		jitter := time.Duration(rand.Int63n(int64(jitterRange) + 1))
		if jitter >= jitterRange {
			jitter = jitterRange - 1
		}
		return effective.Add(jitter), true
	}

	for _, day := range []time.Time{from, from.AddDate(0, 0, 1)} {
		wStart := windowStart(day)
		wEnd := windowEnd(day)
		if !wEnd.After(wStart) {
			continue
		}
		startSlot := firedSoFar
		if !day.Equal(from) || day.After(from) && !sameYMD(day, from) {
			// Tomorrow rolls over to slot 0 — yesterday's fires don't count.
			startSlot = 0
		}
		if startSlot >= n {
			// Today's slots are all spoken for; let the loop fall through to tomorrow.
			continue
		}
		for i := startSlot; i < n; i++ {
			if t, ok := pickInSlot(wStart, wEnd, i); ok {
				return t, nil
			}
		}
	}

	// Fallback: slot 0 of the window two days out.
	day2 := from.AddDate(0, 0, 2)
	wStart := windowStart(day2)
	wEnd := windowEnd(day2)
	if !wEnd.After(wStart) {
		return time.Time{}, fmt.Errorf("window %q has zero or negative duration", spec)
	}
	if t, ok := pickInSlot(wStart, wEnd, 0); ok {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("could not find a slot for window %q", spec)
}

// sameYMD reports whether two times fall on the same calendar day in their
// respective locations.
func sameYMD(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// windowDurationHours returns the duration of an HH:MM-HH:MM window in hours.
// Returns 0 if the spec is malformed or the window has zero/negative span.
func windowDurationHours(spec string) float64 {
	sh, sm, eh, em, err := parseWindowBounds(spec)
	if err != nil {
		return 0
	}
	mins := (eh*60 + em) - (sh*60 + sm)
	if mins <= 0 {
		return 0
	}
	return float64(mins) / 60.0
}

// repeatConditionMet asks the LLM whether the repeat_until condition has been satisfied.
// Returns true if the condition is met (stop repeating) or if the LLM call fails
// after the condition string looks time-based and is clearly in the past.
func (T *Phantom) repeatConditionMet(ctx context.Context, p phantomCallPayload, recentMessages []Message) bool {
	if p.RepeatUntil == "" {
		return false
	}
	sysPrompt := fmt.Sprintf(
		"Current date and time: %s\n\n"+
			"A repeating scheduled message has the following stopping condition:\n\"%s\"\n\n"+
			"This message has fired %d time(s) so far.\n\n"+
			"The recent conversation history is provided below. "+
			"Based on the condition, the current time, and the conversation history, "+
			"has the stopping condition been met?\n\n"+
			"Reply with exactly one word: YES or NO.",
		time.Now().Format("Monday, January 2, 2006 3:04:05 PM MST"),
		p.RepeatUntil,
		p.RepeatCount,
	)
	msgs := make([]Message, 0, len(recentMessages)+1)
	msgs = append(msgs, recentMessages...)
	msgs = append(msgs, Message{Role: "user", Content: "Has the stopping condition been met? Answer YES or NO."})

	condCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := T.LLM.Chat(condCtx, msgs, WithSystemPrompt(sysPrompt), WithThink(false))
	if err != nil || resp == nil {
		Log("[phantom/scheduler] condition check failed for %s: %v — continuing repeat", p.Handle, err)
		return false
	}
	answer := strings.ToUpper(strings.TrimSpace(resp.Content))
	met := strings.HasPrefix(answer, "YES")
	if met {
		Log("[phantom/scheduler] repeat_until condition met for %s: %q", p.Handle, p.RepeatUntil)
	}
	return met
}

// fireScheduledCall runs the LLM with the scheduled prompt and enqueues the reply.
func (T *Phantom) fireScheduledCall(ctx context.Context, p phantomCallPayload) {
	cfg := defaultConfig(T.DB)

	if p.IsProactive && !cfg.ProactiveEnabled {
		Log("[phantom/proactive] proactive disabled — dropping task for %s", p.ChatID)
		return
	}
	// Dedup: if a proactive message was sent very recently (e.g. orphaned tasks
	// from the old versioning system firing in parallel), skip silently.
	if p.IsProactive {
		if last := proactiveLastFire(T.DB, p.ChatID); !last.IsZero() && time.Since(last) < 5*time.Minute {
			Log("[phantom/proactive] duplicate fire for %s — last fire %s ago, dropping", p.ChatID, time.Since(last).Round(time.Second))
			return
		}
	}

	var conv Conversation
	T.DB.Get(conversationTable, p.ChatID, &conv)

	// Remove the tracking record now that this task is firing.
	// The reschedule path will create a new record with the fresh task ID.
	if !p.IsProactive {
		T.untrackTask(p.PhantomTaskID)
	}

	if p.IsProactive && !conv.ProactiveEnabled {
		Log("[phantom/proactive] conversation %s not opted in — dropping", p.ChatID)
		return
	}
	if p.IsProactive && cfg.ProactiveMaxPerDay > 0 {
		if count := proactiveTodayCount(T.DB, p.ChatID); count >= cfg.ProactiveMaxPerDay {
			Log("[phantom/proactive] daily cap of %d reached for %s — rescheduling for next window", cfg.ProactiveMaxPerDay, p.ChatID)
			T.rescheduleProactive(p.ChatID, p.Handle, cfg)
			return
		}
	}
	// For proactive tasks, use the current config prompt (may have been updated).
	if p.IsProactive && cfg.ProactivePrompt != "" {
		p.Prompt = cfg.ProactivePrompt
		p.RepeatRandomWindow = cfg.ProactiveWindow
	}

	personaName := cfg.PersonaName
	if conv.PersonaName != "" {
		personaName = conv.PersonaName
	}
	personality := cfg.Personality
	if conv.Personality != "" {
		personality = conv.Personality
	}
	convRules := cfg.SystemPrompt
	if conv.SystemPrompt != "" {
		convRules = conv.SystemPrompt
	}
	basePrompt := buildSystemPrompt(personality, convRules)

	var senderDesc string
	if conv.DisplayName != "" {
		senderDesc = fmt.Sprintf("%s (%s)", conv.DisplayName, p.Handle)
	} else {
		senderDesc = p.Handle
	}

	// Build recent history so the LLM can judge whether the timing is right.
	history := recentMessages(T.DB, p.ChatID, 10)
	var msgs []Message
	for _, m := range history {
		role := "user"
		content := m.Text
		if m.Role == "assistant" {
			role = "assistant"
		} else {
			label := p.Handle
			if conv.DisplayName != "" {
				label = conv.DisplayName
			}
			content = label + ": " + m.Text
		}
		msgs = append(msgs, Message{Role: role, Content: content})
	}
	msgs = append(msgs, Message{Role: "user", Content: "Compose your scheduled message now, or call stay_silent if the timing is not right."})

	// The scheduled prompt is an instruction for the LLM — injected into the
	// system prompt so the LLM composes an outbound message naturally rather
	// than responding to a fake user turn.
	sysPrompt := fmt.Sprintf(
		"Current date and time: %s\n\nYour name is %s. The person you are messaging is %s.\n\n%s%s\n\n"+
			"## Scheduled Message Instruction\n%s\n\n"+
			"Compose a natural outbound message to the user following the instruction above. "+
			"Do not mention that this is scheduled or automated. "+
			"When you know someone's name, use it naturally. "+
			"Keep responses varied — avoid repetitive patterns. "+
			"If the conversation history or language rules suggest the timing is not right, call stay_silent instead of sending. "+
			"IMPORTANT: Reply in plain text only. No markdown. This is a text message conversation.",
		time.Now().Format("Monday, January 2, 2006 3:04 PM MST"), personaName, senderDesc, basePrompt, memoryBlock(T.DB, p.ChatID), p.Prompt,
	)

	sess := &ToolSession{LLM: T.LLM}
	tools := T.buildConvTools(p.ChatID, p.Handle, conv, cfg, sess, false)

	phantomChatOpts := buildThinkOpts("app.phantom")
	resp, _, err := T.RunAgentLoop(ctx, msgs, AgentLoopConfig{
		SystemPrompt: sysPrompt,
		Tools:        tools,
		MaxRounds:    6,
		RouteKey:     "app.phantom",
		PromptTools:  T.PromptTools,
		ChatOptions:  phantomChatOpts,
		Confirm:      func(string, string) bool { return true },
		// Drain any view-images deposited by tools so the LLM sees the
		// frames on its next round (LLM-only, not delivered to user).
		OnRoundStart: func() []Message {
			imgs := sess.DrainViewImages()
			if len(imgs) == 0 {
				return nil
			}
			return []Message{{
				Role:    "user",
				Content: "Here are the sampled frames for visual analysis:",
				Images:  imgs,
			}}
		},
	})
	if err != nil {
		Log("[phantom/scheduler] LLM error for %s: %v", p.Handle, err)
		return
	}

	if sess.Silenced {
		Log("[phantom/scheduler] stay_silent called for %s — no scheduled reply sent", p.Handle)
		return
	}

	var replyText string
	if resp != nil {
		replyText = strings.TrimSpace(stripEmojis(resp.Content))
	}
	sessionImages := filterNewImages(sess.Images)
	sessionVideos := filterNewVideos(sess.Videos)
	if replyText == "" && len(sessionImages) == 0 && len(sessionVideos) == 0 {
		if len(sess.Images) > 0 || len(sess.Videos) > 0 {
			Log("[phantom/scheduler] all attachments already sent to %s, nothing new to queue", p.Handle)
		} else {
			// Capture diagnostic context for empty LLM responses.
			if resp == nil {
				Log("[phantom/scheduler] empty LLM response for %s — nil response from RunAgentLoop", p.Handle)
			} else {
				Log("[phantom/scheduler] empty LLM response for %s — content=%d chars, reasoning=%d chars, tool_calls=%d, stop_reason=%q",
					p.Handle, len(resp.Content), len(resp.Reasoning), len(resp.ToolCalls))
			}
		}
		return
	}

	enqueueOutbox(T.DB, OutboxItem{
		ID:      newID(),
		ChatID:  p.ChatID,
		Handle:  p.Handle,
		Text:    replyText,
		Images:  sessionImages,
		Videos:  sessionVideos,
		Type:    "scheduled",
		Created: now(),
	})
	if p.IsProactive {
		incrementProactiveCount(T.DB, p.ChatID)
	}
	Log("[phantom/scheduler] queued reply for %s: %q", p.Handle, replyText)

	p.RepeatCount++

	if p.RepeatUntil != "" {
		// Build recent conversation history for the condition evaluator.
		var recentMsgs []Message
		for _, m := range recentMessages(T.DB, p.ChatID, 10) {
			recentMsgs = append(recentMsgs, Message{Role: m.Role, Content: m.Text})
		}
		if T.repeatConditionMet(ctx, p, recentMsgs) {
			Log("[phantom/scheduler] stopping repeat for %s after %d fire(s)", p.Handle, p.RepeatCount)
			return
		}
	}

	// Proactive tasks use rescheduleProactive (cancel-and-replace by ID).
	if p.IsProactive {
		T.rescheduleProactive(p.ChatID, p.Handle, cfg)
		return
	}

	switch {
	case p.RepeatRandomWindow != "":
		// User-scheduled random-window repeats use a single slot covering
		// the full window, preserving the legacy "fire once at a random time
		// in this daily window" behavior. The proactive scheduler is the
		// only path that uses multi-slot distribution.
		next, err := nextRandomWindowTime(p.RepeatRandomWindow, time.Now(), 1, 0)
		if err != nil {
			Log("[phantom/scheduler] random window reschedule error for %s: %v", p.Handle, err)
		} else {
			p.PhantomTaskID = newID()
			if sid, err := ScheduleTask(phantomTaskKind, p, next); err != nil {
				Log("[phantom/scheduler] random window reschedule error for %s: %v", p.Handle, err)
			} else {
				T.trackTask(sid, p, next)
				Log("[phantom/scheduler] rescheduled random window for %s at %s", p.Handle, next.Local().Format("Mon Jan 2 3:04 PM"))
			}
		}
	case p.RepeatCron != "":
		next, err := nextCronOccurrence(p.RepeatCron, time.Now())
		if err != nil {
			Log("[phantom/scheduler] cron reschedule error for %s: %v", p.Handle, err)
		} else {
			p.PhantomTaskID = newID()
			if sid, err := ScheduleTask(phantomTaskKind, p, next); err != nil {
				Log("[phantom/scheduler] cron reschedule error for %s: %v", p.Handle, err)
			} else {
				T.trackTask(sid, p, next)
				Log("[phantom/scheduler] rescheduled cron %q for %s at %s", p.RepeatCron, p.Handle, next.Local().Format("Mon Jan 2 3:04 PM"))
			}
		}
	case p.RepeatSeconds > 0:
		next := time.Now().Add(time.Duration(p.RepeatSeconds) * time.Second)
		p.PhantomTaskID = newID()
		if sid, err := ScheduleTask(phantomTaskKind, p, next); err != nil {
			Log("[phantom/scheduler] repeat reschedule error for %s: %v", p.Handle, err)
		} else {
			T.trackTask(sid, p, next)
			Log("[phantom/scheduler] rescheduled repeat for %s in %s", p.Handle, formatDuration(p.RepeatSeconds))
		}
	}
}

// listScheduledToolDef returns a tool that lists pending scheduled tasks for this conversation.
func (T *Phantom) listScheduledToolDef(chatID string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "list_scheduled",
			Description: "List all pending scheduled messages for this conversation. Returns task IDs needed to cancel them.",
			Parameters:  map[string]ToolParam{},
		},
		Handler: func(args map[string]any) (string, error) {
			tasks := T.listTasksForChat(chatID)
			if len(tasks) == 0 {
				return "No pending scheduled tasks for this conversation.", nil
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "%d pending scheduled task(s):\n\n", len(tasks))
			for _, rec := range tasks {
				runAt, _ := time.Parse(time.RFC3339, rec.RunAt)
				var runAtStr string
				if !runAt.IsZero() {
					runAtStr = runAt.Local().Format("Jan 2, 2006 at 3:04 PM MST")
				} else {
					runAtStr = rec.RunAt
				}
				fmt.Fprintf(&sb, "[%s] %q\n  Next run: %s\n", rec.PhantomID, rec.Prompt, runAtStr)
				if rec.Repeat != "" {
					fmt.Fprintf(&sb, "  Repeats: %s\n", rec.Repeat)
				}
				if rec.Until != "" {
					fmt.Fprintf(&sb, "  Stops when: %s\n", rec.Until)
				}
				sb.WriteString("\n")
			}
			return strings.TrimSpace(sb.String()), nil
		},
	}
}

// cancelScheduledToolDef returns a tool that cancels a pending scheduled task by ID.
func (T *Phantom) cancelScheduledToolDef(chatID string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "cancel_scheduled",
			Description: "Cancel a pending scheduled message. Use list_scheduled first to get the task ID. " +
				"Use \"all\" as the task_id to cancel every pending task for this conversation.",
			Parameters: map[string]ToolParam{
				"task_id": {
					Type:        "string",
					Description: "The task ID to cancel (from list_scheduled), or \"all\" to cancel all.",
				},
			},
			Required: []string{"task_id"},
		},
		Handler: func(args map[string]any) (string, error) {
			taskID, _ := args["task_id"].(string)
			taskID = strings.TrimSpace(taskID)
			if taskID == "" {
				return "", fmt.Errorf("task_id is required")
			}
			if strings.ToLower(taskID) == "all" {
				tasks := T.listTasksForChat(chatID)
				if len(tasks) == 0 {
					return "No pending scheduled tasks to cancel.", nil
				}
				for _, rec := range tasks {
					UnscheduleTask(rec.SchedulerID)
					T.untrackTask(rec.PhantomID)
				}
				return fmt.Sprintf("Cancelled %d scheduled task(s).", len(tasks)), nil
			}
			var rec PhantomTaskRecord
			if !T.DB.Get(phantomTasksTable, taskID, &rec) || rec.ChatID != chatID {
				return "", fmt.Errorf("no pending task with ID %q — use list_scheduled to see current tasks", taskID)
			}
			UnscheduleTask(rec.SchedulerID)
			T.untrackTask(rec.PhantomID)
			return fmt.Sprintf("Cancelled: %q", rec.Prompt), nil
		},
	}
}
