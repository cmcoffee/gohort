// The Operator's event-monitor tools: create, list and delete monitors that
// stay silent until something changes. Listed in operatorToolDefsUnfolded
// (operator_tools.go); the model reaches them through `schedule`.

package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func createEventMonitorToolDef(sess *ToolSession, owner, agentID string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "create_event_monitor",
			Description: "Set up a monitor that WAKES you when something happens (vs a standing agent, which RUNS on a clock). Pick the CHEAPEST kind that detects the change, deterministic beats an LLM checker: \"webhook\" mints a secret URL an external system POSTs to (push, no polling); \"http_poll\" fetches a URL, extracts a value, and wakes you when it crosses a threshold (no LLM, best for numeric/value conditions); \"watch\" invokes a TOOL each interval, hashes its output, and wakes you ONLY when it changes (no LLM until it does, best for \"tell me when X changes\", e.g. a chat via read_chat); \"poll\" runs an LLM checker agent every interval (MOST expensive: reserve for FUZZY conditions a value or hash can't capture). On wake you react in this thread (report / delegate). NOT for work that must run and REPORT every interval whatever it finds: that is a standing agent (create_standing_agent, with until/max_attempts), not a monitor. NOT for a time of day: \"tell me at 1:10pm\" / \"in 20 minutes\" is set_timer, which needs no URL and no threshold.",
			Parameters: map[string]ToolParam{
				"name":             {Type: "string", Description: "Short unique name for this monitor, e.g. \"nvda-below\" or \"ts-join\"."},
				"kind":             {Type: "string", Description: "\"webhook\", \"http_poll\", \"watch\", or \"poll\": prefer the cheapest that fits (see the tool description)."},
				"wake_brief":       {Type: "string", Description: "What you should do when it fires (guides your reaction). Only used for notify=\"channel\"."},
				"notify":           {Type: "string", Enum: []string{"channel", "direct", "text"}, Description: "How the user is alerted when it fires. \"channel\" (default): wake here in the thread so you can react/summarize (uses an LLM). \"direct\": post the change verbatim into the channel thread with NO LLM (it just shows up here + lights the unread dot). \"text\": text the owner's phone with the change, no LLM. ASK the user which they want when setting a monitor up."},
				"wake_agent":       {Type: "string", Description: "(optional) Name or id of the agent this monitor belongs to: the one woken when it fires. Defaults to you. Set it when you are wiring a watch onto ANOTHER agent, so the alert lands in that agent's thread rather than in this conversation."},
				"bulletin":         {Type: "string", Description: "Optional: the name of a bulletin board (Knowledge, Bulletins). Each change is POSTED to that board with NO LLM and nobody woken: every agent that follows the board sees it on its next turn. Use it to keep several agents up with one feed (today's headlines, a status) that a single monitor fetches. Pair with format_script to trim the change to a few lines; a post is at most 600 characters."},
				"deliver_to":       {Type: "string", Description: "Optional: a chat_id from list_chats (e.g. \"any;+;chat872212368359368118\"). When set, the formatted alert is posted DIRECTLY to THAT conversation with NO LLM, instead of waking you in this thread: use it to route a watch/http_poll alert straight to a group chat or other channel. Setting it forces notify=\"direct\" to that chat. Omit to alert in this thread per notify."},
				"interval_seconds": {Type: "number", Description: "http_poll/watch/poll: how often to check, in seconds (minimum 30; 900 = every 15 min, 3600 = hourly)."},
				"daily_at":         {Type: "string", Description: "http_poll/watch/poll, instead of interval_seconds: check at set times of day, 24-hour HH:MM in the user's zone, \"08:00\" or several, \"08:00,18:00\". Use it for \"each morning\": an interval runs from whenever the monitor was made, not at a time of day."},
				"until":            {Type: "string", Description: "Optional: the stopping CONDITION in plain words, \"the PR is merged\", \"the build goes green\". After each fire the change it saw is judged against this, and the monitor stops itself when the condition is met (kept, not deleted). Use it when the user wants to keep hearing about something UNTIL a state is reached: the monitor's own trigger says when to alert, this says when to stop. Costs one small model call per fire, so omit it when a fire count (stop_after) already says when to stop."},
				"stop_after":       {Type: "number", Description: "Optional: stop the monitor after it has fired this many times. This bounds the ALERTS a monitor raises; it does NOT turn a monitor into a run-this-N-times job, for that use create_standing_agent, whose max_attempts bounds RUNS. Use it whenever the user bounds the alerts: \"tell me the next two times\", \"just once\", \"stop after 3\". On the last fire the monitor pauses itself and says so; it is kept, not deleted, and resuming gives it a fresh allowance. Omit for a monitor that should keep watching until the user stops it."},
				"tool_name":        {Type: "string", Description: "watch only: the tool invoked each interval; its output is hashed and you're woken ONLY when it changes. Use an existing tool that returns the thing to watch (e.g. read_chat for a chat). No LLM runs between changes: the cheapest detection."},
				"tool_args":        {Type: "object", Description: "watch only: arguments passed to tool_name every invocation, e.g. {\"chat_id\":\"any;+;chat123\",\"limit\":10}."},
				"format_script":    {Type: "string", Description: "watch only, optional: sandboxed python that shapes the alert. It receives {\"prior\":...,\"current\":...} JSON on stdin (the previous and current tool output) and prints the notification text to stdout. Printing NOTHING no longer suppresses, it FAILS OPEN to the built-in diff so a broken script can't silently eat a change; to intentionally drop a change, print the sentinel \"SKIP\" (or {\"skip\":true}). No network, no LLM. Omit to use the built-in diff summary. Use this to format exactly the notification you want (e.g. parse a client list and print only \"X joined\" / \"X left\", or \"SKIP\" when only serveradmin changed)."},
				"check_agent":      {Type: "string", Description: "poll only: name/id of an existing agent that checks the condition each interval."},
				"check":            {Type: "string", Description: "poll only: the question/brief given to the checker. Tell it to answer with the match string when the event has happened."},
				"match_contains":   {Type: "string", Description: "poll only: fire when the checker's answer begins with this word (case-insensitive). Default \"YES\". The checker is told to begin with it or with NONE."},
				"url":              {Type: "string", Description: "http_poll only: URL fetched each interval (e.g. a finance JSON API)."},
				"json_path":        {Type: "string", Description: "http_poll: dotted path into the JSON response, array indices included, e.g. \"quoteResponse.result.0.regularMarketPrice\". Omit json_path and regex to compare the whole body."},
				"regex":            {Type: "string", Description: "http_poll: alternative extraction, first capture group of this regex against the body."},
				"compare_op":       {Type: "string", Description: "http_poll: one of < > <= >= == != contains. Fire when extracted_value <op> threshold is true."},
				"threshold":        {Type: "string", Description: "http_poll: the value compared against (a number for < > <= >=)."},
			},
			Required: []string{"name", "kind"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			name := strings.TrimSpace(oArgStr(args, "name"))
			kind := strings.ToLower(strings.TrimSpace(oArgStr(args, "kind")))
			if name == "" {
				return "", fmt.Errorf("name is required")
			}
			if kind != EventKindWebhook && kind != EventKindPoll && kind != EventKindHTTP && kind != EventKindWatch {
				return "", fmt.Errorf("kind must be %q, %q, %q, or %q", EventKindWebhook, EventKindHTTP, EventKindWatch, EventKindPoll)
			}
			if _, exists := GetEventMonitor(RootDB, owner, name); exists {
				return "", fmt.Errorf("a monitor named %q already exists", name)
			}
			notify := strings.ToLower(strings.TrimSpace(oArgStr(args, "notify")))
			switch notify {
			case EventNotifyText, EventNotifyDirect:
				// honored as-is
			default:
				notify = EventNotifyChannel
			}
			// deliver_to routes the formatted alert straight to a specific chat
			// (a group / channel) with no LLM — the DeliverChatID + notify=direct
			// path that already exists in operator_wake. Setting it forces direct
			// delivery to that chat (channel/text wouldn't make sense here).
			deliverTo := strings.TrimSpace(oArgStr(args, "deliver_to"))
			if deliverTo != "" {
				notify = EventNotifyDirect
			}
			// bulletin posts each change to a board every following agent
			// reads, with no LLM and nobody woken (bulletins.go).
			board := bulletinName(oArgStr(args, "bulletin"))
			if board != "" {
				if _, ok := loadBulletin(UserDB(orchestrateBaseDB, owner), board); !ok {
					return "", fmt.Errorf("no bulletin board named %q: make one in Knowledge, Bulletins, first", board)
				}
				notify = monitorNotifyBulletin
			}
			wakeAgentID, err := resolveMonitorWakeAgent(sess, agentID, oArgStr(args, "wake_agent"), notify, deliverTo)
			if err != nil {
				return "", err
			}
			m := EventMonitor{
				Name: name, Owner: owner, Kind: kind, Notify: notify,
				DeliverChatID: deliverTo,
				Bulletin:      board,
				// Wake the agent that created this monitor, IN the session it
				// was created in, so the event lands back where the user set
				// it up (not a hardcoded default thread). WakeSession falls
				// back to the agent's channel home thread when unknown.
				//
				// wake_agent overrides both, and exists because a monitor has
				// no other way to name a target: unlike a standing agent, its
				// whole shape is "wake me when this happens". An AUTHOR
				// setting one up for somebody else's agent has to say whose,
				// or the alert fires into a build session nobody reopens.
				WakeAgent: wakeAgentID,
				WakeSession: func() string {
					if wakeAgentID != agentID {
						return "" // handed over: its own thread, not the author's
					}
					if sess != nil {
						return sess.ChatSessionID
					}
					return ""
				}(),
				WakeBrief: strings.TrimSpace(oArgStr(args, "wake_brief")), Created: time.Now(),
				// A bound on the alerts, for every kind including webhook —
				// fireWake counts and stops. Negative reads as unbounded
				// rather than as an immediate stop, which would create a
				// monitor that is over before it starts.
				MaxFires: maxInt(oArgInt(args, "stop_after"), 0),
				Until:    strings.TrimSpace(oArgStr(args, "until")),
			}
			// daily_at checks at set times of day instead of on the interval.
			if at := strings.TrimSpace(oArgStr(args, "daily_at")); at != "" {
				if kind == EventKindWebhook {
					return "", fmt.Errorf("a webhook monitor fires when something posts to it: it has no schedule for daily_at")
				}
				mins, err := parseDailyAt(at)
				if err != nil {
					return "", err
				}
				m.DailyAt = mins
			}
			if kind == EventKindWebhook {
				m.Token = NewEventToken()
				SaveEventMonitor(RootDB, m)
				return fmt.Sprintf("Webhook monitor %q created. Have the external system POST JSON {\"summary\":\"...\"} to:\n  <your gohort base URL>/orchestrate/api/operator/event/%s\nEach POST wakes me in this thread.%s", name, m.Token, fireLimitSentence(m)), nil
			}
			if kind == EventKindHTTP {
				m.URL = strings.TrimSpace(oArgStr(args, "url"))
				m.JSONPath = strings.TrimSpace(oArgStr(args, "json_path"))
				m.Regex = strings.TrimSpace(oArgStr(args, "regex"))
				m.CompareOp = strings.TrimSpace(oArgStr(args, "compare_op"))
				m.Threshold = strings.TrimSpace(oArgStr(args, "threshold"))
				m.IntervalSeconds = oArgInt(args, "interval_seconds")
				if m.IntervalSeconds <= 0 {
					m.IntervalSeconds = 900
				}
				if err := missingArgs("http_poll monitor "+strconv.Quote(name),
					reqArg{"url", m.URL, "the address fetched each interval, e.g. \"https://example.com/status\""},
					reqArg{"compare_op", m.CompareOp, "one of < > <= >= == != contains"},
					reqArg{"threshold", m.Threshold, "the value compared against, always as a string: \"200\" with compare_op \"==\", or \"error\" with compare_op \"contains\". If you have no threshold because this should report the value WHATEVER it is, that is not a monitor: a monitor stays silent until something changes. Use schedule(when=\"every\", then=the work, until = what makes it done, max_attempts = how many runs it gets)"},
				); err != nil {
					return "", err
				}
				if err := ValidateThreshold(m.CompareOp, m.Threshold); err != nil {
					return "", err
				}
				if !ValidCompareOp(m.CompareOp) {
					return "", fmt.Errorf("compare_op must be one of < > <= >= == != contains")
				}
				extractDesc := "the response body"
				if m.JSONPath != "" {
					extractDesc = "json_path " + m.JSONPath
				} else if m.Regex != "" {
					extractDesc = "a regex match"
				}
				SaveEventMonitor(RootDB, m)
				if err := ScheduleEventMonitor(RootDB, m); err != nil {
					return "", fmt.Errorf("saved but scheduling failed: %w", err)
				}
				got, _ := GetEventMonitor(RootDB, owner, name)
				return fmt.Sprintf("HTTP monitor %q created: %s I fetch %s, read %s, and wake you when the value %s %s. Fires once on the crossing (and re-arms after it recovers).%s Next check: %s.",
					name, monitorCadence(got), m.URL, extractDesc, m.CompareOp, m.Threshold,
					fireLimitSentence(m), got.NextCheck.Local().Format("Mon Jan 2 3:04 PM")) + dupMonitorWarning(m), nil
			}
			if kind == EventKindWatch {
				m.ToolName = strings.TrimSpace(oArgStr(args, "tool_name"))
				if ta, ok := args["tool_args"].(map[string]any); ok {
					m.ToolArgs = ta
				}
				m.FormatScript = oArgStr(args, "format_script")
				m.IntervalSeconds = oArgInt(args, "interval_seconds")
				if m.IntervalSeconds <= 0 {
					m.IntervalSeconds = 60
				}
				if err := missingArgs("watch monitor "+strconv.Quote(name),
					reqArg{"tool_name", m.ToolName, "the tool invoked each interval, whose output is hashed to detect a change"},
				); err != nil {
					return "", err
				}
				// Seed the change baseline now from a known-good probe, so the
				// first poll detects a REAL change instead of firing on the
				// initial content.
				body, perr := InvokeWatchTool(owner, m.WakeAgent, m.ToolName, m.ToolArgs)
				if errors.Is(perr, errWatchToolMakesChanges) {
					return "", perr
				}
				if perr == nil {
					m.LastHash = HashWatcherBody(body)
				}
				SaveEventMonitor(RootDB, m)
				if err := ScheduleEventMonitor(RootDB, m); err != nil {
					return "", fmt.Errorf("saved but scheduling failed: %w", err)
				}
				got, _ := GetEventMonitor(RootDB, owner, name)
				if m.DeliverChatID != "" {
					return fmt.Sprintf("Watch monitor %q created: %s I run %s and, when its output changes, post the formatted alert DIRECTLY to chat %s, no LLM, it does NOT come back to this thread.%s Next check: %s.",
						name, monitorCadence(got), m.ToolName, m.DeliverChatID, fireLimitSentence(m), got.NextCheck.Local().Format("Mon Jan 2 3:04 PM")) + dupMonitorWarning(m), nil
				}
				return fmt.Sprintf("Watch monitor %q created: %s I run %s and wake you ONLY when its output changes, no LLM runs in between.%s Next check: %s.",
					name, monitorCadence(got), m.ToolName, fireLimitSentence(m), got.NextCheck.Local().Format("Mon Jan 2 3:04 PM")) + dupMonitorWarning(m), nil
			}
			wantAgent := strings.TrimSpace(oArgStr(args, "check_agent"))
			m.Check = strings.TrimSpace(oArgStr(args, "check"))
			m.MatchContains = strings.TrimSpace(oArgStr(args, "match_contains"))
			m.IntervalSeconds = oArgInt(args, "interval_seconds")
			if err := missingArgs("poll monitor "+strconv.Quote(name),
				reqArg{"check_agent", wantAgent, "name or id of an existing agent that runs the check"},
				reqArg{"check", m.Check, "the question that agent is asked each interval, whose answer decides whether to wake you. If there is no question (the agent should just do the work and report every time), use schedule(when=\"every\") instead; telling a checker to always answer the match word makes a schedule out of a monitor"},
			); err != nil {
				return "", err
			}
			// Resolve the checker to a REAL agent. The LLM may pass a display
			// name or its own conversational nickname that isn't an actual
			// agent id; resolve by id, then by name, and finally fall back to
			// the channel agent creating the monitor (which exists and can
			// self-check with its own tools) so the monitor never saves broken.
			m.CheckAgent = resolveCheckAgent(sess, owner, wantAgent, agentID)
			SaveEventMonitor(RootDB, m)
			if err := ScheduleEventMonitor(RootDB, m); err != nil {
				return "", fmt.Errorf("saved but scheduling failed: %w", err)
			}
			match := m.MatchContains
			if match == "" {
				match = "YES"
			}
			got, _ := GetEventMonitor(RootDB, owner, name)
			return fmt.Sprintf("Poll monitor %q created: %s, agent %q is asked %q; I wake when the answer begins with %q.%s Next check: %s.",
				name, monitorCadence(got), m.CheckAgent, m.Check, match, fireLimitSentence(m), got.NextCheck.Local().Format("Mon Jan 2 3:04 PM")) + dupMonitorWarning(m), nil
		},
	}
}

