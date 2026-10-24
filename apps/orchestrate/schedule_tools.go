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

// recurringToolDef is the agent's scheduling tool when it has no fleet: the
// one `schedule` tool (operator_schedule_tool.go) composed from this turn's
// recurring tasks alone, so when="every" with nothing else named puts THIS
// agent on a clock and when="at" is a task that fires once. The grouped
// `recurring` tool it replaced had its own vocabulary (pattern, interval
// minutes, max_fires); the agent now says it the user's way and the fold
// translates.
func (t *chatTurn) recurringToolDef() AgentToolDef {
	user, agentID := "", ""
	if t != nil {
		user, agentID = t.user, t.agent.ID
	}
	return scheduleToolDef(nil, user, agentID, nil, t)
}

// recurringTasks is this agent's own recurring tasks, for schedule's
// name-to-id lookup.
func (t *chatTurn) recurringTasks() []recurringTaskRow {
	if t == nil {
		return nil
	}
	return listAgentRecurringTasks(t.user, t.agent.ID)
}

// recurringEditTarget reports whether an existing task is the one a re-issued
// schedule EDITS rather than sits beside. The NAME identifies a task — a re-issue
// almost never reproduces a multi-sentence directive verbatim — and an identical
// prompt still matches the tasks that carry no name.
//
// Notice what it does NOT take: the session. Matching used to require the task to
// be homed in the thread the call came from, which meant a task living in the
// cortex — where a cortex agent's own conversation runs, and where
// recurring(move) puts one — was invisible to every later edit, and each retime
// banked another copy under the same name.
func recurringEditTarget(p orchUpdatePayload, name, prompt string) bool {
	if n := strings.TrimSpace(name); n != "" && strings.EqualFold(strings.TrimSpace(p.Name), n) {
		return true
	}
	prompt = strings.TrimSpace(prompt)
	return prompt != "" && strings.TrimSpace(p.Prompt) == prompt
}

// recurringScope resolves WHOSE schedules an action works on.
//
// Omitted is the agent being talked to, which is what every caller meant before
// this existed. "all" is every agent the user owns — the answer to "what is
// scheduled anywhere?". A name or id is that agent, resolved the way
// agents(action="get") resolves one, so a model can pass back the name it was
// given rather than hunting for an id.
//
// The reason it exists: a recurring task belongs to the agent that created it,
// so an agent asked about ANOTHER agent's schedule could not see it at all.
// Live, that produced neither an answer nor a refusal — the turn fetched a pile
// of unrelated records and narrated them instead of saying it could not look.
//
// Ownership never widens here: listAgentRecurringTasks filters by username, so
// every scope below is still only this user's tasks.
func (t *chatTurn) recurringScope(args map[string]any) (agentID, label string, err error) {
	key := strings.TrimSpace(stringArg(args, "agent"))
	switch {
	case key == "":
		return t.agent.ID, "this agent", nil
	case strings.EqualFold(key, "all"):
		return "", "any of your agents", nil
	}
	a, ok := findAgentByNameOrID(t.udb, t.user, key)
	if !ok {
		return "", "", fmt.Errorf("no agent %q: agents(action=\"list\") shows what you have", key)
	}
	return a.ID, fmt.Sprintf("%q", a.Name), nil
}

// recurringAgentName labels a task by the agent that owns it, so a listing that
// spans agents says which is which and a cancel names what it just stopped.
func (t *chatTurn) recurringAgentName(id string) string {
	if a, ok := loadAgent(t.udb, id); ok && strings.TrimSpace(a.Name) != "" {
		return a.Name
	}
	return id
}

