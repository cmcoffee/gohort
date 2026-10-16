package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/timing"
)

// The one scheduling tool.
//
// An agent used to be handed eight tools across two families (standing
// agents on a clock; event monitors of four kinds) plus a timer, and the
// first thing it had to do with "tell me at 1:10pm" was pick a family. One
// agent said outright that the pick was confusing, and another got it wrong
// in the way the archetype doc warns about: it built an http_poll against a
// public time API, compared an ISO datetime with >=, and the monitor parked
// itself before 1:10 came.
//
// `schedule` takes the ask in the agent's words. `when` is the decision, in
// six plain options; `then` is what to do; `time` is whatever the user said
// ("1:10pm", "in 20 minutes", "daily 08:00", "every 15 minutes"), parsed
// here rather than by the agent. Everything else has a default. Underneath,
// each `when` calls the handler of the tool it replaced, so records, runners,
// the console and the run ledger are untouched: this is one surface over the
// same machinery, not a merge of the stores (docs/scheduling-unification.md
// says why the stores stay apart).

// scheduleFoldedTools are the operator tools `schedule` replaces. They are
// still built (their handlers are the implementation) and no longer shown to
// the model; their names stay reserved and alias to schedule in allowlists.
var scheduleFoldedTools = map[string]bool{
	"create_standing_agent": true, "list_standing_agents": true, "run_standing_now": true,
	"set_standing_paused": true, "delete_standing_agent": true,
	"create_event_monitor": true, "list_event_monitors": true, "delete_event_monitor": true,
	"set_timer": true,
}

// scheduleWhenAliases map the words an agent may reach for (the retired
// tools' kinds among them) onto the six `when` options.
var scheduleWhenAliases = map[string]string{
	"at": "at", "once": "at", "timer": "at", "delay": "at", "in": "at",
	"every": "every", "recurring": "every", "cron": "every", "interval": "every", "standing": "every", "daily": "every",
	"value_crosses": "value_crosses", "http_poll": "value_crosses", "threshold": "value_crosses", "value": "value_crosses",
	"output_changes": "output_changes", "watch": "output_changes", "changes": "output_changes", "change": "output_changes",
	"posted": "posted", "webhook": "posted", "push": "posted",
	"agent_says": "agent_says", "poll": "agent_says", "check": "agent_says", "checker": "agent_says",
}

// recurringImpl is the agent's own recurring-task path (a prompt re-run as
// this agent in a thread of its own), folded into `schedule` as the
// when="every" that names no other runner. chatTurn implements it; tests
// substitute a recorder.
type recurringImpl interface {
	recurringSchedule(args map[string]any) (string, error)
	recurringList(args map[string]any) (string, error)
	recurringCancel(args map[string]any) (string, error)
	recurringMove(args map[string]any) (string, error)
	recurringTasks() []recurringTaskRow
}

// operatorManagementToolDefs is the operator toolset as the model sees it:
// every unfolded tool except the ones `schedule` replaces, plus `schedule`
// composed from their handlers. rec, when given, folds the agent's own
// recurring tasks into the same tool (an author that keeps them); nil is a
// Fleet agent, which schedules through the fleet only.
func operatorManagementToolDefs(sess *ToolSession, agentID string, rec recurringImpl) []AgentToolDef {
	all := operatorToolDefsUnfolded(sess, agentID)
	impl := map[string]ToolHandlerFunc{}
	out := make([]AgentToolDef, 0, len(all))
	inserted := false
	for _, td := range all {
		if scheduleFoldedTools[td.Tool.Name] {
			impl[td.Tool.Name] = td.Handler
			if !inserted {
				// Sits where create_standing_agent sat, right after delegate.
				out = append(out, AgentToolDef{})
				inserted = true
			}
			continue
		}
		out = append(out, td)
	}
	owner := ""
	if sess != nil {
		owner = sess.Username
	}
	def := scheduleToolDef(sess, owner, agentID, impl, rec)
	if !inserted {
		return append(out, def)
	}
	for i := range out {
		if out[i].Tool.Name == "" {
			out[i] = def
			break
		}
	}
	return out
}

