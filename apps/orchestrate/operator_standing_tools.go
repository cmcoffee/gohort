// The Operator's standing-agent tools: create, list, run, pause and delete
// agents that run on a schedule. Listed in operatorToolDefsUnfolded
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

func createStandingAgentToolDef(sess *ToolSession, owner, agentID, controllerAgentID string) AgentToolDef {
	// The session this surface is bound to, so a standing agent's reports can
	// land back in the session it was created from.
	controllerSession := ""
	if sess != nil {
		controllerSession = sess.ChatSessionID
	}
	return AgentToolDef{
		Tool: Tool{
			Name:        "create_standing_agent",
			Description: "Create a standing (scheduled) agent and start its schedule: the tool for work that RUNS on a clock and reports what it finds, whatever it finds. \"Every 5 minutes, fetch X and tell me the value\"; \"every morning, summarize the overnight alerts\". There is nothing to wait for: the schedule IS the trigger. (An event monitor is the opposite, it stays SILENT until something changes.) A job with a finish line is still this tool: `until` says what must be true for it to be DONE and `max_attempts` bounds the tries, which together are how \"done when you have read and reported it, stop after 2\" is expressed. NOT for running something once to try it: a standing agent keeps firing on its schedule until it is deleted, so one made to get a single run goes on running. To try a machine you built, use machine(action=\"run\", name, input); to run an agent or pipeline once, agents(action=\"run\"). agent_id must name an agent that already exists. Schedule it EITHER with cron (recurring at a wall-clock time, preferred for \"every day at HH:MM\") OR with interval_seconds + optional start_at (a specific first run, then a fixed interval).",
			Parameters: map[string]ToolParam{
				"name":                 {Type: "string", Description: "Short unique name for this standing job, e.g. \"daily-weather\"."},
				"agent_id":             {Type: "string", Description: "Name or id of an existing agent to run. Give this OR pipeline_id, not both."},
				"pipeline_id":          {Type: "string", Description: "Name or id of a stored pipeline to run instead of an agent. Use this when the task IS the workflow (a nightly research run, a scheduled report), rather than something an agent should think about first. It runs the pipeline directly, so no model call is spent deciding to start it, and `mission` becomes the pipeline's input."},
				"machine_id":           {Type: "string", Description: "Name or id of a stored MACHINE to run. The third target, for work that carries state between its steps: a pipeline is dataflow with nothing kept between stages, a machine has a blackboard and running lists. Reach for it when the task is \"gather, keep what is new, report on what changed\" rather than a straight-through workflow. The machine must be marked \"this RUNS instead of converses\", because a schedule fires with nobody there to answer a step that waits. `mission` becomes the run's input."},
				"mission":              {Type: "string", Description: "What the agent should do each run."},
				"until":                {Type: "string", Description: "(optional) Makes this an OBJECTIVE rather than a plain schedule: what must be TRUE for it to be FINISHED, in plain language (\"the file is uploaded and its link posted to the thread\"). Every run is judged against this from what it actually DID, not from what it said; the run that reaches the goal is the last, and each later attempt is told what the earlier ones tried. Use it when the user wants something DONE; omit it when they want something RUN on a schedule."},
				"max_attempts":         {Type: "number", Description: "(optional, with until) How many runs may end with the goal still UNMET before the schedule stops trying. Reaching it marks the schedule broken with the last reason, so the owner sees it stopped and why; resuming gives it a fresh allowance. Omit for no attempt bound."},
				"cron":                 {Type: "string", Description: "Recurring wall-clock schedule in the human form DAY(S) HH:MM, NOT 5-field crontab (\"*/1 * * * *\" is INVALID). LOCAL time, the SAME zone time_in_zone reports; use the time the user stated VERBATIM, do NOT convert to UTC. e.g. \"every day at 12pm\" → \"daily 12:00\"; also \"FRI 21:30\", \"weekdays 17:00\". For sub-hourly / every-N-minutes schedules cron can't express, use interval_seconds instead (e.g. 60 = every minute). Leave empty if using interval_seconds."},
				"start_at":             {Type: "string", Description: "ISO8601 first-run time, e.g. 2026-06-10T08:00:00-07:00. Use with interval_seconds for an arbitrary start + interval. Omit when using cron."},
				"interval_seconds":     {Type: "number", Description: "Recurrence interval in seconds (60 = every minute, 3600 = hourly, 21600 = every 6h, 86400 = daily). This is the way to schedule sub-hourly / every-N-minutes runs (cron can't). Use with optional start_at. Omit when using cron."},
				"max_interval_seconds": {Type: "number", Description: "(optional, with interval_seconds) Makes the cadence RANDOM: each gap between runs is drawn between interval_seconds and this, e.g. 1800 and 3600 for every 30-60 minutes at random."},
				"active_from":          {Type: "string", Description: "(optional, with interval_seconds) Only between this local time (HH:MM) and active_to: a run that would land outside the window waits for the next one."},
				"active_to":            {Type: "string", Description: "(optional, with active_from) The window's end, HH:MM local."},
			},
			Required: []string{"name", "agent_id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			name := strings.TrimSpace(oArgStr(args, "name"))
			agentID := strings.TrimSpace(oArgStr(args, "agent_id"))
			cron := strings.TrimSpace(oArgStr(args, "cron"))
			interval := oArgInt(args, "interval_seconds")
			pipelineRef := strings.TrimSpace(oArgStr(args, "pipeline_id"))
			machineRef := strings.TrimSpace(oArgStr(args, "machine_id"))
			if name == "" {
				return "", fmt.Errorf("name is required")
			}
			var namedTargets []string
			for _, t := range []struct{ label, ref string }{
				{"agent_id", agentID}, {"pipeline_id", pipelineRef}, {"machine_id", machineRef},
			} {
				if t.ref != "" {
					namedTargets = append(namedTargets, t.label)
				}
			}
			switch {
			case len(namedTargets) == 0:
				return "", fmt.Errorf("give the schedule something to run: agent_id, pipeline_id, or machine_id")
			case len(namedTargets) > 1:
				return "", fmt.Errorf("name one of %s, not several: whichever the runner checked first would be the one that ran", strings.Join(namedTargets, ", "))
			}
			// Resolve the target to its STABLE record id now, and store that.
			// Agent ids are UUIDs; agent_id here is usually a name/slug. If we
			// stored the raw string, a later rename (Name changes, id doesn't)
			// would silently orphan this schedule. Resolving up front also
			// fails loudly at setup instead of quietly at fire time.
			// toolWarn flags a target that cannot reach any tool. Such an
			// agent schedules and fires perfectly well — it just can't DO
			// anything but talk, so a mission like "fetch X and send it"
			// produces a plausible-sounding run that accomplished nothing.
			// A warning, not an error: a text-only agent that composes
			// something and reports back is a legitimate standing task.
			// A pipeline target resolves to its stored ID the same way an
			// agent does, and for the same reason: a rename must not
			// orphan the schedule, and a typo should fail here rather
			// than at 3am.
			pipelineID := ""
			if pipelineRef != "" {
				if sess == nil || sess.DB == nil {
					return "", fmt.Errorf("no session store to resolve %q against", pipelineRef)
				}
				def, ok := LoadPipelineDef(sess.DB, owner, pipelineRef)
				if !ok {
					for _, d := range ListPipelineDefs(sess.DB, owner) {
						if strings.EqualFold(strings.TrimSpace(d.Name), pipelineRef) {
							def, ok = d, true
							break
						}
					}
				}
				if !ok {
					return "", fmt.Errorf("no pipeline named %q: pipeline(action=\"list\") shows the exact names", pipelineRef)
				}
				if err := def.Validate(); err != nil {
					return "", fmt.Errorf("pipeline %q would not run: %w", def.Name, err)
				}
				pipelineID = def.ID
			}
			// Same posture as the pipeline above: resolve to the stored id
			// so a rename cannot orphan the schedule, and refuse here what
			// would otherwise be refused at 3am.
			machineID := ""
			if machineRef != "" {
				if sess == nil || sess.DB == nil {
					return "", fmt.Errorf("no session store to resolve %q against", machineRef)
				}
				def, ok := findMachineByNameOrID(sess.DB, owner, machineRef)
				if !ok {
					return "", fmt.Errorf("no machine named %q: machine(action=\"list\") shows the exact names", machineRef)
				}
				if !def.Unattended {
					return "", fmt.Errorf("machine %q converses rather than runs: it has a step that waits for a person, and a schedule fires with nobody there. Turn on unattended on that machine, or schedule something else", def.Name)
				}
				if probs := def.Problems(); len(probs) > 0 {
					return "", fmt.Errorf("machine %q will not run yet: %s (%d outstanding)", def.Name, probs[0], len(probs))
				}
				machineID = def.ID
			}
			toolWarn := ""
			if agentID != "" && sess != nil && sess.DB != nil {
				if target, ok := findAgentByNameOrID(sess.DB, owner, agentID); ok {
					agentID = target.ID
					if agentHasNoTools(target) {
						toolWarn = fmt.Sprintf(
							" ⚠ WARNING: %q has NO tools (its allowlist is set to none), so it can only produce text, it cannot fetch, search, send, or call anything. If this mission needs a tool, pick a different agent or give this one an allowlist via update_agent, then recreate the schedule.",
							target.Name)
					}
				} else {
					return "", fmt.Errorf("no agent named %q found: create it first, or check the name (agents action=list shows the exact names)", agentID)
				}
			}
			// The scheduler dispatches the agent with Mission as the per-run
			// message, and dispatch REQUIRES a non-empty message — an empty
			// mission fails every fire with "message is required" (quietly, at
			// fire time). Default it so a self-directed agent (whose orchestrator
			// prompt already says what to do each run) still fires cleanly.
			mission := strings.TrimSpace(oArgStr(args, "mission"))
			if mission == "" && pipelineID == "" && machineID == "" {
				mission = "Run your standing task now."
			}
			sa := StandingAgent{
				Name: name, Owner: owner, AgentID: agentID, PipelineID: pipelineID, MachineID: machineID,
				Mission: mission, Created: time.Now(),
				Until:       strings.TrimSpace(stringArg(args, "until")),
				MaxAttempts: intFromArgs(args, "max_attempts"),
				// Where the runs report. Usually the agent that set this
				// up; for an AUTHOR wiring a schedule onto somebody else's
				// agent, that agent, because a build session is not a home
				// (see scheduleHomeAgent). The session link goes with it:
				// pinning a handed-over schedule to the session it was
				// authored in would send every run back to the workshop.
				ReportAgentID: scheduleHomeAgent(sess, controllerAgentID, agentID),
				ReportSessionID: func() string {
					if scheduleHomeAgent(sess, controllerAgentID, agentID) != controllerAgentID {
						return ""
					}
					return controllerSession
				}(),
				// Same default as a recurring task: a controller with a cortex
				// reads its scheduled runs there, not interleaved into whichever
				// conversation set the schedule up. The report session stays the
				// home, so the console's Move-to → Session brings it back.
				Surface: scheduleSurfaceDefault("", hasCortexThread(owner, scheduleHomeAgent(sess, controllerAgentID, agentID))),
			}
			spec, hasSpec, err := timingArg(args)
			if err != nil {
				return "", err
			}
			switch {
			case hasSpec:
				// The shared timing (core/timing): the whole answer when given.
				sa.Timing = spec
			case cron != "":
				if _, err := NextCronOccurrence(cron, time.Now()); err != nil {
					if strings.EqualFold(strings.TrimSpace(cron), "once") || strings.EqualFold(strings.TrimSpace(cron), "now") {
						return "", errors.New(standingNotOnce)
					}
					return "", fmt.Errorf("invalid cron %q: %w", cron, err)
				}
				sa.Cron = cron
			case interval > 0:
				sa.IntervalSeconds = interval
				if hi := oArgInt(args, "max_interval_seconds"); hi > 0 {
					if hi <= interval {
						return "", fmt.Errorf("max_interval_seconds (%d) has to be above interval_seconds (%d): the gap is drawn between the two", hi, interval)
					}
					sa.MaxIntervalSeconds = hi
				}
				has, from, to, err := standingWindowFrom(oArgStr(args, "active_from"), oArgStr(args, "active_to"))
				if err != nil {
					return "", err
				}
				sa.HasWindow, sa.WindowFromMin, sa.WindowToMin = has, from, to
				if startStr := strings.TrimSpace(oArgStr(args, "start_at")); startStr != "" {
					t, err := time.Parse(time.RFC3339, startStr)
					if err != nil {
						return "", fmt.Errorf("invalid start_at (use ISO8601 like 2026-06-10T08:00:00-07:00): %w", err)
					}
					sa.StartAt = t
				}
			default:
				return "", fmt.Errorf("provide a schedule: either cron, or interval_seconds (with optional start_at). %s", standingNotOnce)
			}
			SaveStandingAgent(RootDB, sa)
			if err := ScheduleStandingAgent(RootDB, sa); err != nil {
				return "", fmt.Errorf("saved but scheduling failed: %w", err)
			}
			summarizeMissionLater(sa)
			got, _ := GetStandingAgent(RootDB, owner, name)
			return fmt.Sprintf("Created standing agent %q running %q on %s. Next run: %s.%s",
				name, agentID, StandingScheduleLabel(got), got.NextRun.Local().Format("Mon Jan 2 3:04 PM"), toolWarn), nil
		},
	}
}

func listStandingAgentsToolDef(sess *ToolSession, owner string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "list_standing_agents",
			Description: "List the user's standing agents with their schedule, paused state, last run status, and next run.",
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
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
				// A parked one says so and what fixes it, so a deleted
				// agent's schedules read as something to relink to its
				// replacement rather than as paused ones.
				if RelinkFixesIt(StandingParkCause(sa)) {
					state = "PARKED, needs relink (" + sa.BrokenReason + "): schedule(action=\"relink\", name, run_by)"
				}
				next := "-"
				if !sa.NextRun.IsZero() {
					next = sa.NextRun.Local().Format("Mon Jan 2 3:04 PM")
				}
				// What it RUNS, which is an agent or a pipeline. Printing
				// sa.AgentID unconditionally rendered a pipeline schedule
				// as `runs ""` — a listing that shows a blank target is
				// how somebody concludes the schedule is broken and
				// deletes a working one.
				target := strconv.Quote(sa.AgentID)
				switch {
				case sa.TargetsPipeline():
					target = "pipeline " + strconv.Quote(sa.PipelineID)
					if sess != nil && sess.DB != nil {
						if def, ok := LoadPipelineDef(sess.DB, owner, sa.PipelineID); ok {
							target = "pipeline " + strconv.Quote(def.Name)
						}
					}
				case sa.TargetsMachine():
					target = "machine " + strconv.Quote(sa.MachineID)
					if sess != nil && sess.DB != nil {
						if def, ok := LoadMachineDef(sess.DB, owner, sa.MachineID); ok {
							target = "machine " + strconv.Quote(def.Name)
						}
					}
				}
				when := StandingScheduleLabel(sa)
				if sa.LinkGroup != "" {
					when += " (link group " + strconv.Quote(sa.LinkGroup) + ", on the group's time)"
					next = "with its group"
				}
				fmt.Fprintf(&b, "- %s (%s): runs %s on %s; last=%s; next=%s\n",
					sa.Name, state, target, when, status, next)
				// What it does, so an edit (schedule action="edit") starts
				// from the text it replaces rather than a guess at it.
				if m := strings.TrimSpace(sa.Mission); m != "" {
					fmt.Fprintf(&b, "  does: %s\n", truncate(m, 600))
				}
			}
			return strings.TrimSpace(b.String()), nil
		},
	}
}