func (t *chatTurn) recurringSchedule(args map[string]any) (string, error) {
	if t.session == nil || t.session.ID == "" {
		return "", errors.New("recurring(schedule) requires an active session: start a turn first")
	}
	// A recurring task runs THIS agent, in THIS session: that is its whole
	// shape, and there is no parameter to point it elsewhere. For an author
	// that is never what was meant. Asked to put an agent on a schedule,
	// Builder would schedule ITSELF to replay a build prompt forever, in a
	// session nobody reopens, and the agent the user was talking about would
	// have nothing on it at all.
	//
	// Refuse and name the tool that does this properly, rather than accept and
	// be wrong on a clock. schedule(when="every") takes `agent` precisely
	// because "run THAT agent every morning" is a different job from "do this
	// again in an hour".
	if agentHandsOffSchedules(t.agent) {
		return "", fmt.Errorf("recurring(schedule) puts THIS agent on a clock, and scheduling %s is not what you want. "+
			"Use schedule(action=\"create\", when=\"every\") with agent set to the agent that should run, then set to what it should do each time, and time to when",
			t.agent.Name)
	}
	spec := RecurringSpec{
		SessionID:   t.session.ID,
		AgentID:     t.agent.ID,
		Username:    t.user,
		Prompt:      strings.TrimSpace(stringArg(args, "prompt")),
		Name:        strings.TrimSpace(stringArg(args, "name")),
		Pattern:     strings.ToLower(strings.TrimSpace(stringArg(args, "pattern"))),
		MaxFires:    intFromArgs(args, "max_fires"),
		Until:       strings.TrimSpace(stringArg(args, "until")),
		MaxAttempts: intFromArgs(args, "max_attempts"),
	}
	// The shared timing (core/timing), when the schedule tool built one: the
	// whole answer, with the pattern fields below kept for older callers.
	if tspec, ok, err := timingArg(args); err != nil {
		return "", err
	} else if ok {
		tspec.MaxRuns = 0 // a recurring task's run count is its max_fires
		spec.Timing = tspec
	}
	// daily_at alone says what it is: a task at set times of day.
	if at := strings.TrimSpace(stringArg(args, "daily_at")); at != "" {
		mins, err := parseDailyAt(at)
		if err != nil {
			return "", err
		}
		spec.AtMinutes = mins
		spec.Pattern = RecurringDaily
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
		spec.MaxGapSeconds = intFromArgs(args, "max_gap_minutes") * 60
	default:
		spec.IntervalSeconds = intFromArgs(args, "interval_minutes") * 60
	}
	// Update, don't duplicate: a re-issue that names (or restates) a task this
	// agent already runs REPLACES it rather than stacking a second copy.
	//
	// Scoped to the AGENT, not to this session, and matched on the task's NAME
	// before its directive. Both of those were how one task became three:
	// session-scoped matching cannot see a task homed in the cortex — where a
	// cortex agent's own conversation lives, and where recurring(move) puts one —
	// so every retime from a conversation made a new copy under the same name;
	// and directive matching needed the model to reproduce a multi-sentence
	// prompt verbatim, which a reworded edit never does.
	prior, replaced := (*orchUpdatePayload)(nil), false
	for _, rt := range listAgentRecurringTasks(t.user, t.agent.ID) {
		p := rt.Payload
		if !recurringEditTarget(p, spec.Name, spec.Prompt) {
			continue
		}
		// Every match goes, not just the first — duplicates already banked by the
		// old session-scoped matching collapse back into one on the next edit.
		UnscheduleTask(rt.TaskID)
		replaced = true
		if prior == nil {
			kept := p
			prior = &kept
		}
	}
	if prior != nil {
		// An edit keeps the task's identity: the session it was made in (which
		// its fires link back to) and its run history.
		spec.SessionID = prior.SessionID
		spec.FireCount = prior.FireCount
		spec.CreatedAt = prior.CreatedAt
	}
	id, err := ScheduleOrchestrateUpdate(spec)
	if err != nil {
		return "", err
	}
	verb, note := "SCHEDULED_OK", ""
	if replaced {
		verb, note = "UPDATED_OK", " (edited the existing task in place: no duplicate created; its run history and home thread carried over)"
	}
	out := fmt.Sprintf("%s id=%s%s: a recurring TASK now runs %s, appending its reply into this agent's cortex each cycle. It also appears in this agent's Schedules rail, where the user can cancel it. When you confirm to the user, call it a \"recurring task\" (not a bridge/monitor), say its reports appear in the cortex, and don't send them to the Bridges app. Manage it with recurring(action=\"list\") or recurring(action=\"cancel\", id=%q).", verb, id, note, specCadence(spec), id)
	// Unattended fires answer to gates an interactive turn never meets — a tool
	// that needs confirmation with nobody there to give it, a proactive message
	// with no inbound to reply to. Both used to surface only when the task fired,
	// which is the worst possible moment: hours later, to no one. Report it here,
	// where whoever is authoring the schedule can still act on it.
	if warn := PreflightSummary(t.app.PreflightAutonomous(spec.Username, spec.AgentID)); warn != "" {
		out += "\n\n" + warn
	}
	return out, nil
}