func scheduleToolDef(sess *ToolSession, owner, controllerAgentID string, impl map[string]ToolHandlerFunc, rec recurringImpl) AgentToolDef {
	hasOp, hasRec := len(impl) > 0, rec != nil
	runsAs := "Runs as you unless `agent` names another agent (or `pipeline` / `machine`)."
	if hasRec && !hasOp {
		runsAs = "Runs as you, in a thread of your own (your Cortex when you have one, else here; `to` chooses)."
	} else if hasRec {
		runsAs = "With no `agent` it runs as you, as a recurring task of your own; `agent` / `pipeline` / `machine` hands it to another runner on a real schedule."
	}
	var b strings.Builder
	b.WriteString("Anything that should happen LATER or REPEATEDLY: a reminder, a job on a clock")
	if hasOp {
		b.WriteString(", an alert when something changes")
	}
	b.WriteString(". One tool; `when` is the whole decision:\n")
	b.WriteString("  when=\"at\": ONCE at a time or after a delay. \"tell me at 1:10pm\", \"ping me in 20 minutes\". time=\"1:10pm\" / \"in 20 minutes\". Nothing is fetched or checked: the clock is the trigger.\n")
	b.WriteString("  when=\"every\": REPEATEDLY on a clock, and you hear from it every time. \"every morning summarize the alerts\", \"every 15 minutes fetch the price and tell me\". time=\"daily 08:00\" / \"weekdays 17:00\" / \"every 15 minutes\". " + runsAs + "\n")
	if hasOp {
		b.WriteString("  when=\"value_crosses\": a URL's value crosses a line, silently checked, no LLM. \"tell me when NVDA goes below 150\". url + json_path (or regex) + compare_op + threshold.\n")
		b.WriteString("  when=\"output_changes\": a tool's output changes, silently checked, no LLM until it does. \"tell me when the chat has a new message\", \"when the page changes\". tool_name (+ tool_args).\n")
		b.WriteString("  when=\"posted\": an external system POSTs to a secret URL you are given. No polling.\n")
		b.WriteString("  when=\"agent_says\": an agent judges a fuzzy condition each check (the expensive one; only when no value or hash can capture it). check = the question; it fires when the answer contains match_contains (default YES).\n")
	}
	b.WriteString("`then` is what to do when it fires, in plain words: for every it is the mission run each time; for the rest it is handed to you on wake. Everything else has a default: a name is made up")
	if hasOp {
		b.WriteString(", alerts wake you here (notify=\"channel\"; \"direct\" posts the raw change with no LLM, \"text\" texts the owner's phone; ASK the user which they want for a monitor), checks run at a sensible cadence (check_every=\"30s\" / \"5 minutes\" to change it)")
	}
	b.WriteString(".\nBounds: stop_after=N stops it after N ")
	if hasOp {
		b.WriteString("alerts (\"just once\" = 1; `at` is always once)")
	} else {
		b.WriteString("runs (\"do this 5 times\")")
	}
	b.WriteString(". until=\"...\" makes it an objective judged after each run and stopped when true (max_attempts bounds the tries).\n")
	b.WriteString("Other actions: list (everything scheduled), ")
	if hasOp {
		b.WriteString("pause / resume / run_now / ")
	}
	b.WriteString("delete (by name)")
	if hasRec {
		b.WriteString(", move (a task of your own: to=\"cortex\" / \"session\" / \"background\")")
	}
	b.WriteString(", relink (a schedule parked because the agent it ran as was deleted: name + run_by, the agent replacing it; it keeps its mission, timing, goal and notes, so relink rather than create it again)")
	b.WriteString(", get (name: one scheduled run or monitor with what it does IN FULL; list shortens long ones), edit (name + one of: then, the whole new text; find + replace, one passage changed in place; append, a line added at the end. Changes only WHAT an existing scheduled run or monitor does; its timing, link group and goal stay as they are. To add a rule, append it: never retype a long mission you have only seen shortened)")
	b.WriteString(". Re-issuing create with the SAME name edits that schedule in place, timing included. To run something once right now, don't schedule it: agents(action=\"run\") or machine(action=\"run\"), or do it yourself.")
	if hasRec {
		b.WriteString(" Tell the user which thread a recurring task of yours will report in.")
	}

	params := map[string]ToolParam{
		"action":       {Type: "string", Enum: []string{"create", "list", "get", "edit", "pause", "resume", "run_now", "delete", "relink"}, Description: "create | list | get | edit | pause | resume | run_now | delete | relink."},
		"run_by":       {Type: "string", Description: "(relink) The agent that runs the parked schedule from now on, by name or id: usually the one made to replace the deleted agent."},
		"when":         {Type: "string", Enum: []string{"at", "every", "value_crosses", "output_changes", "posted", "agent_says"}, Description: "(create) The trigger: at (once, at a time) | every (on a clock, report each time) | value_crosses (a URL's value crosses a line) | output_changes (a tool's output changes) | posted (an external POST) | agent_says (an agent judges a condition)."},
		"find":         {Type: "string", Description: "(edit) A passage of what it does now, copied exactly, to change in place with replace. It must appear exactly once."},
		"replace":      {Type: "string", Description: "(edit, with find) What the found passage becomes. Empty removes it."},
		"append":       {Type: "string", Description: "(edit) A line or paragraph added to the end of what it does, e.g. a new rule. The rest is kept as it is."},
		"then":         {Type: "string", Description: "(create, edit) What to do when it fires, in plain words. edit with then replaces the WHOLE of what it does: use find/replace or append for a change to part of it. every: the mission run each time (\"fetch the forecast and post it to the family chat\"). at / monitors: handed to you on wake (\"tell the user it's 1:10pm, as they asked\")."},
		"time":         {Type: "string", Description: "(create) When, as the user said it, in their own zone (the one time_in_zone reports; never convert to UTC). at: \"13:10\", \"1:10pm\", \"in 20 minutes\", or ISO8601. every: \"daily 08:00\", \"weekdays 17:00\", \"FRI 21:30\", \"every 15 minutes\", \"hourly\", set times \"09:00,13:00\", once a month \"monthly on the 28th at 9am\" / \"monthly on the last day at 9am\" / \"monthly on the 2nd to last day at 9am\" (a day a month lacks runs on its last day), or once a year \"DEC-25 09:00\" / \"every year on Dec 25 at 9am\". Monitors: optional set times of day to check, \"08:00\" or \"08:00,18:00\", instead of check_every."},
		"name":         {Type: "string", Description: "(create: optional, one is made up; pause/resume/run_now/delete: required) Short name, e.g. \"daily-weather\", \"nvda-below\"."},
		"agent":        {Type: "string", Description: "(at: who is woken to do it, default you; every: who runs it, default you; agent_says: who checks, default you; list / delete / move: WHOSE schedules to act on, another of your own agents by name or id, or \"all\" for every agent you own, list only) Name or id of an existing agent."},
		"pipeline":     {Type: "string", Description: "(every, instead of agent) A stored pipeline to run; `then` becomes its input."},
		"machine":      {Type: "string", Description: "(every, instead of agent) A stored machine to run; `then` becomes its input."},
		"start_at":     {Type: "string", Description: "(every with an interval, optional) ISO8601 first run, e.g. 2026-06-10T08:00:00-07:00; then the interval from there."},
		"stop_after":   {Type: "number", Description: "(every / monitors, optional) Stop after this many runs (every) or alerts (monitors): \"just once\" = 1, \"do this 5 times\" = 5. Omit to keep going until stopped."},
		"stop_on":      {Type: "string", Description: "(every, optional) The last day it runs, YYYY-MM-DD in the user's zone: \"until the end of November\" = 2026-11-30."},
		"days":         {Type: "string", Description: "(every, optional) Which days it runs: \"weekdays\", \"weekends\", or day names like \"mon,wed,fri\". Omit for every day, or when time already names the days."},
		"until":        {Type: "string", Description: "(every / monitors, optional) Makes it an OBJECTIVE rather than a plain cadence: the goal in plain words that makes it DONE (\"the PR is merged\"). Judged after each run or fire from what it actually did; it stops itself when true."},
		"max_attempts": {Type: "integer", Description: "(with until, optional) How many runs may end with the goal unmet before it stops trying."},
	}
	if hasOp {
		for k, v := range map[string]ToolParam{
			"check_every":    {Type: "string", Description: "(monitors) How often to check: \"30s\", \"5 minutes\", \"1 hour\". Minimum 30s. Default: value_crosses 15 minutes, output_changes 1 minute, agent_says 15 minutes. Match how fast the thing can actually change."},
			"url":            {Type: "string", Description: "(value_crosses) The URL fetched each check, e.g. a finance JSON API."},
			"json_path":      {Type: "string", Description: "(value_crosses) Dotted path into the JSON response, indices included, e.g. \"quoteResponse.result.0.regularMarketPrice\". Omit json_path and regex to compare the whole body."},
			"regex":          {Type: "string", Description: "(value_crosses) Alternative extraction: first capture group against the body."},
			"compare_op":     {Type: "string", Enum: []string{"<", ">", "<=", ">=", "==", "!=", "contains"}, Description: "(value_crosses) Fires when value <op> threshold. < > <= >= need a numeric threshold; a time of day is when=\"at\", not a comparison."},
			"threshold":      {Type: "string", Description: "(value_crosses) The value compared against, as a string: \"150\", \"error\"."},
			"tool_name":      {Type: "string", Description: "(output_changes) The tool run each check; you are woken only when its output changes. read_chat for a chat, fetch_url for a page."},
			"tool_args":      {Type: "object", Description: "(output_changes) Arguments passed to tool_name every check, e.g. {\"chat_id\":\"any;+;chat123\",\"limit\":10}."},
			"format_script":  {Type: "string", Description: "(output_changes, optional) Sandboxed python shaping the alert: reads {\"prior\",\"current\"} JSON on stdin, prints the alert; print \"SKIP\" to drop a change. Empty output falls back to the built-in diff."},
			"check":          {Type: "string", Description: "(agent_says) The question the checking agent answers each check. Tell it to answer the match word only when the event has happened, and a clean NONE otherwise."},
			"match_contains": {Type: "string", Description: "(agent_says) Fires when the answer contains this (case-insensitive). Default \"YES\"."},
			"notify":         {Type: "string", Enum: []string{"channel", "direct", "text"}, Description: "(at / monitors) channel (default): wake you here so you react in your own words. direct: post the change into this thread with NO LLM. text: text the owner's phone, no LLM."},
			"deliver_to":     {Type: "string", Description: "(monitors, optional) A chat_id from list_chats: the alert is posted straight to THAT conversation with no LLM instead of waking you."},
			"bulletin":       {Type: "string", Description: "(monitors, optional) A bulletin board name: each change is posted there with no LLM for every following agent to read."},
			"wake_agent":     {Type: "string", Description: "(monitors, optional) Another agent to wake instead of you, so the alert lands in its thread. Only when wiring a watch for a different agent."},
			"surface":        {Type: "string", Enum: []string{"session", "cortex", "background"}, Description: "(monitors, optional) Where the fire surfaces for the agent. Omit for the default (this session)."},
		} {
			params[k] = v
		}
	} else {
		params["when"] = ToolParam{Type: "string", Enum: []string{"at", "every"}, Description: "(create) The trigger: at (once, at a time or after a delay) | every (on a clock, repeatedly)."}
		params["action"] = ToolParam{Type: "string", Enum: []string{"create", "list", "get", "edit", "delete", "move", "relink"}, Description: "create | list | get | edit | delete | move | relink."}
		params["agent"] = ToolParam{Type: "string", Description: "(list / delete / move, optional) WHOSE schedules to act on: another of your own agents, by name or id, or \"all\" (list only) for every agent you own. Omit for yourself."}
		delete(params, "pipeline")
		delete(params, "machine")
		delete(params, "start_at")
		params["stop_after"] = ToolParam{Type: "number", Description: "(every, optional) Stop after this many runs (\"do this 5 times\"). Omit for the default: it runs until deleted."}
	}
	if hasRec {
		for k, v := range map[string]ToolParam{
			"to":              {Type: "string", Enum: []string{"cortex", "session", "background"}, Description: "(every as you / move) Where a task of your own posts its reports: cortex = your standing mind thread (needs one); session = this conversation, or the one in session_id; background = runs, posts nowhere. Omit for your default (cortex when you have one, else here)."},
			"session_id":      {Type: "string", Description: "(move, optional, with to=\"session\") A specific session of yours to report into, e.g. one from open_session."},
			"random":          {Type: "boolean", Description: "(every, optional) Fire at RANDOM moments instead of a fixed cadence: random gaps between min_gap_minutes and max_gap_minutes, or times_per_day moments a day inside the active_from/active_to window."},
			"times_per_day":   {Type: "integer", Description: "(every, random) Random fires per day inside the window (1-96)."},
			"min_gap_minutes": {Type: "integer", Description: "(every, random) Minimum minutes between fires."},
			"max_gap_minutes": {Type: "integer", Description: "(every, random) Maximum minutes between fires."},
			"active_from":     {Type: "string", Description: "(every at an interval or random, optional) Daily window start, HH:MM local, with active_to; runs that would land outside it wait for the next window."},
			"active_to":       {Type: "string", Description: "(every at an interval or random, optional) Daily window end, HH:MM local."},
		} {
			params[k] = v
		}
		if hasOp {
			params["action"] = ToolParam{Type: "string", Enum: []string{"create", "list", "get", "edit", "pause", "resume", "run_now", "delete", "move", "relink"}, Description: "create | list | get | edit | pause | resume | run_now | delete | move | relink."}
		}
	}
	tool := Tool{Name: "schedule", Description: b.String(), Parameters: params, Required: []string{"action"}}
	if hasRec {
		tool.Caps = []Capability{CapRead, CapWrite}
	}
	return AgentToolDef{
		Tool: tool,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			// A schedule set up here fires later as the owner's: someone else
			// on a channel cannot leave one behind.
			if nonOwnerRequester(ctx) {
				return "Not done: schedule is the owner's to use, and this request came from someone else on a channel. Tell them it needs the owner, who can do it themselves or ask you directly.", nil
			}
			action := strings.ToLower(strings.TrimSpace(oArgStr(args, "action")))
			name := strings.TrimSpace(oArgStr(args, "name"))
			switch action {
			case "create", "", "set", "add", "new", "schedule":
				out, err := scheduleCreate(ctx, owner, controllerAgentID, impl, rec, args)
				if err == nil && name != "" {
					sessionApprove(sess, "schedule", name, "", ChangeNew) // made here: this session's own
				}
				return out, err
			case "list":
				return scheduleList(ctx, owner, controllerAgentID, impl, rec, args)
			case "get", "show", "read":
				if name == "" {
					return "", fmt.Errorf("get needs name: the schedule to read in full (schedule(action=\"list\") shows them)")
				}
				return scheduleGet(owner, controllerAgentID, hasOp, name)
			case "edit", "update", "change":
				if name == "" {
					return "", fmt.Errorf("edit needs name: the schedule to change (schedule(action=\"list\") shows them and what each does)")
				}
				change := missionChange{Then: strings.TrimSpace(oArgStr(args, "then")), Find: oArgStr(args, "find"),
					Replace: oArgStr(args, "replace"), Append: strings.TrimSpace(oArgStr(args, "append"))}
				if err := change.valid(); err != nil {
					return "", err
				}
				if msg := scheduleChangeApproval(sess, owner, controllerAgentID, "edit", name, args); msg != "" {
					return msg, nil
				}
				return scheduleEdit(owner, controllerAgentID, hasOp, name, change)
			case "relink":
				if name == "" {
					return "", fmt.Errorf("relink needs name: the parked schedule (schedule(action=\"list\", agent=\"all\") marks them \"needs relink\")")
				}
				if msg := scheduleChangeApproval(sess, owner, controllerAgentID, action, name, args); msg != "" {
					return msg, nil
				}
				return scheduleRelink(owner, name, oArgStr(args, "run_by"))
			case "pause", "resume", "run_now", "run", "delete", "cancel", "remove", "move":
				if name == "" {
					return "", fmt.Errorf("%s needs the name of what to %s: schedule(action=\"list\") shows them", action, action)
				}
				if msg := scheduleChangeApproval(sess, owner, controllerAgentID, action, name, args); msg != "" {
					return msg, nil
				}
				return scheduleControl(ctx, owner, controllerAgentID, impl, rec, action, name, args)
			}
			return "", fmt.Errorf("unknown action %q: use create | list | get | edit | pause | resume | run_now | delete | relink", action)
		},
	}
}