func runStandingNowToolDef(owner string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "run_standing_now",
			Description: "Trigger a standing agent to run immediately (does not change its recurring schedule).",
			Parameters:  map[string]ToolParam{"name": {Type: "string", Description: "The standing agent's name."}},
			Required:    []string{"name"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			name := strings.TrimSpace(oArgStr(args, "name"))
			if _, ok := GetStandingAgent(RootDB, owner, name); !ok {
				return "", fmt.Errorf("no standing agent named %q", name)
			}
			if err := RunStandingNow(RootDB, owner, name); err != nil {
				return "", err
			}
			return fmt.Sprintf("Triggered %q to run now. The result will appear in Activity shortly.", name), nil
		},
	}
}

func setStandingPausedToolDef(owner string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "set_standing_paused",
			Description: "Pause or resume a standing agent's schedule.",
			Parameters: map[string]ToolParam{
				"name":   {Type: "string", Description: "The standing agent's name."},
				"paused": {Type: "boolean", Description: "true to pause, false to resume."},
			},
			Required: []string{"name", "paused"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			name := strings.TrimSpace(oArgStr(args, "name"))
			sa, ok := GetStandingAgent(RootDB, owner, name)
			if !ok {
				return "", fmt.Errorf("no standing agent named %q", name)
			}
			sa.Paused = oArgBool(args, "paused")
			if !sa.Paused && sa.StopCause == StoppedByEnd {
				sa.Runs = 0 // resuming one that ran its count gives it a fresh one
			}
			sa.StopCause, sa.StopNote = "", ""
			if sa.Paused {
				sa.StopCause = StoppedByOwner
			}
			if sa.Paused {
				if sa.SchedulerID != "" {
					UnscheduleTask(sa.SchedulerID)
					sa.SchedulerID = ""
					sa.NextRun = time.Time{}
				}
				SaveStandingAgent(RootDB, sa)
				if sa.LinkGroup != "" {
					return fmt.Sprintf("Paused %q.%s", name, linkMemberNote(owner, sa.LinkGroup, true)), nil
				}
				return fmt.Sprintf("Paused %q.", name), nil
			}
			SaveStandingAgent(RootDB, sa)
			if err := ScheduleStandingAgent(RootDB, sa); err != nil {
				return "", err
			}
			// A linked one has no time of its own while it is linked, so its
			// next run is the group's, not an empty date.
			if sa.LinkGroup != "" {
				return fmt.Sprintf("Resumed %q.%s", name, linkMemberNote(owner, sa.LinkGroup, false)), nil
			}
			got, _ := GetStandingAgent(RootDB, owner, name)
			return fmt.Sprintf("Resumed %q. Next run: %s.", name, got.NextRun.Local().Format("Mon Jan 2 3:04 PM")), nil
		},
	}
}

func deleteStandingAgentToolDef(owner string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "delete_standing_agent",
			Description: "Permanently delete a standing (scheduled) agent and cancel its schedule. Use this for a real removal; set_standing_paused only pauses.",
			Parameters:  map[string]ToolParam{"name": {Type: "string", Description: "The standing agent's name."}},
			Required:    []string{"name"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			name := strings.TrimSpace(oArgStr(args, "name"))
			if _, ok := GetStandingAgent(RootDB, owner, name); !ok {
				return "", fmt.Errorf("no standing agent named %q", name)
			}
			DeleteStandingAgent(RootDB, owner, name)
			return fmt.Sprintf("Deleted standing agent %q and cancelled its schedule.", name), nil
		},
	}
}
