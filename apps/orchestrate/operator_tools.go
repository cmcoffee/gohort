// The Operator agent's exclusive fleet-management catalog: create / list /
// run / pause standing (scheduled) agents and read the run-ledger.
//
// Like Builder's authoring tools, these are NOT globally registered — they're
// appended at run time ONLY for orchestrator-mode agents (the gate lives in
// runner.go's catalog assembly and the dispatch paths), so no other agent
// gets them. Owner-scoped to the runtime user; reuses the shared core spine
// (standing-agent store + run-ledger). The actual execution of a standing
// agent is handled by the registered standing runner (standing_runner.go).

package orchestrate

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func operatorManagementTools(sess *ToolSession) []AgentToolDef {
	owner := ""
	if sess != nil {
		owner = sess.Username
	}
	return []AgentToolDef{
		{
			Tool: Tool{
				Name:        "delegate",
				Description: "Delegate a task to an existing agent — this is how you DO work (you are a controller, you delegate rather than acting directly). If the agent is pre-authorized the delegation runs now and you get the result; otherwise it's queued for the user's approval in the Authorizations pane and you tell them it's waiting.",
				Parameters: map[string]ToolParam{
					"agent": {Type: "string", Description: "Name or id of the existing agent to delegate to."},
					"brief": {Type: "string", Description: "What the agent should do."},
				},
				Required: []string{"agent", "brief"},
			},
			Handler: func(args map[string]any) (string, error) {
				agent := strings.TrimSpace(oArgStr(args, "agent"))
				brief := strings.TrimSpace(oArgStr(args, "brief"))
				if agent == "" || brief == "" {
					return "", fmt.Errorf("agent and brief are required")
				}
				if IsDelegationPreAuthorized(RootDB, owner, agent) {
					rec := RunDelegation(context.Background(), RootDB, owner, agent, brief)
					if rec.Status == RunFailed {
						return fmt.Sprintf("Delegated to %q but it failed: %s", agent, rec.Err), nil
					}
					out := strings.TrimSpace(rec.Summary)
					if out == "" {
						out = strings.TrimSpace(rec.Raw)
					}
					return fmt.Sprintf("Delegated to %q (pre-authorized). Result:\n%s", agent, out), nil
				}
				a := SaveAuthorization(RootDB, Authorization{Owner: owner, Agent: agent, Brief: brief})
				return fmt.Sprintf("Queued a delegation to %q for the user's approval — it's in the Authorizations pane (id %s) and runs once approved.", agent, a.ID), nil
			},
		},
		{
			Tool: Tool{
				Name:        "create_standing_agent",
				Description: "Create a standing (scheduled) agent and start its schedule. agent_id must name an agent that already exists. Schedule it EITHER with cron (recurring at a wall-clock time — preferred for \"every day at HH:MM\") OR with interval_seconds + optional start_at (a specific first run, then a fixed interval).",
				Parameters: map[string]ToolParam{
					"name":             {Type: "string", Description: "Short unique name for this standing job, e.g. \"daily-weather\"."},
					"agent_id":         {Type: "string", Description: "Name or id of an existing agent to run."},
					"mission":          {Type: "string", Description: "What the agent should do each run."},
					"cron":             {Type: "string", Description: "Recurring wall-clock schedule: \"daily 08:00\", \"FRI 21:30\", \"weekdays 17:00\". Use this for \"every day at 8am\" (fires tomorrow 8am, then daily). Leave empty if using interval_seconds."},
					"start_at":         {Type: "string", Description: "ISO8601 first-run time, e.g. 2026-06-10T08:00:00-07:00. Use with interval_seconds for an arbitrary start + interval. Omit when using cron."},
					"interval_seconds": {Type: "number", Description: "Recurrence interval in seconds (86400 = daily, 21600 = every 6h, 3600 = hourly). Use with optional start_at. Omit when using cron."},
				},
				Required: []string{"name", "agent_id"},
			},
			Handler: func(args map[string]any) (string, error) {
				name := strings.TrimSpace(oArgStr(args, "name"))
				agentID := strings.TrimSpace(oArgStr(args, "agent_id"))
				cron := strings.TrimSpace(oArgStr(args, "cron"))
				interval := oArgInt(args, "interval_seconds")
				if name == "" || agentID == "" {
					return "", fmt.Errorf("name and agent_id are required")
				}
				sa := StandingAgent{
					Name: name, Owner: owner, AgentID: agentID,
					Mission: strings.TrimSpace(oArgStr(args, "mission")), Created: time.Now(),
				}
				switch {
				case cron != "":
					if _, err := NextCronOccurrence(cron, time.Now()); err != nil {
						return "", fmt.Errorf("invalid cron %q: %w", cron, err)
					}
					sa.Cron = cron
				case interval > 0:
					sa.IntervalSeconds = interval
					if startStr := strings.TrimSpace(oArgStr(args, "start_at")); startStr != "" {
						t, err := time.Parse(time.RFC3339, startStr)
						if err != nil {
							return "", fmt.Errorf("invalid start_at (use ISO8601 like 2026-06-10T08:00:00-07:00): %w", err)
						}
						sa.StartAt = t
					}
				default:
					return "", fmt.Errorf("provide a schedule: either cron, or interval_seconds (with optional start_at)")
				}
				SaveStandingAgent(RootDB, sa)
				if err := ScheduleStandingAgent(RootDB, sa); err != nil {
					return "", fmt.Errorf("saved but scheduling failed: %w", err)
				}
				got, _ := GetStandingAgent(RootDB, owner, name)
				return fmt.Sprintf("Created standing agent %q running %q on %s. Next run: %s.",
					name, agentID, StandingScheduleLabel(got), got.NextRun.Local().Format("Mon Jan 2 3:04 PM")), nil
			},
		},
		{
			Tool: Tool{
				Name:        "list_standing_agents",
				Description: "List the user's standing agents with their schedule, paused state, last run status, and next run.",
			},
			Handler: func(args map[string]any) (string, error) {
				list := ListStandingAgents(RootDB, owner)
				if len(list) == 0 {
					return "No standing agents are set up yet.", nil
				}
				var b strings.Builder
				fmt.Fprintf(&b, "%d standing agent(s):\n", len(list))
				for _, sa := range list {
					status := "never run"
					if latest := ListRuns(RootDB, owner, RunFilter{Agent: sa.Name, Limit: 1}); len(latest) > 0 {
						status = string(latest[0].Status)
					}
					state := "active"
					if sa.Paused {
						state = "paused"
					}
					next := "—"
					if !sa.NextRun.IsZero() {
						next = sa.NextRun.Local().Format("Mon Jan 2 3:04 PM")
					}
					fmt.Fprintf(&b, "- %s (%s): runs %q on %s; last=%s; next=%s\n",
						sa.Name, state, sa.AgentID, StandingScheduleLabel(sa), status, next)
				}
				return strings.TrimSpace(b.String()), nil
			},
		},
		{
			Tool: Tool{
				Name:        "run_standing_now",
				Description: "Trigger a standing agent to run immediately (does not change its recurring schedule).",
				Parameters:  map[string]ToolParam{"name": {Type: "string", Description: "The standing agent's name."}},
				Required:    []string{"name"},
			},
			Handler: func(args map[string]any) (string, error) {
				name := strings.TrimSpace(oArgStr(args, "name"))
				if _, ok := GetStandingAgent(RootDB, owner, name); !ok {
					return "", fmt.Errorf("no standing agent named %q", name)
				}
				if err := RunStandingNow(RootDB, owner, name); err != nil {
					return "", err
				}
				return fmt.Sprintf("Triggered %q to run now. The result will appear in Activity shortly.", name), nil
			},
		},
		{
			Tool: Tool{
				Name:        "set_standing_paused",
				Description: "Pause or resume a standing agent's schedule.",
				Parameters: map[string]ToolParam{
					"name":   {Type: "string", Description: "The standing agent's name."},
					"paused": {Type: "boolean", Description: "true to pause, false to resume."},
				},
				Required: []string{"name", "paused"},
			},
			Handler: func(args map[string]any) (string, error) {
				name := strings.TrimSpace(oArgStr(args, "name"))
				sa, ok := GetStandingAgent(RootDB, owner, name)
				if !ok {
					return "", fmt.Errorf("no standing agent named %q", name)
				}
				sa.Paused = oArgBool(args, "paused")
				if sa.Paused {
					if sa.SchedulerID != "" {
						UnscheduleTask(sa.SchedulerID)
						sa.SchedulerID = ""
						sa.NextRun = time.Time{}
					}
					SaveStandingAgent(RootDB, sa)
					return fmt.Sprintf("Paused %q.", name), nil
				}
				SaveStandingAgent(RootDB, sa)
				if err := ScheduleStandingAgent(RootDB, sa); err != nil {
					return "", err
				}
				got, _ := GetStandingAgent(RootDB, owner, name)
				return fmt.Sprintf("Resumed %q. Next run: %s.", name, got.NextRun.Local().Format("Mon Jan 2 3:04 PM")), nil
			},
		},
		{
			Tool: Tool{
				Name:        "delete_standing_agent",
				Description: "Permanently delete a standing (scheduled) agent and cancel its schedule. Use this for a real removal; set_standing_paused only pauses.",
				Parameters:  map[string]ToolParam{"name": {Type: "string", Description: "The standing agent's name."}},
				Required:    []string{"name"},
			},
			Handler: func(args map[string]any) (string, error) {
				name := strings.TrimSpace(oArgStr(args, "name"))
				if _, ok := GetStandingAgent(RootDB, owner, name); !ok {
					return "", fmt.Errorf("no standing agent named %q", name)
				}
				DeleteStandingAgent(RootDB, owner, name)
				return fmt.Sprintf("Deleted standing agent %q and cancelled its schedule.", name), nil
			},
		},
		{
			Tool: Tool{
				Name:        "list_runs",
				Description: "List recent runs across the fleet (status-level). Each line shows the run id for use with get_run.",
				Parameters: map[string]ToolParam{
					"agent": {Type: "string", Description: "Optional: restrict to one standing agent's name."},
					"limit": {Type: "number", Description: "Optional: max rows (default 15, max 50)."},
				},
			},
			Handler: func(args map[string]any) (string, error) {
				limit := oArgInt(args, "limit")
				if limit <= 0 || limit > 50 {
					limit = 15
				}
				runs := ListRuns(RootDB, owner, RunFilter{Agent: strings.TrimSpace(oArgStr(args, "agent")), Limit: limit})
				if len(runs) == 0 {
					return "No runs recorded yet.", nil
				}
				var b strings.Builder
				for _, rr := range runs {
					sum := rr.Summary
					if len(sum) > 120 {
						sum = sum[:120] + "…"
					}
					fmt.Fprintf(&b, "- [%s] %s %s (%s): %s\n",
						rr.ID, rr.Started.Local().Format("Jan 2 3:04 PM"), rr.Agent, rr.Status, sum)
				}
				return strings.TrimSpace(b.String()), nil
			},
		},
		{
			Tool: Tool{
				Name:        "get_run",
				Description: "Get one run's full detail, including its output. Use a run id from list_runs.",
				Parameters:  map[string]ToolParam{"id": {Type: "string", Description: "The run id."}},
				Required:    []string{"id"},
			},
			Handler: func(args map[string]any) (string, error) {
				rec, ok := GetRun(RootDB, owner, strings.TrimSpace(oArgStr(args, "id")))
				if !ok {
					return "", fmt.Errorf("no run with that id")
				}
				return fmt.Sprintf("Run %s\nagent: %s\nstatus: %s\ntrigger: %s\nbrief: %s\nsummary: %s\noutput:\n%s",
					rec.ID, rec.Agent, rec.Status, rec.Trigger, rec.Brief, rec.Summary, rec.Raw), nil
			},
		},
		{
			Tool: Tool{
				Name:        "create_event_monitor",
				Description: "Set up a monitor that WAKES you when something happens (vs a standing agent, which RUNS on a clock). Kinds: \"webhook\" mints a secret URL an external system POSTs to (chat-join / alert notifier); \"http_poll\" fetches a URL on an interval, extracts a value, and wakes you when it crosses a threshold (BEST for numeric/value conditions like a stock price or uptime — no agent needed, cheap and deterministic, fires once on the crossing); \"poll\" runs a checker agent on an interval for FUZZY conditions a value-compare can't express. On wake you react in this thread (report / delegate).",
				Parameters: map[string]ToolParam{
					"name":             {Type: "string", Description: "Short unique name for this monitor, e.g. \"nvda-below\" or \"ts-join\"."},
					"kind":             {Type: "string", Description: "\"webhook\", \"http_poll\", or \"poll\"."},
					"wake_brief":       {Type: "string", Description: "What you should do when it fires (guides your reaction)."},
					"interval_seconds": {Type: "number", Description: "http_poll/poll: how often to check, in seconds (minimum 30; 900 = every 15 min, 3600 = hourly)."},
					"check_agent":      {Type: "string", Description: "poll only: name/id of an existing agent that checks the condition each interval."},
					"check":            {Type: "string", Description: "poll only: the question/brief given to the checker. Tell it to answer with the match string when the event has happened."},
					"match_contains":   {Type: "string", Description: "poll only: fire when the checker's answer contains this (case-insensitive). Default \"YES\"."},
					"url":              {Type: "string", Description: "http_poll only: URL fetched each interval (e.g. a finance JSON API)."},
					"json_path":        {Type: "string", Description: "http_poll: dotted path into the JSON response, array indices included, e.g. \"quoteResponse.result.0.regularMarketPrice\". Omit json_path and regex to compare the whole body."},
					"regex":            {Type: "string", Description: "http_poll: alternative extraction — first capture group of this regex against the body."},
					"compare_op":       {Type: "string", Description: "http_poll: one of < > <= >= == != contains. Fire when extracted_value <op> threshold is true."},
					"threshold":        {Type: "string", Description: "http_poll: the value compared against (a number for < > <= >=)."},
				},
				Required: []string{"name", "kind"},
			},
			Handler: func(args map[string]any) (string, error) {
				name := strings.TrimSpace(oArgStr(args, "name"))
				kind := strings.ToLower(strings.TrimSpace(oArgStr(args, "kind")))
				if name == "" {
					return "", fmt.Errorf("name is required")
				}
				if kind != EventKindWebhook && kind != EventKindPoll && kind != EventKindHTTP {
					return "", fmt.Errorf("kind must be %q, %q, or %q", EventKindWebhook, EventKindHTTP, EventKindPoll)
				}
				if _, exists := GetEventMonitor(RootDB, owner, name); exists {
					return "", fmt.Errorf("a monitor named %q already exists", name)
				}
				m := EventMonitor{
					Name: name, Owner: owner, Kind: kind,
					WakeBrief: strings.TrimSpace(oArgStr(args, "wake_brief")), Created: time.Now(),
				}
				if kind == EventKindWebhook {
					m.Token = NewEventToken()
					SaveEventMonitor(RootDB, m)
					return fmt.Sprintf("Webhook monitor %q created. Have the external system POST JSON {\"summary\":\"...\"} to:\n  <your gohort base URL>/orchestrate/api/operator/event/%s\nEach POST wakes me in this thread.", name, m.Token), nil
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
					if m.URL == "" || m.CompareOp == "" || m.Threshold == "" {
						return "", fmt.Errorf("http_poll monitors need url, compare_op, and threshold")
					}
					switch m.CompareOp {
					case "<", ">", "<=", ">=", "==", "!=", "contains":
					default:
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
					return fmt.Sprintf("HTTP monitor %q created: every %ds I fetch %s, read %s, and wake you when the value %s %s. Fires once on the crossing (and re-arms after it recovers). Next check: %s.",
						name, got.IntervalSeconds, m.URL, extractDesc, m.CompareOp, m.Threshold,
						got.NextCheck.Local().Format("Mon Jan 2 3:04 PM")), nil
				}
				m.CheckAgent = strings.TrimSpace(oArgStr(args, "check_agent"))
				m.Check = strings.TrimSpace(oArgStr(args, "check"))
				m.MatchContains = strings.TrimSpace(oArgStr(args, "match_contains"))
				m.IntervalSeconds = oArgInt(args, "interval_seconds")
				if m.CheckAgent == "" || m.Check == "" {
					return "", fmt.Errorf("poll monitors need check_agent and check")
				}
				SaveEventMonitor(RootDB, m)
				if err := ScheduleEventMonitor(RootDB, m); err != nil {
					return "", fmt.Errorf("saved but scheduling failed: %w", err)
				}
				match := m.MatchContains
				if match == "" {
					match = "YES"
				}
				got, _ := GetEventMonitor(RootDB, owner, name)
				return fmt.Sprintf("Poll monitor %q created: every %ds, agent %q is asked %q; I wake when the answer contains %q. Next check: %s.",
					name, got.IntervalSeconds, m.CheckAgent, m.Check, match, got.NextCheck.Local().Format("Mon Jan 2 3:04 PM")), nil
			},
		},
		{
			Tool: Tool{
				Name:        "list_phantom_chats",
				Description: "List the user's phantom (iMessage) conversations — contact/handle and chat id — so you can read or message one. Read-only.",
				Parameters:  map[string]ToolParam{"limit": {Type: "number", Description: "Max conversations (default 20)."}},
			},
			Handler: func(args map[string]any) (string, error) {
				link, ok := ActivePhantomLink()
				if !ok {
					return "", fmt.Errorf("the phantom bridge is not available")
				}
				limit := oArgInt(args, "limit")
				if limit <= 0 {
					limit = 20
				}
				chats, err := link.ListChats(owner, limit)
				if err != nil {
					return "", err
				}
				if len(chats) == 0 {
					return "No phantom conversations found.", nil
				}
				var b strings.Builder
				for _, c := range chats {
					name := c.DisplayName
					if name == "" {
						name = c.Handle
					}
					fmt.Fprintf(&b, "- %s (%s) [chat_id: %s]", name, c.Handle, c.ChatID)
					if !c.LastAt.IsZero() {
						fmt.Fprintf(&b, " last %s", c.LastAt.Local().Format("Jan 2 3:04 PM"))
					}
					b.WriteString("\n")
				}
				return strings.TrimSpace(b.String()), nil
			},
		},
		{
			Tool: Tool{
				Name:        "read_phantom_chat",
				Description: "Read recent messages from one phantom (iMessage) conversation. Use a chat_id from list_phantom_chats. Read-only.",
				Parameters: map[string]ToolParam{
					"chat_id": {Type: "string", Description: "The conversation's chat id."},
					"limit":   {Type: "number", Description: "How many recent messages (default 20)."},
				},
				Required: []string{"chat_id"},
			},
			Handler: func(args map[string]any) (string, error) {
				link, ok := ActivePhantomLink()
				if !ok {
					return "", fmt.Errorf("the phantom bridge is not available")
				}
				chatID := strings.TrimSpace(oArgStr(args, "chat_id"))
				if chatID == "" {
					return "", fmt.Errorf("chat_id is required")
				}
				msgs, err := link.ReadChat(owner, chatID, oArgInt(args, "limit"))
				if err != nil {
					return "", err
				}
				if len(msgs) == 0 {
					return "No messages in that conversation (or it isn't yours).", nil
				}
				var b strings.Builder
				for _, m := range msgs {
					who := "them"
					if m.FromMe {
						who = "me"
					}
					fmt.Fprintf(&b, "[%s] %s: %s\n", m.At.Local().Format("Jan 2 3:04 PM"), who, m.Text)
				}
				return strings.TrimSpace(b.String()), nil
			},
		},
		{
			Tool: Tool{
				Name:        "notify_me",
				Description: "Send a text to the USER'S OWN phone via phantom (a notification to yourself / the owner). No approval needed — it only goes to the owner. Use this to push an alert or a monitor result to the user's phone.",
				Parameters:  map[string]ToolParam{"text": {Type: "string", Description: "The message to send to the owner."}},
				Required:    []string{"text"},
			},
			Handler: func(args map[string]any) (string, error) {
				link, ok := ActivePhantomLink()
				if !ok {
					return "", fmt.Errorf("the phantom bridge is not available")
				}
				text := strings.TrimSpace(oArgStr(args, "text"))
				if text == "" {
					return "", fmt.Errorf("text is required")
				}
				self, ok := link.OwnerHandle(owner)
				if !ok {
					return "", fmt.Errorf("no owner phone configured in phantom (set Owner phone in phantom settings)")
				}
				if err := link.SendToHandle(owner, self, text); err != nil {
					return "", err
				}
				return "Sent to your phone.", nil
			},
		},
		{
			Tool: Tool{
				Name:        "message_contact",
				Description: "Send an iMessage to a CONTACT (someone other than the owner). This contacts a real person, so it ALWAYS queues for the user's approval in the Authorizations pane — it does not send immediately. Identify the recipient by handle (phone/email); use list_phantom_chats to find one.",
				Parameters: map[string]ToolParam{
					"handle": {Type: "string", Description: "Recipient phone/email handle."},
					"text":   {Type: "string", Description: "The message to send."},
				},
				Required: []string{"handle", "text"},
			},
			Handler: func(args map[string]any) (string, error) {
				handle := strings.TrimSpace(oArgStr(args, "handle"))
				text := strings.TrimSpace(oArgStr(args, "text"))
				if handle == "" || text == "" {
					return "", fmt.Errorf("handle and text are required")
				}
				a := SaveAuthorization(RootDB, Authorization{
					Owner: owner, Action: "send_message", Handle: handle, Text: text,
				})
				return fmt.Sprintf("Queued a message to %q for the user's approval — it's in the Authorizations pane (id %s) and sends once approved.", handle, a.ID), nil
			},
		},
		{
			Tool: Tool{
				Name:        "list_event_monitors",
				Description: "List the user's event monitors (webhook + poll) with their kind, schedule, paused state, and when each last fired.",
			},
			Handler: func(args map[string]any) (string, error) {
				ms := ListEventMonitors(RootDB, owner)
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
					}
					if !m.LastFired.IsZero() {
						fmt.Fprintf(&b, "; last fired %s", m.LastFired.Local().Format("Jan 2 3:04 PM"))
					}
					b.WriteString("\n")
				}
				return strings.TrimSpace(b.String()), nil
			},
		},
		{
			Tool: Tool{
				Name:        "delete_event_monitor",
				Description: "Delete an event monitor by name (stops its polling / invalidates its webhook).",
				Parameters:  map[string]ToolParam{"name": {Type: "string", Description: "The monitor's name."}},
				Required:    []string{"name"},
			},
			Handler: func(args map[string]any) (string, error) {
				name := strings.TrimSpace(oArgStr(args, "name"))
				if _, ok := GetEventMonitor(RootDB, owner, name); !ok {
					return "", fmt.Errorf("no event monitor named %q", name)
				}
				DeleteEventMonitor(RootDB, owner, name)
				return fmt.Sprintf("Deleted event monitor %q.", name), nil
			},
		},
	}
}

// dropToolsByName removes the named tools from a parallel (tools, names) pair.
// Used to keep the generic interval scheduler ("recurring") off the Operator —
// it schedules through the fleet (create_standing_agent) instead.
func dropToolsByName(tools []AgentToolDef, names []string, drop ...string) ([]AgentToolDef, []string) {
	dropSet := map[string]bool{}
	for _, d := range drop {
		dropSet[d] = true
	}
	outT := make([]AgentToolDef, 0, len(tools))
	for _, td := range tools {
		if !dropSet[td.Tool.Name] {
			outT = append(outT, td)
		}
	}
	outN := make([]string, 0, len(names))
	for _, n := range names {
		if !dropSet[n] {
			outN = append(outN, n)
		}
	}
	return outT, outN
}

// --- arg helpers (o-prefixed to avoid collisions in this package) ------------

func oArgStr(args map[string]any, k string) string {
	if v, ok := args[k].(string); ok {
		return v
	}
	return ""
}

func oArgBool(args map[string]any, k string) bool {
	switch v := args[k].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	}
	return false
}

func oArgInt(args map[string]any, k string) int {
	switch v := args[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}