// scheduleCreate turns (when, then, time, ...) into the call the replaced
// tool took, and makes it.
func scheduleCreate(ctx context.Context, owner, controllerAgentID string, impl map[string]ToolHandlerFunc, rec recurringImpl, args map[string]any) (string, error) {
	when, ok := scheduleWhenAliases[strings.ToLower(strings.TrimSpace(oArgStr(args, "when")))]
	if !ok {
		return "", fmt.Errorf("when is required: at (once, at a time) | every (on a clock) | value_crosses (a URL's value) | output_changes (a tool's output) | posted (an external POST) | agent_says (an agent judges it)")
	}
	then := strings.TrimSpace(oArgStr(args, "then"))
	name := strings.TrimSpace(oArgStr(args, "name"))
	timeSpec := strings.TrimSpace(oArgStr(args, "time"))
	if timeSpec == "" {
		timeSpec = strings.TrimSpace(oArgStr(args, "at"))
	}
	if when != "at" && name == "" {
		name = scheduleAutoName(when, then, timeSpec)
	}
	forward := func(tool string, m map[string]any) (string, error) {
		h, ok := impl[tool]
		if !ok {
			return "", fmt.Errorf("this agent cannot set that up: it schedules only its own work (when=\"at\" or when=\"every\" with no agent named)")
		}
		return h(ctx, m)
	}
	// Optional fields pass through under the names the handlers read.
	pass := func(m map[string]any, pairs ...string) {
		for i := 0; i+1 < len(pairs); i += 2 {
			if v, ok := args[pairs[i]]; ok && v != nil && v != "" {
				m[pairs[i+1]] = v
			}
		}
	}
	switch when {
	case "at":
		if timeSpec == "" {
			return "", fmt.Errorf("when=\"at\" needs time: \"13:10\", \"1:10pm\", \"in 20 minutes\", or ISO8601")
		}
		if then == "" {
			return "", fmt.Errorf("when=\"at\" needs then: what to do when it goes off, e.g. \"tell the user it's 1:10pm\"")
		}
		if _, ok := impl["set_timer"]; !ok && rec != nil {
			return scheduleOwnTimer(owner, rec, timeSpec, then, name, args)
		}
		m := map[string]any{"at": timeSpec, "note": then}
		pass(m, "name", "name", "notify", "notify", "wake_agent", "wake_agent")
		// agent names who is woken to do it, as it names who runs an every:
		// "run QA Radar once in 4 minutes" is agent="QA Radar", when="at".
		// Without this the one-off woke its author, and a Builder asked for
		// a one-off run of the agent it had just built set a timer on
		// itself instead.
		if a := strings.TrimSpace(oArgStr(args, "agent")); a != "" {
			if _, named := m["wake_agent"]; !named {
				m["wake_agent"] = a
			}
		}
		return forward("set_timer", m)
	case "every":
		random := oArgBool(args, "random")
		if timeSpec == "" && !random {
			return "", fmt.Errorf("when=\"every\" needs time: \"daily 08:00\", \"weekdays 17:00\", \"FRI 21:30\", \"every 15 minutes\", or \"hourly\"")
		}
		var cron string
		var interval int
		var times []int
		if mm := timesListRe.FindStringSubmatch(strings.TrimSpace(timeSpec)); mm != nil {
			// Set times: "09:00,13:00" (or "daily 09:00,13:00").
			mins, err := parseDailyAt(mm[1])
			if err != nil {
				return "", err
			}
			times = mins
		} else if timeSpec != "" {
			var err error
			if cron, interval, err = parseEverySpec(timeSpec); err != nil {
				return "", err
			}
		}
		// The same timing every Scheduler dialog edits (core/timing): days,
		// how often, a window, and the stops. Passed down beside the old
		// fields, which callers that predate it still read.
		spec, specOK, err := everyTiming(owner, args, cron, interval, times)
		if err != nil {
			return "", err
		}
		var specArg map[string]any
		if specOK {
			b, _ := json.Marshal(spec)
			_ = json.Unmarshal(b, &specArg)
			args["timing"] = specArg
		}
		if then == "" {
			return "", fmt.Errorf("when=\"every\" needs then: the mission run each time, e.g. \"fetch the forecast and post it to the family chat\"")
		}
		namedRunner := strings.TrimSpace(oArgStr(args, "agent")) != "" || strings.TrimSpace(oArgStr(args, "pipeline")) != "" || strings.TrimSpace(oArgStr(args, "machine")) != ""
		if rec != nil && !namedRunner {
			out, err := scheduleOwnRecurring(rec, cron, interval, then, name, args)
			// A weekday pattern the recurring path cannot express falls
			// through to a fleet schedule on the asking agent, where one
			// exists.
			if err == nil || len(impl) == 0 || !strings.Contains(err.Error(), "weekday pattern") {
				return out, err
			}
		}
		m := map[string]any{"name": name, "mission": then}
		if specArg != nil {
			// A scheduled agent's stops ride its timing: stop_after is a run
			// count, stop_on a last day.
			if v := oArgInt(args, "stop_after"); v > 0 {
				specArg["max_runs"] = v
			}
			m["timing"] = specArg
		}
		switch {
		case specArg != nil && (len(times) > 0 || (random && oArgInt(args, "times_per_day") > 0)):
			// Set times and N a day at random have no old form for a
			// scheduled agent; the timing is the whole answer.
		case random:
			lo, hi, err := standingRandomGaps(args, interval)
			if err != nil {
				return "", err
			}
			m["interval_seconds"], m["max_interval_seconds"] = lo, hi
		case cron != "":
			m["cron"] = cron
		default:
			m["interval_seconds"] = interval
		}
		pass(m, "pipeline", "pipeline_id", "machine", "machine_id", "start_at", "start_at", "until", "until", "max_attempts", "max_attempts",
			"active_from", "active_from", "active_to", "active_to")
		if _, named := m["pipeline_id"]; !named {
			if _, named := m["machine_id"]; !named {
				agent := strings.TrimSpace(oArgStr(args, "agent"))
				if agent == "" {
					agent = controllerAgentID
				}
				m["agent_id"] = agent
			}
		}
		if v := oArgInt(args, "stop_after"); v > 0 && specArg == nil {
			return "", fmt.Errorf("an annual date has no run count: give it until (what makes it done) with max_attempts, or delete it when it has done its job")
		}
		return forward("create_standing_agent", m)
	}
	// The four monitor kinds.
	kind := map[string]string{"value_crosses": EventKindHTTP, "output_changes": EventKindWatch, "posted": EventKindWebhook, "agent_says": EventKindPoll}[when]
	m := map[string]any{"name": name, "kind": kind, "wake_brief": then}
	pass(m, "notify", "notify", "deliver_to", "deliver_to", "bulletin", "bulletin", "wake_agent", "wake_agent", "surface", "surface",
		"stop_after", "stop_after", "until", "until",
		"url", "url", "json_path", "json_path", "regex", "regex", "compare_op", "compare_op", "threshold", "threshold",
		"tool_name", "tool_name", "tool_args", "tool_args", "format_script", "format_script",
		"check", "check", "match_contains", "match_contains")
	if when == "agent_says" {
		if agent := strings.TrimSpace(oArgStr(args, "agent")); agent != "" {
			m["check_agent"] = agent
		} else {
			m["check_agent"] = controllerAgentID
		}
	}
	if ce := strings.TrimSpace(oArgStr(args, "check_every")); ce != "" {
		secs, err := parseCheckEvery(ce)
		if err != nil {
			return "", err
		}
		m["interval_seconds"] = secs
	} else if v := oArgInt(args, "interval_seconds"); v > 0 {
		m["interval_seconds"] = v
	}
	if timeSpec != "" {
		m["daily_at"] = timeSpec
	}
	switch when {
	case "value_crosses":
		if strings.TrimSpace(oArgStr(args, "url")) == "" {
			return "", fmt.Errorf("when=\"value_crosses\" needs url (what to fetch), compare_op and threshold (the line it crosses), and json_path or regex (where the value is)")
		}
	case "output_changes":
		if strings.TrimSpace(oArgStr(args, "tool_name")) == "" {
			return "", fmt.Errorf("when=\"output_changes\" needs tool_name: the tool whose output is watched (read_chat for a chat, fetch_url for a page), plus its tool_args")
		}
	case "agent_says":
		if strings.TrimSpace(oArgStr(args, "check")) == "" {
			return "", fmt.Errorf("when=\"agent_says\" needs check: the question answered each check, answering the match word only when it has happened")
		}
	}
	return forward("create_event_monitor", m)
}