// recurringList shows the AGENT's active tasks, not just this session's —
// once a task can live in the Cortex thread (recurring(move)), a
// session-scoped list would hide exactly the tasks the user asks about.
// posts_to tells the model (and the user) where each task's reports land.
func (t *chatTurn) recurringList(args map[string]any) (string, error) {
	if t.session == nil || t.session.ID == "" {
		return "(no active session)", nil
	}
	type row struct {
		ID        string `json:"id"`
		Agent     string `json:"agent"` // whose task it is; a listing may span agents
		Name      string `json:"name"`
		Prompt    string `json:"prompt"`
		Pattern   string `json:"pattern"`
		Cadence   string `json:"cadence"`
		FireCount int    `json:"fire_count"`
		CreatedAt string `json:"created_at"`
		PostsTo   string `json:"posts_to"` // cortex, or its_session for a task naming no agent
		// Objective fields, present only on a task that has a goal.
		Objective string `json:"objective,omitempty"`
		State     string `json:"objective_state,omitempty"`
		Parked    string `json:"parked,omitempty"`
		Paused    bool   `json:"paused,omitempty"`
		// LinkGroup is the group it runs with, on the group's time: pause or
		// resume the group by this id to hold them all.
		LinkGroup string `json:"link_group,omitempty"`
	}
	scopeID, scopeLabel, err := t.recurringScope(args)
	if err != nil {
		return "", err
	}
	var rows []row
	for _, rt := range listAgentRecurringTasks(t.user, scopeID) {
		p := rt.Payload
		pattern := p.Pattern
		if pattern == "" {
			pattern = RecurringFixed
		}
		postsTo := "cortex"
		if scheduleSession(p.AgentID, p.SessionID) != cortexSessionID(p.AgentID) {
			postsTo = "its_session"
		}
		rows = append(rows, row{
			ID: rt.TaskID, Agent: t.recurringAgentName(p.AgentID),
			Name: recurringName(p), Prompt: p.Prompt,
			Pattern: pattern, Cadence: recurringDetail(p),
			FireCount: p.FireCount, CreatedAt: p.CreatedAt,
			PostsTo:   postsTo,
			Objective: p.Until, State: objectiveStateLabel(p.objective()),
			Parked: brokenListReason(p),
			Paused: p.Paused, LinkGroup: p.LinkGroup,
		})
	}
	if len(rows) == 0 {
		return fmt.Sprintf("(no recurring tasks for %s)", scopeLabel), nil
	}
	b, _ := json.MarshalIndent(rows, "", "  ")
	return string(b), nil
}

// recurringCancel stops any of THIS agent's tasks owned by this user —
// agent-scoped to match list, so a task moved to the Cortex thread stays
// cancellable from the conversation that's discussing it.
func (t *chatTurn) recurringCancel(args map[string]any) (string, error) {
	if t.session == nil || t.session.ID == "" {
		return "", errors.New("recurring(cancel) requires an active session")
	}
	id := strings.TrimSpace(stringArg(args, "id"))
	if id == "" {
		return "", errors.New("id is required for recurring(cancel)")
	}
	scopeID, scopeLabel, err := t.recurringScope(args)
	if err != nil {
		return "", err
	}
	for _, rt := range listAgentRecurringTasks(t.user, scopeID) {
		if rt.TaskID == id {
			UnscheduleTask(id)
			dropRecurringTaskNotes(rt.Payload)
			// Names the agent: cancelling across agents must never read as
			// having cancelled one of your own.
			return fmt.Sprintf("CANCELLED ok. Recurring task %s on %s removed.", id, t.recurringAgentName(rt.Payload.AgentID)), nil
		}
	}
	return "", fmt.Errorf("no recurring task %s on %s: recurring(action=\"list\", agent=\"all\") shows every id you own", id, scopeLabel)
}