func listEventMonitorsToolDef(owner, controllerAgentID string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "list_event_monitors",
			Description: "List the user's event monitors (webhook, http_poll, watch, poll, timer) with their kind, schedule, paused state, and when each last fired.",
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			// Scope to THIS agent's monitors (WakeAgent set on create), not
			// every monitor the owner has across all their agents.
			var ms []EventMonitor
			for _, m := range ListEventMonitors(RootDB, owner) {
				if m.WakeAgent == controllerAgentID {
					ms = append(ms, m)
				}
			}
			if len(ms) == 0 {
				return "No event monitors are set up.", nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%d event monitor(s):\n", len(ms))
			for _, m := range ms {
				state := "active"
				if m.Paused {
					state = "paused"
				}
				fmt.Fprintf(&b, "- %s [%s, %s]", m.Name, m.Kind, state)
				switch m.Kind {
				case EventKindPoll:
					fmt.Fprintf(&b, ": every %ds via %q", m.IntervalSeconds, m.CheckAgent)
				case EventKindHTTP:
					fmt.Fprintf(&b, ": every %ds fetch %s, value %s %s", m.IntervalSeconds, m.URL, m.CompareOp, m.Threshold)
				case EventKindWebhook:
					fmt.Fprintf(&b, ": POST .../orchestrate/api/operator/event/%s", m.Token)
				case EventKindTimer:
					fmt.Fprintf(&b, ": goes off %s", m.FireAt.In(UserLocation(owner)).Format("Mon Jan 2 3:04 PM MST"))
				}
				if !m.LastFired.IsZero() {
					fmt.Fprintf(&b, "; last fired %s", m.LastFired.Local().Format("Jan 2 3:04 PM"))
				}
				b.WriteString(lastCheckNote(m))
				if lbl := m.FireLabel(); lbl != "" {
					fmt.Fprintf(&b, "; %s", lbl)
				}
				if lbl := objectiveStateLabel(monitorObjective(m)); lbl != "" {
					fmt.Fprintf(&b, "; %s", lbl)
				}
				if lbl := m.StuckLabel(); lbl != "" {
					fmt.Fprintf(&b, "; %s", lbl)
				}
				b.WriteString("\n")
			}
			return strings.TrimSpace(b.String()), nil
		},
	}
}

func deleteEventMonitorToolDef(owner, controllerAgentID string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "delete_event_monitor",
			Description: "Delete an event monitor by name (stops its polling / invalidates its webhook).",
			Parameters:  map[string]ToolParam{"name": {Type: "string", Description: "The monitor's name."}},
			Required:    []string{"name"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			name := strings.TrimSpace(oArgStr(args, "name"))
			// Scope to THIS agent — don't let one agent delete another's monitor.
			if m, ok := GetEventMonitor(RootDB, owner, name); !ok || m.WakeAgent != controllerAgentID {
				return "", fmt.Errorf("no event monitor named %q", name)
			}
			DeleteEventMonitor(RootDB, owner, name)
			return fmt.Sprintf("Deleted event monitor %q.", name), nil
		},
	}
}