// scheduleList is every scheduled thing in one answer: the clock schedules
// and the monitors, each under its own heading, so the agent never has to
// know which store holds what it is asked about.
func scheduleList(ctx context.Context, owner, controllerAgentID string, impl map[string]ToolHandlerFunc, rec recurringImpl, args map[string]any) (string, error) {
	var parts []string
	// Scheduled runs that run AS this agent (or the agent named), set up by
	// the owner or another agent. Listed here for every agent, not only one
	// with the fleet tools: an agent asked to change "its" scheduled task
	// otherwise saw "Nothing is scheduled" while introspect named it, and had
	// no way to read what it does or change it.
	if _, fleet := impl["list_standing_agents"]; !fleet {
		if out := scheduledRunsListing(owner, controllerAgentID, args); out != "" {
			parts = append(parts, "SCHEDULED RUNS THAT RUN AS YOU (change what one does with action=\"edit\")\n"+out)
		}
	}
	if rec != nil {
		out, err := rec.recurringList(args)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(out) != "" && !strings.HasPrefix(strings.TrimSpace(out), "(no ") && !strings.Contains(out, "no recurring tasks") && !strings.Contains(out, "\"tasks\": []") && !strings.Contains(out, "\"tasks\":[]") {
			parts = append(parts, "YOUR OWN RECURRING TASKS (when=every, run by you)\n"+strings.TrimSpace(out))
		}
	}
	for _, src := range []struct{ tool, heading, empty string }{
		{"list_standing_agents", "ON A CLOCK (when=every)", "No standing agents are set up yet."},
		{"list_event_monitors", "WAITING FOR SOMETHING (when=at / value_crosses / output_changes / posted / agent_says)", "No event monitors are set up."},
	} {
		h, ok := impl[src.tool]
		if !ok {
			continue
		}
		out, err := h(ctx, args)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(out) == src.empty {
			continue
		}
		parts = append(parts, src.heading+"\n"+strings.TrimSpace(out))
	}
	if len(parts) == 0 {
		return "Nothing is scheduled.", nil
	}
	return strings.Join(parts, "\n\n"), nil
}

