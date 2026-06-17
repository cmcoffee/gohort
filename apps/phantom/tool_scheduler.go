package phantom

import (
	"context"
	"encoding/json"
	"fmt"
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
}

// splitRules splits a newline-separated rules string (as saved by a "rules"
// FormField) into trimmed, non-empty lines.
func splitRules(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			out = append(out, ln)
		}
	}
	return out
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

	// Unified trigger engine (the `schedule` tool). Phantom-chat triggers
	// deliver into a conversation: action=callback runs the agent turn and
	// queues its reply; action=notify queues the summary verbatim. The legacy
	// phantom.callback handler above stays registered for in-flight reminders
	// and LLM-scheduled callbacks.
	RegisterTriggerAction("phantom_chat", func(ctx context.Context, t ScheduledTrigger, summary string) {
		switch t.Action {
		case ActionNotify:
			text := strings.TrimSpace(summary)
			if text == "" {
				return
			}
			T.rememberRecentReply(text)
			storeMessage(T.DB, PhantomMessage{
				ID:        now() + "-" + newID(),
				ChatID:    t.TargetID,
				Role:      "assistant",
				Text:      text,
				Timestamp: now(),
			})
			enqueueOutbox(T.DB, OutboxItem{
				ID:      newID(),
				ChatID:  t.TargetID,
				Handle:  t.TargetMeta,
				Text:    text,
				Type:    "scheduled",
				Created: now(),
			})
		default: // ActionCallback
			T.composeScheduledReply(ctx, t.TargetID, t.TargetMeta, t.Prompt)
		}
	})
	RegisterStopConditionChecker(func(ctx context.Context, t ScheduledTrigger) bool {
		if t.TargetKind != "phantom_chat" {
			return false
		}
		var recentMsgs []Message
		for _, m := range recentMessages(T.DB, t.TargetID, 10) {
			recentMsgs = append(recentMsgs, Message{Role: m.Role, Content: m.Text})
		}
		return T.evalStopCondition(ctx, t.RepeatUntil, t.RepeatCount, t.TargetMeta, recentMsgs)
	})
	StartTriggerScheduler()
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
				if _, err := NextCronOccurrence(repeatCron, time.Now()); err != nil {
					return "", fmt.Errorf("invalid repeat_cron: %w", err)
				}
			}
			repeatUntil, _ := args["repeat_until"].(string)
			repeatUntil = strings.TrimSpace(repeatUntil)
			repeatRandomWindow, _ := args["repeat_random_window"].(string)
			repeatRandomWindow = strings.TrimSpace(repeatRandomWindow)
			if repeatRandomWindow != "" {
				if _, _, _, _, werr := ParseWindowBounds(repeatRandomWindow); werr != nil {
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

// repeatConditionMet asks the LLM whether the repeat_until condition has been
// satisfied for a legacy scheduled task.
func (T *Phantom) repeatConditionMet(ctx context.Context, p phantomCallPayload, recentMessages []Message) bool {
	return T.evalStopCondition(ctx, p.RepeatUntil, p.RepeatCount, p.Handle, recentMessages)
}

// evalStopCondition asks the LLM whether a repeat_until stopping condition has
// been met (returns true = stop repeating). Shared by the legacy scheduler and
// the unified trigger engine's stop-condition checker. A failed/empty LLM call
// continues the repeat (returns false).
func (T *Phantom) evalStopCondition(ctx context.Context, repeatUntil string, repeatCount int, handle string, recentMessages []Message) bool {
	if repeatUntil == "" {
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
		repeatUntil,
		repeatCount,
	)
	msgs := make([]Message, 0, len(recentMessages)+1)
	msgs = append(msgs, recentMessages...)
	msgs = append(msgs, Message{Role: "user", Content: "Has the stopping condition been met? Answer YES or NO."})

	condCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := T.LLM.Chat(condCtx, msgs, WithSystemPrompt(sysPrompt), WithThink(false))
	if err != nil || resp == nil {
		Log("[phantom/scheduler] condition check failed for %s: %v — continuing repeat", handle, err)
		return false
	}
	answer := strings.ToUpper(strings.TrimSpace(resp.Content))
	met := strings.HasPrefix(answer, "YES")
	if met {
		Log("[phantom/scheduler] repeat_until condition met for %s: %q", handle, repeatUntil)
	}
	return met
}

// fireScheduledCall runs the LLM with the scheduled prompt and enqueues the reply.
func (T *Phantom) fireScheduledCall(ctx context.Context, p phantomCallPayload) {
	// Remove the tracking record now that this task is firing.
	// The reschedule path will create a new record with the fresh task ID.
	T.untrackTask(p.PhantomTaskID)

	// Compose + deliver the scheduled turn (shared with the unified trigger
	// engine). If nothing was delivered (LLM error, stay_silent with nothing,
	// empty, or a duplicate), don't reschedule — matching the prior behavior.
	if _, delivered := T.composeScheduledReply(ctx, p.ChatID, p.Handle, p.Prompt); !delivered {
		return
	}

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

	switch {
	case p.RepeatRandomWindow != "":
		// Random-window repeats use a single slot covering the full window:
		// fire once at a random time in this daily window.
		next, err := NextRandomWindowTime(p.RepeatRandomWindow, time.Now(), 1, 0)
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
		next, err := NextCronOccurrence(p.RepeatCron, time.Now())
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

// composeScheduledReply runs one scheduled/callback agent turn for a chat: it
// builds the persona system prompt + recent history, assembles the FULL live
// tool catalog (assembleConvTools, so a fired callback has the same tools a live
// turn does), runs the agent loop, and enqueues the reply (plus any gathered
// attachments) to the chat's outbox. Shared by the legacy scheduler fire path
// (fireScheduledCall) and the unified trigger engine's callback action
// (RegisterTriggerAction "phantom_chat"). Returns the delivered text and whether
// anything was delivered (false on LLM error / stay_silent-with-nothing / empty
// response / duplicate) so the caller can decide whether to reschedule.
func (T *Phantom) composeScheduledReply(ctx context.Context, chatID, handle, instruction string) (string, bool) {
	cfg := defaultConfig(T.DB)
	var conv Conversation
	T.DB.Get(conversationTable, chatID, &conv)

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
		senderDesc = fmt.Sprintf("%s (%s)", conv.DisplayName, handle)
	} else {
		senderDesc = handle
	}

	// Recent history so the LLM can judge whether the timing is right.
	history := recentMessages(T.DB, chatID, 10)
	var msgs []Message
	for _, m := range history {
		role := "user"
		content := m.Text
		if m.Role == "assistant" {
			role = "assistant"
		} else {
			label := handle
			if conv.DisplayName != "" {
				label = conv.DisplayName
			}
			content = label + ": " + m.Text
		}
		msgs = append(msgs, Message{Role: role, Content: content})
	}
	msgs = append(msgs, Message{Role: "user", Content: "Compose your scheduled message now, or call stay_silent if the timing is not right."})

	// The scheduled instruction is injected into the system prompt so the LLM
	// composes an outbound message naturally rather than answering a fake turn.
	sysPrompt := fmt.Sprintf(
		"Current date and time: %s\n\nYour name is %s. The person you are messaging is %s.\n\n%s%s\n\n"+
			"## Scheduled Message Instruction\n%s\n\n"+
			"Compose a natural outbound message to the user following the instruction above. "+
			"Do not mention that this is scheduled or automated. "+
			"When you know someone's name, use it naturally. "+
			"Keep responses varied — avoid repetitive patterns. "+
			"If the conversation history or language rules suggest the timing is not right, call stay_silent instead of sending. "+
			"IMPORTANT: Reply in plain text only. No markdown. This is a text message conversation.",
		time.Now().Format("Monday, January 2, 2006 3:04 PM MST"), personaName, senderDesc, basePrompt, memoryBlock(T.DB, chatID), instruction,
	)

	sess := &ToolSession{
		LLM:          T.LLM,
		LeadLLM:      T.LeadLLM,
		WorkspaceDir: ensurePhantomWorkspace(cfg),
		DB:           T.DB,
		Username:     cfg.OwnerHandle,
	}
	for _, pt := range LoadPersistentTempTools(sess.DB, sess.Username) {
		tt := pt.Tool
		_ = sess.AppendTempTool(&tt)
	}
	// send_status: enqueue an immediate outbox item so the user gets a progress
	// iMessage before the scheduled reply. FIFO ordering preserves arrival order.
	sess.StatusCallback = func(text string) {
		text = strings.TrimSpace(stripEmojis(text))
		if text == "" {
			return
		}
		Log("[phantom/scheduler] send_status for %s: %q", handle, text)
		T.rememberRecentReply(text)
		storeMessage(T.DB, PhantomMessage{
			ID:        now() + "-" + newID(),
			ChatID:    chatID,
			Role:      "assistant",
			Text:      text,
			Timestamp: now(),
		})
		enqueueOutbox(T.DB, OutboxItem{
			ID:      newID(),
			ChatID:  chatID,
			Handle:  handle,
			Text:    text,
			Type:    "status",
			Created: now(),
		})
	}
	// Full catalog, same as a live turn (recall + skill tools included) so a
	// fired callback can actually use what it was scheduled for, e.g. a calling
	// tool reached through a skill. includeScheduler=false to avoid a scheduled
	// task scheduling itself recursively. Fresh deliveredSkills per fire.
	tools := T.assembleConvTools(chatID, handle, conv, cfg, sess, map[string]bool{}, false)

	phantomChatOpts := buildThinkOpts("app.phantom")
	resp, _, err := T.RunAgentLoop(ctx, msgs, AgentLoopConfig{
		SystemPrompt: sysPrompt,
		Tools:        tools,
		MaxRounds:    15,
		RouteKey:     "app.phantom",
		PromptTools:  T.PromptTools,
		ChatOptions:  phantomChatOpts,
		Confirm:      func(string, string) bool { return true },
		// Drain any view-images deposited by tools so the LLM sees the frames on
		// its next round (LLM-only, not delivered to user).
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
		Log("[phantom/scheduler] LLM error for %s: %v", handle, err)
		return "", false
	}

	var replyText string
	if resp != nil {
		replyText = strings.TrimSpace(stripEmojis(resp.Content))
		// Consume [ATTACH: ...] markers so the file attaches and the marker
		// doesn't leak as raw text (the interactive path does this too).
		replyText, _ = applyAttachMarkers(sess, replyText)
	}
	sessionImages := filterNewImages(sess.Images)
	sessionVideos := filterNewVideos(sess.Videos)
	sessionVideos = normalizeAudioForDelivery(sessionVideos)

	// stay_silent suppresses the text but lets gathered attachments through.
	if sess.Silenced {
		if len(sessionImages) == 0 && len(sessionVideos) == 0 {
			Log("[phantom/scheduler] stay_silent called for %s — no scheduled reply sent", handle)
			return "", false
		}
		Log("[phantom/scheduler] stay_silent called for %s — text suppressed, %d images / %d videos still delivered", handle, len(sessionImages), len(sessionVideos))
		replyText = ""
	}
	if replyText == "" && len(sessionImages) == 0 && len(sessionVideos) == 0 {
		if len(sess.Images) > 0 || len(sess.Videos) > 0 {
			Log("[phantom/scheduler] all attachments already sent to %s, nothing new to queue", handle)
		} else if resp == nil {
			Log("[phantom/scheduler] empty LLM response for %s — nil response from RunAgentLoop", handle)
		} else {
			Log("[phantom/scheduler] empty LLM response for %s — content=%d chars, reasoning=%d chars, tool_calls=%d",
				handle, len(resp.Content), len(resp.Reasoning), len(resp.ToolCalls))
		}
		return "", false
	}

	// Replay guard: drop if this exact text was sent recently (catches
	// rescued-empty-response replays and coalesced re-run repeats).
	if T.matchesRecentReply(replyText) {
		Log("[phantom/scheduler] dropping duplicate scheduled reply for %s (matches recently-sent text)", handle)
		return "", false
	}
	T.rememberRecentReply(replyText)
	enqueueOutbox(T.DB, OutboxItem{
		ID:      newID(),
		ChatID:  chatID,
		Handle:  handle,
		Text:    replyText,
		Images:  sessionImages,
		Videos:  sessionVideos,
		Type:    "scheduled",
		Created: now(),
	})
	Log("[phantom/scheduler] queued reply for %s: %q", handle, replyText)
	return replyText, true
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
