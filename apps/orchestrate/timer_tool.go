package orchestrate

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/oddjob/core"
)

// set_timer is the one-call answer to "tell me at 1:10pm" / "ping me in 20
// minutes". Before it existed the nearest tool was create_event_monitor, and
// an agent asked for a 1:10pm alert built an http_poll against a public
// time API, comparing an ISO datetime string with >=: every check failed
// and the monitor parked itself before the time came. A clock needs no URL,
// no threshold and no interval, so the tool takes a time and a note and
// nothing else it can get wrong.
//
// Underneath it is a one-shot event monitor of the timer kind, so the
// console, the run ledger and the wake path all apply unchanged, and it
// removes itself once it has gone off.
func timerToolDef(sess *ToolSession, owner, agentID string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "set_timer",
			Description: "Wake yourself ONCE at a time of day or after a delay: \"tell me when it's 1:10pm\", \"remind me at 3\", \"ping me in 20 minutes\". The clock is the trigger: nothing is fetched or checked, so never build a monitor for this. When it goes off you are woken here with the note and do what it says (tell the user, start the work). For something that repeats on a clock use create_standing_agent (cron); for \"when X happens\" use create_event_monitor.",
			Parameters: map[string]ToolParam{
				"at":     {Type: "string", Description: "When it goes off, in the user's own zone (the one time_in_zone reports): a time of day (\"13:10\", \"1:10pm\", \"3 PM\"; a time already past today means tomorrow), a delay (\"in 20 minutes\", \"in 1h30m\", \"in 90s\"), or an ISO8601 timestamp (\"2026-10-10T13:10:00-07:00\")."},
				"note":   {Type: "string", Description: "What to do when it goes off, handed back to you on wake: \"tell the user it's 1:10pm, as they asked\", \"start the nightly export\"."},
				"name":   {Type: "string", Description: "Optional short name; one is made from the time when omitted."},
				"notify": {Type: "string", Enum: []string{"channel", "direct", "text"}, Description: "How the user hears about it. \"channel\" (default): wake you here so you can say it in your own words. \"direct\": post the note into the thread with NO LLM. \"text\": text the owner's phone with the note, no LLM."},
			},
			Required: []string{"at", "note"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			loc := UserLocation(owner)
			now := time.Now().In(loc)
			at, err := parseTimerAt(oArgStr(args, "at"), now)
			if err != nil {
				return "", err
			}
			note := strings.TrimSpace(oArgStr(args, "note"))
			if note == "" {
				return "", fmt.Errorf("note is required: what to do when the timer goes off")
			}
			name := strings.TrimSpace(oArgStr(args, "name"))
			if name == "" {
				name = "timer-" + at.Format("0304pm")
			}
			if _, exists := GetEventMonitor(RootDB, owner, name); exists {
				return "", fmt.Errorf("a monitor named %q already exists", name)
			}
			notify := strings.ToLower(strings.TrimSpace(oArgStr(args, "notify")))
			switch notify {
			case EventNotifyText, EventNotifyDirect:
			default:
				notify = EventNotifyChannel
			}
			wakeAgentID, err := resolveMonitorWakeAgent(sess, agentID, "", notify, "")
			if err != nil {
				return "", err
			}
			m := EventMonitor{
				Name: name, Owner: owner, Kind: EventKindTimer, Notify: notify,
				WakeAgent: wakeAgentID,
				WakeSession: func() string {
					if wakeAgentID == agentID && sess != nil {
						return sess.ChatSessionID
					}
					return ""
				}(),
				WakeBrief: note, FireAt: at, OneShot: true, Created: time.Now(),
			}
			SaveEventMonitor(RootDB, m)
			if err := ScheduleEventMonitor(RootDB, m); err != nil {
				return "", fmt.Errorf("saved but scheduling failed: %w", err)
			}
			return fmt.Sprintf("Timer %q set for %s (%s from now). When it goes off I am woken here with: %s. It fires once and then removes itself.",
				name, at.Format("Mon Jan 2 3:04 PM MST"), humanUntil(at.Sub(now)), note), nil
		},
	}
}

var (
	timerDelayRe = regexp.MustCompile(`(?i)^in\s+(.+)$`)
	timerUnitRe  = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s)\b`)
	timerClockRe = regexp.MustCompile(`(?i)^(\d{1,2})(?::(\d{2}))?\s*(am|pm|a\.m\.|p\.m\.)?$`)
)

// parseTimerAt turns what the agent wrote into the moment the timer goes
// off, in now's location. It takes a delay ("in 20 minutes", "in 1h30m"), a
// clock time ("13:10", "1:10pm", "3 PM"; one already past today is
// tomorrow's), or an ISO8601 timestamp. The result is always in the future.
func parseTimerAt(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("at is required: a time of day (\"13:10\", \"1:10pm\"), a delay (\"in 20 minutes\"), or an ISO8601 timestamp")
	}
	if mm := timerDelayRe.FindStringSubmatch(s); mm != nil {
		d, err := parseTimerDelay(mm[1])
		if err != nil {
			return time.Time{}, err
		}
		return now.Add(d), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			if !t.After(now) {
				return time.Time{}, fmt.Errorf("%s is already past (it is %s now)", t.Format("Mon Jan 2 3:04 PM MST"), now.Format("3:04 PM MST"))
			}
			return t, nil
		}
	}
	if mm := timerClockRe.FindStringSubmatch(s); mm != nil {
		h, _ := strconv.Atoi(mm[1])
		min := 0
		if mm[2] != "" {
			min, _ = strconv.Atoi(mm[2])
		}
		ampm := strings.ToLower(strings.ReplaceAll(mm[3], ".", ""))
		if min > 59 || h > 23 || (ampm != "" && (h == 0 || h > 12)) || (ampm == "" && mm[2] == "") {
			return time.Time{}, fmt.Errorf("%q is not a time of day", s)
		}
		switch {
		case ampm == "pm" && h < 12:
			h += 12
		case ampm == "am" && h == 12:
			h = 0
		}
		y, mo, d := now.Date()
		t := time.Date(y, mo, d, h, min, 0, 0, now.Location())
		if !t.After(now) {
			t = t.AddDate(0, 0, 1)
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("could not read %q as a time: use a time of day (\"13:10\", \"1:10pm\"), a delay (\"in 20 minutes\"), or an ISO8601 timestamp", s)
}

// parseTimerDelay reads the part after "in": "20 minutes", "1h30m", "90s",
// "2 hours 15 min". Go's own duration form is accepted first.
func parseTimerDelay(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if d, err := time.ParseDuration(strings.ReplaceAll(s, " ", "")); err == nil && d > 0 {
		return d, nil
	}
	var total time.Duration
	for _, mm := range timerUnitRe.FindAllStringSubmatch(s, -1) {
		n, _ := strconv.ParseFloat(mm[1], 64)
		switch strings.ToLower(mm[2])[0] {
		case 'h':
			total += time.Duration(n * float64(time.Hour))
		case 'm':
			total += time.Duration(n * float64(time.Minute))
		case 's':
			total += time.Duration(n * float64(time.Second))
		}
	}
	if total <= 0 {
		return 0, fmt.Errorf("could not read the delay %q: say \"in 20 minutes\", \"in 1h30m\", or \"in 90s\"", s)
	}
	return total, nil
}

// humanUntil renders a wait the way a person says it: "4 minutes", "1h 30m".
func humanUntil(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%d hours", h)
		}
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%.1f days", d.Hours()/24)
}