// scheduleControl applies pause / resume / run_now / delete to whatever the
// name is: a standing agent, or a monitor woken by this agent. A name held
// by both is refused rather than guessed, with both named so the agent can
// delete one and retry.
func scheduleControl(ctx context.Context, owner, controllerAgentID string, impl map[string]ToolHandlerFunc, rec recurringImpl, action, name string, args map[string]any) (string, error) {
	_, isStanding := GetStandingAgent(RootDB, owner, name)
	mon, isMonitor := GetEventMonitor(RootDB, owner, name)
	isMonitor = isMonitor && mon.WakeAgent == controllerAgentID
	if len(impl) == 0 {
		isStanding, isMonitor = false, false
	}
	ownTaskID := ""
	if rec != nil && strings.TrimSpace(oArgStr(args, "agent")) != "" {
		// Another agent's task, by its id or by its name among that agent's
		// tasks. A name used to be read as an id here, so "pause the task
		// called X on that agent" could only ever answer "no task X".
		agentKey := strings.TrimSpace(oArgStr(args, "agent"))
		id := name
		if hits := recurringOfAgent(owner, agentKey, userRecurringMatches(owner, name)); len(hits) == 1 {
			id = hits[0].TaskID
		} else if len(hits) > 1 {
			return "", fmt.Errorf("%q names %d recurring tasks on that agent (%s): pass one id as name", name, len(hits), recurringIDs(hits))
		}
		m := map[string]any{"id": id, "agent": agentKey}
		switch action {
		case "delete", "cancel", "remove":
			// Scoped to the agent named, unless it no longer exists: a deleted
			// agent does not resolve, so its tasks are found across every
			// task the person owns instead, by the id.
			scope := agentKey
			if orchestrateBaseDB != nil {
				if _, ok := findAgentByNameOrID(UserDB(orchestrateBaseDB, owner), owner, agentKey); !ok {
					scope = "all"
				}
			}
			return rec.recurringCancel(map[string]any{"id": id, "agent": scope})
		}
		switch action {
		case "delete", "cancel", "remove":
			return rec.recurringCancel(m)
		case "move":
			for _, k := range []string{"to", "session_id"} {
				if v := strings.TrimSpace(oArgStr(args, k)); v != "" {
					m[k] = v
				}
			}
			return rec.recurringMove(m)
		}
		return "", fmt.Errorf("a recurring task cannot be %sd, only deleted or moved: schedule(action=\"delete\", name=%q)", action, id)
	}
	if rec != nil {
		for _, rt := range rec.recurringTasks() {
			if rt.TaskID == name || strings.EqualFold(recurringName(rt.Payload), name) {
				ownTaskID = rt.TaskID
				break
			}
		}
	}
	// Every recurring task the person owns, not only this agent's: the
	// listing shows them all, and a name or id from it has to reach them. It
	// also lets the clash below be seen. A standing agent created under an
	// old recurring task's name matched alone, so deleting by name took the
	// new standing agent and left the old task running.
	var others []recurringTaskRow
	if rec != nil && ownTaskID == "" {
		others = userRecurringMatches(owner, name)
	}
	found := len(others)
	for _, f := range []bool{isStanding, isMonitor, ownTaskID != ""} {
		if f {
			found++
		}
	}
	switch {
	case found > 1:
		msg := fmt.Sprintf("%q names more than one scheduled thing", name)
		if len(others) > 0 {
			msg += fmt.Sprintf(", including recurring task(s) %s: delete a recurring task by its id first (name=\"<id>\"), and the name then means only what is left", recurringIDs(others))
		} else {
			msg += "; schedule(action=\"list\") shows them, delete one first"
		}
		return "", fmt.Errorf("%s", msg)
	case found == 0:
		return "", fmt.Errorf("nothing scheduled is named %q: schedule(action=\"list\") shows what there is", name)
	}
	if len(others) == 1 {
		switch action {
		case "delete", "cancel", "remove":
			return rec.recurringCancel(map[string]any{"id": others[0].TaskID, "agent": "all"})
		}
		return "", fmt.Errorf("a recurring task cannot be %sd, only deleted or moved: schedule(action=\"delete\", name=%q)", action, others[0].TaskID)
	}
	if ownTaskID != "" {
		switch action {
		case "delete", "cancel", "remove":
			return rec.recurringCancel(map[string]any{"id": ownTaskID})
		case "move":
			m := map[string]any{"id": ownTaskID}
			for _, k := range []string{"to", "session_id"} {
				if v := strings.TrimSpace(oArgStr(args, k)); v != "" {
					m[k] = v
				}
			}
			return rec.recurringMove(m)
		}
		return "", fmt.Errorf("a recurring task of your own has no %s: delete %q and create it again when it should run, or move it", action, name)
	}
	if action == "move" {
		return "", fmt.Errorf("only a recurring task of your own moves; %q is relocated from the console's Move-to control", name)
	}
	call := func(tool string, m map[string]any) (string, error) {
		h, ok := impl[tool]
		if !ok {
			return "", fmt.Errorf("%s is not available here", tool)
		}
		return h(ctx, m)
	}
	if isStanding {
		switch action {
		case "pause":
			return call("set_standing_paused", map[string]any{"name": name, "paused": true})
		case "resume":
			return call("set_standing_paused", map[string]any{"name": name, "paused": false})
		case "run_now", "run":
			return call("run_standing_now", map[string]any{"name": name})
		default:
			return call("delete_standing_agent", map[string]any{"name": name})
		}
	}
	switch action {
	case "pause":
		if !StopEventMonitor(RootDB, owner, name, MonitorStopOwner, "Paused by "+controllerAgentID+" at the owner's request.") {
			return fmt.Sprintf("%q is already paused.", name), nil
		}
		return fmt.Sprintf("Paused %q. Resume it to start watching again.", name), nil
	case "resume":
		if !mon.Paused {
			return fmt.Sprintf("%q is already running.", name), nil
		}
		if mon.Broken {
			if reason := eventMonitorDependencyError(mon); reason != "" {
				return "", fmt.Errorf("can't resume %q: %s; fix that or delete it", name, reason)
			}
			mon.Broken, mon.BrokenReason = false, ""
		}
		RearmMonitorFires(&mon)
		mon.Paused, mon.StopReason = false, ""
		SaveEventMonitor(RootDB, mon)
		if err := ScheduleEventMonitor(RootDB, mon); err != nil {
			return "", err
		}
		if cur, ok := GetEventMonitor(RootDB, owner, name); ok && !cur.NextCheck.IsZero() {
			return fmt.Sprintf("Resumed %q. Next check: %s.", name, cur.NextCheck.In(UserLocation(owner)).Format("Mon Jan 2 3:04 PM")), nil
		}
		return fmt.Sprintf("Resumed %q.", name), nil
	case "run_now", "run":
		if err := RunEventMonitorCheck(ctx, RootDB, owner, name); err != nil {
			return "", err
		}
		return fmt.Sprintf("Checked %q now; if its condition held it fired. Its schedule is unchanged.", name), nil
	default:
		return call("delete_event_monitor", map[string]any{"name": name})
	}
}

// scheduleOwnTimer is when="at" for an agent with no fleet timer: a recurring
// task of its own that fires once after the delay. The minute floor is the
// recurring scheduler's.
func scheduleOwnTimer(owner string, rec recurringImpl, timeSpec, then, name string, args map[string]any) (string, error) {
	now := time.Now().In(UserLocation(owner))
	at, err := parseTimerAt(timeSpec, now)
	if err != nil {
		return "", err
	}
	mins := int((at.Sub(now) + time.Minute - 1) / time.Minute)
	if mins < 1 {
		mins = 1
	}
	if name == "" {
		name = "timer-" + at.Format("0304pm")
	}
	m := map[string]any{"prompt": then, "name": name, "pattern": RecurringFixed, "interval_minutes": mins, "max_fires": 1}
	if v := strings.TrimSpace(oArgStr(args, "to")); v != "" {
		m["to"] = v
	}
	out, err := rec.recurringSchedule(m)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Timer %q set for %s (%s from now), once: %s.\n%s", name, at.Format("Mon Jan 2 3:04 PM MST"), humanUntil(at.Sub(now)), then, out), nil
}

// standingRandomGaps reads a random cadence for a scheduled agent: the gap
// between runs is drawn between min_gap_minutes and max_gap_minutes each time.
// A missing minimum falls back to the interval in time, if it gave one. A
// scheduled agent has no "N times a day inside a window" shape; that is a task
// of the agent's own.
func standingRandomGaps(args map[string]any, interval int) (lo, hi int, err error) {
	if oArgInt(args, "times_per_day") > 0 {
		return 0, 0, fmt.Errorf("a scheduled agent fires at random gaps (min_gap_minutes to max_gap_minutes), not a number of times a day: give the gaps instead")
	}
	lo, hi = oArgInt(args, "min_gap_minutes")*60, oArgInt(args, "max_gap_minutes")*60
	if lo <= 0 {
		lo = interval
	}
	if lo <= 0 || hi <= lo {
		return 0, 0, fmt.Errorf("random needs min_gap_minutes and a larger max_gap_minutes, e.g. 30 and 60 for a run every 30-60 minutes at random")
	}
	return lo, hi, nil
}

// scheduleOwnRecurring is when="every" with no other runner named, for an
// agent that keeps its own recurring tasks: the parsed cadence becomes the
// recurring tool's pattern, and stop_after is its run cap.
func scheduleOwnRecurring(rec recurringImpl, cron string, interval int, then, name string, args map[string]any) (string, error) {
	m := map[string]any{"prompt": then, "name": name}
	// The shared timing everyTiming built, when it could: the recurring tool
	// takes it as the whole answer, and the pattern fields below stay for
	// callers that read them.
	if t, ok := args["timing"]; ok && t != nil {
		m["timing"] = t
	}
	switch {
	case oArgBool(args, "random"):
		m["pattern"] = RecurringRandom
	case cron == "":
		mins := (interval + 59) / 60
		if mins < 1 {
			mins = 1
		}
		m["pattern"] = RecurringFixed
		m["interval_minutes"] = mins
	case strings.HasPrefix(cron, "daily "):
		m["pattern"] = RecurringDaily
		m["daily_at"] = strings.TrimPrefix(cron, "daily ")
	case strings.HasPrefix(cron, "MONTHLY-") && m["timing"] != nil:
		// Once a month has no pattern of its own: the timing is the whole
		// answer, and the pattern fields only say what time of day it is.
		m["pattern"] = RecurringDaily
		m["daily_at"] = cron[strings.LastIndex(cron, " ")+1:]
	default:
		return "", fmt.Errorf("a task of your own repeats at an interval or daily at set times; %q is a weekday pattern it cannot keep. Say so, or offer the daily version", cron)
	}
	for _, k := range []string{"to", "until", "active_from", "active_to"} {
		if v := strings.TrimSpace(oArgStr(args, k)); v != "" {
			m[k] = v
		}
	}
	for _, k := range []string{"times_per_day", "min_gap_minutes", "max_gap_minutes", "max_attempts"} {
		if v := oArgInt(args, k); v > 0 {
			m[k] = v
		}
	}
	if v := oArgInt(args, "stop_after"); v > 0 {
		m["max_fires"] = v
	}
	return rec.recurringSchedule(m)
}

var (
	// "monthly on the 28th at 9am", "every month on the last day at 09:00",
	// "the 2nd to last day of each month at 9", "28th of every month 09:00",
	// "28th 09:00": a day of the month in words, read into the MONTHLY form.
	// Groups: 1 every/each month, 2 monthly, 3 last, 4 the day's number, 5
	// its ordinal ending, 6 "to last", 7 "of every month", 8 the time.
	everyMonthlyRe = regexp.MustCompile(`(?i)^(?:(?:every|each)\s+(month)\s+|(monthly)\s+)?(?:on\s+)?(?:the\s+)?(?:(last)|(\d{1,2})(st|nd|rd|th)?(?:[\s-]+(to[\s-]+last))?)(?:\s+day)?(\s+of\s+(?:every|each|the)\s+month)?,?\s+(?:at\s+)?(.+)$`)
	// "every year on Dec 25 at 9am", "yearly on December 25th 09:00",
	// "Dec 25 at 9am": a date in words, read into the MON-DD form.
	everyYearlyRe   = regexp.MustCompile(`(?i)^(?:(?:every|each)\s+year\s+on\s+|(?:yearly|annually)\s+on\s+|(?:yearly|annually)\s+|on\s+)?([a-z]+)\.?\s+(\d{1,2})(?:st|nd|rd|th)?,?\s+(?:at\s+)?(.+)$`)
	everyIntervalRe = regexp.MustCompile(`(?i)^(?:every\s+)?(\d+(?:\.\d+)?)?\s*(hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s)$`)
	everyDailyRe    = regexp.MustCompile(`(?i)^(?:every\s+)?(weekdays?|weekends?|everyday|daily|day|morning|afternoon|evening|night|[a-z]{3}-\d{1,2}|[a-z]{3}(?:,[a-z]{3})*)s?\s+(?:at\s+)?(.+)$`)
)

// parseEverySpec turns "every 15 minutes" / "hourly" into an interval and
// "daily 08:00" / "every day at 8am" / "weekdays 17:00" / "FRI 21:30" into
// the cron form NextCronOccurrence reads. One field, the user's words, and
// the agent never chooses between cron and interval_seconds.
func parseEverySpec(s string) (cron string, interval int, err error) {
	s = strings.TrimSpace(s)
	low := strings.ToLower(s)
	switch low {
	case "hourly", "every hour":
		return "", 3600, nil
	case "every minute", "minutely":
		return "", 60, nil
	}
	if mm := everyIntervalRe.FindStringSubmatch(low); mm != nil {
		n := 1.0
		if mm[1] != "" {
			n, _ = strconv.ParseFloat(mm[1], 64)
		}
		var unit time.Duration
		switch mm[2][0] {
		case 'h':
			unit = time.Hour
		case 'm':
			unit = time.Minute
		default:
			unit = time.Second
		}
		secs := int(n * float64(unit) / float64(time.Second))
		if secs <= 0 {
			return "", 0, fmt.Errorf("could not read %q as an interval", s)
		}
		return "", secs, nil
	}
	if spec, ok := monthlySpec(s); ok {
		if _, ok := timing.FromCron(spec); !ok {
			return "", 0, fmt.Errorf("could not read %q as a day of the month: use the 1st to the 31st (a day a month does not have runs on its last day), the last day, or up to the 7th to last day", s)
		}
		return spec, 0, nil
	}
	if mm := everyYearlyRe.FindStringSubmatch(s); mm != nil {
		if mon := yearMonth(mm[1]); mon != "" {
			clock, cerr := clockHHMM(strings.TrimSpace(mm[3]))
			if cerr != nil {
				return "", 0, cerr
			}
			day, _ := strconv.Atoi(mm[2])
			spec := fmt.Sprintf("%s-%d %s", mon, day, clock)
			if _, ok := timing.FromCron(spec); !ok {
				return "", 0, fmt.Errorf("%s %d is not a date", mm[1], day)
			}
			return spec, 0, nil
		}
	}
	if mm := everyDailyRe.FindStringSubmatch(s); mm != nil {
		days := strings.ToLower(mm[1])
		at := strings.TrimSpace(mm[2])
		switch days {
		case "day", "everyday":
			days = "daily"
		case "weekday":
			days = "weekdays"
		case "weekend":
			days = "weekends"
		case "morning", "afternoon", "evening", "night":
			// "every morning at 8" says am; "every evening at 6" says pm.
			if m2 := timerClockRe.FindStringSubmatch(at); m2 != nil && m2[3] == "" {
				if days == "morning" {
					at += "am"
				} else {
					at += "pm"
				}
			}
			days = "daily"
		}
		clock, cerr := clockHHMM(at)
		if cerr != nil {
			return "", 0, cerr
		}
		spec := strings.ToUpper(days) + " " + clock
		if days == "daily" || days == "weekdays" || days == "weekends" {
			spec = days + " " + clock
		}
		if _, cerr := NextCronOccurrence(spec, time.Now()); cerr != nil {
			return "", 0, fmt.Errorf("could not read %q as a schedule: %v. Use \"daily 08:00\", \"weekdays 17:00\", \"FRI 21:30\", or \"every 15 minutes\"", s, cerr)
		}
		return spec, 0, nil
	}
	// A bare clock time means every day at that time.
	switch low {
	case "noon", "midday", "every day at noon", "daily at noon":
		return "daily 12:00", 0, nil
	case "midnight", "every day at midnight":
		return "daily 00:00", 0, nil
	}
	if clock, cerr := clockHHMM(s); cerr == nil {
		return "daily " + clock, 0, nil
	}
	return "", 0, fmt.Errorf("could not read %q as a schedule: use \"daily 08:00\", \"weekdays 17:00\", \"FRI 21:30\", \"every 15 minutes\", \"hourly\", once a month \"monthly on the 28th at 9am\" or \"monthly on the last day at 9am\", or once a year \"DEC-25 09:00\"", s)
}

// monthlySpec reads a day of the month in words (everyMonthlyRe) into the
// MONTHLY form timing.FromCron takes, or the form itself ("MONTHLY-28
// 09:00"). Only words that say "month" somewhere, or an ordinal day ("28th"),
// read as monthly: "28 09:00" alone is not. ok with a spec FromCron refuses
// means it was meant as monthly but the day is out of range.
func monthlySpec(s string) (string, bool) {
	if f := strings.Fields(s); len(f) == 2 && strings.HasPrefix(strings.ToUpper(f[0]), "MONTHLY-") {
		return strings.ToUpper(f[0]) + " " + f[1], true
	}
	mm := everyMonthlyRe.FindStringSubmatch(s)
	if mm == nil {
		return "", false
	}
	saysMonth := mm[1] != "" || mm[2] != "" || mm[7] != ""
	if !saysMonth && (mm[3] != "" || mm[5] == "") {
		return "", false
	}
	clock, err := clockHHMM(strings.TrimSpace(mm[8]))
	if err != nil {
		return "", false
	}
	at, _ := time.Parse("15:04", clock)
	day := -1
	if mm[3] == "" {
		day, _ = strconv.Atoi(mm[4])
		if mm[6] != "" {
			day = -day
		}
	}
	if day == 0 {
		return "MONTHLY-0 " + clock, true
	}
	return timing.MonthlyCron(day, at.Hour()*60+at.Minute()), true
}

// yearMonth is a month name or its three-letter form as the MON the yearly
// form takes ("december", "Dec" -> "DEC"), "" for anything else, a weekday
// included.
func yearMonth(word string) string {
	w := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(word), "."))
	for m := time.January; m <= time.December; m++ {
		full := strings.ToLower(m.String())
		if w == full || w == full[:3] || (w == "sept" && m == time.September) {
			return strings.ToUpper(full[:3])
		}
	}
	return ""
}

// clockHHMM reads "08:00", "8am", "5:30 PM" as 24-hour HH:MM.
func clockHHMM(s string) (string, error) {
	mm := timerClockRe.FindStringSubmatch(strings.TrimSpace(s))
	if mm == nil {
		return "", fmt.Errorf("%q is not a time of day", s)
	}
	h, _ := strconv.Atoi(mm[1])
	min := 0
	if mm[2] != "" {
		min, _ = strconv.Atoi(mm[2])
	}
	ampm := strings.ToLower(strings.ReplaceAll(mm[3], ".", ""))
	if min > 59 || h > 23 || (ampm != "" && (h == 0 || h > 12)) || (ampm == "" && mm[2] == "") {
		return "", fmt.Errorf("%q is not a time of day", s)
	}
	switch {
	case ampm == "pm" && h < 12:
		h += 12
	case ampm == "am" && h == 12:
		h = 0
	}
	return fmt.Sprintf("%02d:%02d", h, min), nil
}

// parseCheckEvery reads "30s", "5 minutes", "1 hour", or a bare number of
// seconds.
func parseCheckEvery(s string) (int, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n, nil
	}
	if d, err := parseTimerDelay(s); err == nil {
		return int(d / time.Second), nil
	}
	return 0, fmt.Errorf("could not read check_every %q: say \"30s\", \"5 minutes\", or \"1 hour\"", s)
}

var autoNameClean = regexp.MustCompile(`[^a-z0-9]+`)

// scheduleAutoName makes a name from the ask so the agent need not invent
// one: the first words of `then`, or the trigger and time.
func scheduleAutoName(when, then, timeSpec string) string {
	src := then
	if src == "" {
		src = when + " " + timeSpec
	}
	words := strings.Fields(strings.ToLower(src))
	if len(words) > 4 {
		words = words[:4]
	}
	n := strings.Trim(autoNameClean.ReplaceAllString(strings.Join(words, "-"), "-"), "-")
	if n == "" {
		n = when
	}
	return n
}

// scheduleFoldedToolNames lists the retired names, for the alias map and
// tests.
func scheduleFoldedToolNames() []string {
	out := make([]string, 0, len(scheduleFoldedTools))
	for n := range scheduleFoldedTools {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// userRecurringMatches is every recurring task owner has, on any agent, whose
// id or name is key.
func userRecurringMatches(owner, key string) []recurringTaskRow {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	var out []recurringTaskRow
	for _, rt := range listAgentRecurringTasks(owner, "") {
		if rt.TaskID == key || strings.EqualFold(recurringName(rt.Payload), key) {
			out = append(out, rt)
		}
	}
	return out
}

// recurringOfAgent keeps the tasks run by the agent agentKey names, by its id
// or, while it still exists, its name.
func recurringOfAgent(owner, agentKey string, rows []recurringTaskRow) []recurringTaskRow {
	want := agentKey
	if orchestrateBaseDB != nil {
		if a, ok := findAgentByNameOrID(UserDB(orchestrateBaseDB, owner), owner, agentKey); ok {
			want = a.ID
		}
	}
	var out []recurringTaskRow
	for _, rt := range rows {
		if rt.Payload.AgentID == want {
			out = append(out, rt)
		}
	}
	return out
}

func recurringIDs(rows []recurringTaskRow) string {
	ids := make([]string, len(rows))
	for i, rt := range rows {
		ids[i] = rt.TaskID
	}
	return strings.Join(ids, ", ")
}

// --- the shared timing, from the tool's words ---------------------------------

var timesListRe = regexp.MustCompile(`(?i)^(?:(?:every\s+day|daily|at)\s+)?(\d{1,2}:\d{2}(?:\s*,\s*\d{1,2}:\d{2})+)$`)

// scheduleDaysArg reads days="weekdays" / "weekends" / "every day" /
// "mon,wed,fri" into a weekday mask. Zero for every day or "".
func scheduleDaysArg(s string) (uint8, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "", "daily", "every day", "everyday", "all":
		return 0, nil
	case "weekdays", "weekday":
		return timing.Weekdays, nil
	case "weekends", "weekend":
		return timing.Weekends, nil
	}
	if spec, ok := timing.FromCron(s + " 00:00"); ok {
		return spec.Days, nil
	}
	return 0, fmt.Errorf("days %q: use weekdays, weekends, every day, or day names like mon,wed,fri", s)
}

// everyTiming is when="every" as a core/timing.Spec, the form every Scheduler
// dialog edits: the parsed time (a cron-like spec, an interval, or a list of
// set times), random gaps or a number a day, days, a window, and the stops.
// ok is false only for an old form a Spec cannot say (an annual date), which
// is passed on as it was.
func everyTiming(owner string, args map[string]any, cron string, interval int, times []int) (timing.Spec, bool, error) {
	var s timing.Spec
	switch {
	case oArgBool(args, "random"):
		if n := oArgInt(args, "times_per_day"); n > 0 {
			s = timing.Spec{How: timing.HowRandomCount, PerDay: n}
		} else {
			lo, hi := oArgInt(args, "min_gap_minutes")*60, oArgInt(args, "max_gap_minutes")*60
			if lo <= 0 {
				lo = interval
			}
			s = timing.Spec{How: timing.HowRandomGap, MinGapSeconds: lo, MaxGapSeconds: hi}
		}
	case len(times) > 0:
		s = timing.Spec{How: timing.HowTimes, Times: times}
	case cron != "":
		spec, ok := timing.FromCron(cron)
		if !ok {
			return timing.Spec{}, false, nil
		}
		s = spec
	default:
		s = timing.Spec{How: timing.HowEvery, EverySeconds: interval}
	}
	if d := strings.TrimSpace(oArgStr(args, "days")); d != "" {
		mask, err := scheduleDaysArg(d)
		if err != nil {
			return timing.Spec{}, false, err
		}
		s.Days = mask
	}
	if from, to := strings.TrimSpace(oArgStr(args, "active_from")), strings.TrimSpace(oArgStr(args, "active_to")); from != "" || to != "" {
		has, f, tt, err := standingWindowFrom(from, to)
		if err != nil {
			return timing.Spec{}, false, err
		}
		s.HasWindow, s.WindowFrom, s.WindowTo = has, f, tt
	}
	stopAt, err := stopAtFrom(owner, oArgStr(args, "stop_on"))
	if err != nil {
		return timing.Spec{}, false, err
	}
	s.StopAt = stopAt
	if err := s.Validate(); err != nil {
		return timing.Spec{}, false, err
	}
	return s, true, nil
}

// scheduleChangeApproval is the change question for a schedule another
// family's agent runs (change_approval.go): "" when the change may go ahead.
// An agent pausing, moving or deleting what it, its parent or its sub-agents
// run is managing its own work and asks nothing; Builder, or another agent,
// changing someone else's schedule asks the owner first. Running one now is
// not a change.
func scheduleChangeApproval(sess *ToolSession, owner, actingAgent, action, name string, args map[string]any) string {
	if sess == nil || sess.ApproveChange == nil {
		return ""
	}
	verb := map[string]string{
		"pause": "Pause it.", "resume": "Resume it.",
		"delete": "Delete it and cancel its schedule.", "cancel": "Delete it and cancel its schedule.", "remove": "Delete it and cancel its schedule.",
		"move":   "Change where it reports.",
		"edit":   "Change what it does: " + truncate(strings.TrimSpace(oArgStr(args, "then")), 300),
		"relink": "Hand it to " + chFirst(strings.TrimSpace(oArgStr(args, "run_by")), "another agent") + " to run.",
	}[action]
	if verb == "" {
		return "" // run_now: nothing changes
	}
	db := agentStore(owner)
	runner := ""
	switch {
	case strings.TrimSpace(oArgStr(args, "agent")) != "":
		key := strings.TrimSpace(oArgStr(args, "agent"))
		runner = key
		if db != nil {
			if a, ok := findAgentByNameOrID(db, owner, key); ok {
				runner = a.ID
			}
		}
	default:
		if sa, ok := GetStandingAgent(RootDB, owner, name); ok {
			runner = sa.AgentID
		} else if m, ok := GetEventMonitor(RootDB, owner, name); ok {
			runner = m.WakeAgent
		} else {
			runner = actingAgent // its own recurring task
		}
	}
	if db != nil && runner != "" && topAgentOf(db, runner) == topAgentOf(db, actingAgent) {
		return ""
	}
	return sessionApprove(sess, "schedule", name, verb, changeOpFor(action == "delete" || action == "cancel" || action == "remove"))
}

// scheduledRunsListing lists the scheduled runs (standing agents) that run as
// the agent: controllerAgentID, the agent `agent` names, or every agent the
// owner has for "all". Each with its timing (a link group's member says so),
// its state, and what it does, which is what an edit replaces.
func scheduledRunsListing(owner, controllerAgentID string, args map[string]any) string {
	want := controllerAgentID
	all := false
	if key := strings.TrimSpace(oArgStr(args, "agent")); key != "" {
		if strings.EqualFold(key, "all") {
			all = true
		} else if db := agentStore(owner); db != nil {
			if a, ok := findAgentByNameOrID(db, owner, key); ok {
				want = a.ID
			}
		}
	}
	var b strings.Builder
	for _, sa := range ListStandingAgents(RootDB, owner) {
		if !all && sa.AgentID != want {
			continue
		}
		state := "active"
		if sa.Paused {
			state = "paused"
		}
		if RelinkFixesIt(StandingParkCause(sa)) {
			state = "PARKED, needs relink (" + sa.BrokenReason + ")"
		}
		timing := StandingScheduleLabel(sa)
		if sa.LinkGroup != "" {
			timing += " (link group " + strconv.Quote(sa.LinkGroup) + ")"
		}
		does := strings.TrimSpace(sa.Mission)
		if len(does) > listMissionMax {
			does = truncate(does, listMissionMax) + fmt.Sprintf("\n  (shortened: schedule(action=\"get\", name=%q) shows all of it, and edit with append or find/replace changes part of it without retyping the rest)", sa.Name)
		}
		fmt.Fprintf(&b, "- %s: %s, %s\n  does: %s\n", sa.Name, timing, state, does)
	}
	return strings.TrimSpace(b.String())
}

// scheduleEdit changes only what a scheduled run or a monitor does: its
// mission (a run) or its brief (a monitor). Timing, link group, goal and
// state are left as they are; both are read by name when they fire, so the
// next fire does the new thing. Re-issuing create with the same name was the
// only way to change one before, and that needs the timing again, which a
// member of a link group does not have of its own (it runs on the group's).
//
// An agent without the fleet tools changes only what runs as it (or as its
// own sub-agents), the same reach its list has.
func scheduleEdit(owner, controllerAgentID string, hasFleet bool, name string, change missionChange) (string, error) {
	mine := scheduleReach(owner, controllerAgentID, hasFleet)
	if sa, ok := GetStandingAgent(RootDB, owner, name); ok {
		if !mine(sa.AgentID) {
			return "", fmt.Errorf("%q runs as another agent; only it, or the owner, can change what it does", name)
		}
		next, err := change.apply(sa.Mission)
		if err != nil {
			return "", fmt.Errorf("%q: %w", name, err)
		}
		sa.Mission = next
		SaveStandingAgent(RootDB, sa)
		return fmt.Sprintf("Changed what %q does; it runs %s as before. %s", name, StandingScheduleLabel(sa), change.said(next)), nil
	}
	if m, ok := GetEventMonitor(RootDB, owner, name); ok {
		if !mine(m.WakeAgent) {
			return "", fmt.Errorf("%q wakes another agent; only it, or the owner, can change what it hands over", name)
		}
		next, err := change.apply(m.WakeBrief)
		if err != nil {
			return "", fmt.Errorf("%q: %w", name, err)
		}
		m.WakeBrief = next
		SaveEventMonitor(RootDB, m)
		return fmt.Sprintf("Changed what %q hands over when it fires; it checks as before. %s", name, change.said(next)), nil
	}
	return "", fmt.Errorf("no scheduled run or monitor named %q (a recurring task of your own changes by create with the same name, its when and time): schedule(action=\"list\") shows them", name)
}

// scheduleReach is whether this agent may read or change a schedule that runs
// as runner: with the fleet tools, any; otherwise its own and its sub-agents'.
func scheduleReach(owner, controllerAgentID string, hasFleet bool) func(runner string) bool {
	db := agentStore(owner)
	return func(runner string) bool {
		if hasFleet || runner == controllerAgentID {
			return true
		}
		return db != nil && runner != "" && topAgentOf(db, runner) == topAgentOf(db, controllerAgentID)
	}
}

// listMissionMax is how much of a mission the list shows before shortening
// it and pointing at get.
const listMissionMax = 600

// scheduleGet is one scheduled run or monitor with what it does in full: the
// list shortens long missions, and an edit to part of one has to be able to
// see the whole of it.
func scheduleGet(owner, controllerAgentID string, hasFleet bool, name string) (string, error) {
	mine := scheduleReach(owner, controllerAgentID, hasFleet)
	if sa, ok := GetStandingAgent(RootDB, owner, name); ok && mine(sa.AgentID) {
		state := "active"
		if sa.Paused {
			state = "paused"
		}
		timing := StandingScheduleLabel(sa)
		if sa.LinkGroup != "" {
			timing += " (link group " + strconv.Quote(sa.LinkGroup) + ")"
		}
		return fmt.Sprintf("%s: %s, %s. What it does, in full:\n\n%s", sa.Name, timing, state, strings.TrimSpace(sa.Mission)), nil
	}
	if m, ok := GetEventMonitor(RootDB, owner, name); ok && mine(m.WakeAgent) {
		return fmt.Sprintf("%s (a monitor). What it hands over when it fires, in full:\n\n%s", m.Name, strings.TrimSpace(m.WakeBrief)), nil
	}
	return "", fmt.Errorf("no scheduled run or monitor named %q that you can read: schedule(action=\"list\") shows them", name)
}

// missionChange is one edit to what a schedule does: the whole new text
// (Then), one passage changed in place (Find, Replace), or a passage added at
// the end (Append). Exactly one kind per edit.
//
// The partial kinds exist because the whole-text kind needs the whole text,
// and an agent shown a shortened list either retyped a long mission from the
// part it could see, losing the rest, or stopped to ask the person to paste
// it. A rule added to the end needs none of the rest.
type missionChange struct {
	Then, Find, Replace, Append string
}

func (c missionChange) valid() error {
	kinds := 0
	for _, set := range []bool{c.Then != "", c.Find != "", c.Append != ""} {
		if set {
			kinds++
		}
	}
	switch {
	case kinds == 0:
		return Error("edit needs one of: then (the whole new text), find + replace (one passage changed), or append (a line added at the end)")
	case kinds > 1:
		return Error("edit takes one kind of change at a time: then, find + replace, or append")
	}
	return nil
}

// apply makes the change to old.
func (c missionChange) apply(old string) (string, error) {
	switch {
	case c.Then != "":
		return c.Then, nil
	case c.Append != "":
		return strings.TrimRight(old, " \n") + "\n\n" + c.Append, nil
	}
	switch n := strings.Count(old, c.Find); n {
	case 0:
		return "", Error("find does not appear in what it does now. Copy the passage exactly from schedule(action=\"get\")")
	case 1:
		return strings.Replace(old, c.Find, c.Replace, 1), nil
	default:
		return "", fmt.Errorf("find appears %d times in what it does now; include more of the surrounding text so it names one place", n)
	}
}

// said is the result line: what changed, not the whole text again.
func (c missionChange) said(next string) string {
	switch {
	case c.Append != "":
		return "Added at the end: " + truncate(c.Append, 300)
	case c.Find != "":
		return "Changed one passage; the rest is as it was."
	}
	return "From the next run it does: " + truncate(next, 400)
}
