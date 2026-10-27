package orchestrate

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/timing"
)

type consoleRecurringRow struct {
	// Recurring tasks carry no user-set name — the label is the prompt's first
	// line (matching the Schedules rail). It renders as the card title.
	Name    string `json:"name"`
	Cadence string `json:"cadence"`            // human cadence ("recurring - every 30m")
	Fires   string `json:"fires,omitempty"`    // "<fired> / <cap>" so far
	Runs    string `json:"runs,omitempty"`     // "for sub-agent X" when the parent hands its work on
	NextRun string `json:"next_run,omitempty"` // RFC3339 next fire (matches consoleAgentRow)
	State   string `json:"state,omitempty"`    // visible only when broken ("⚠ needs relink — …")
	// Objective and Failing are the same two lines the other scheduled kinds
	// carry (schedule_row_state.go). The objective used to live in State,
	// which meant a task that had both a goal and a park could only say one of
	// them — and State is the cell that answers "is it running".
	Objective string `json:"objective,omitempty"`
	Failing   string `json:"failing,omitempty"`
	ID        string `json:"_id"`               // hidden; row-action target (the scheduler task id)
	UID       string `json:"_uid,omitempty"`    // hidden; the task's own id, stable across fires (link groups key on it)
	Broken    bool   `json:"_broken,omitempty"` // hidden gate (Delete-only on a broken row)
	Paused    bool   `json:"_paused,omitempty"` // hidden gate: Pause on a live row, Resume on a paused one
	// Relinkable gates the Relink row action: only a schedule whose target is
	// GONE has anything to relink. A stalled objective gets Resume instead.
	Relinkable bool   `json:"_relinkable,omitempty"`
	Agent      string `json:"_agent,omitempty"` // hidden; whose task it is, named on the fleet Scheduler
}

// subAgentRunsLabel is a schedule's "runs" line when its parent hands the
// work to a sub-agent, and "" otherwise.
func subAgentRunsLabel(text string) string {
	if sub := subAgentHandoffOf(text); sub != "" {
		return "for sub-agent " + sub
	}
	return ""
}

// handleConsoleRecurring lists the owner's recurring tasks (the `recurring` tool
// → per-session scheduled updates) for the Recurring-tasks nav card view — the
// status-card sibling of Enabled agents / Event monitors. Scoped to the agent
// that runs them (AgentID), matching the other two panes.
func (T *OrchestrateApp) handleConsoleRecurring(c *jsonapi.Call) (any, error) {
	r := c.R
	user := c.User
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
	return consoleRecurringRows(user, agentID), nil
}

// consoleRecurringRows builds the rows for this view. Split off the handler so the
// merged Scheduler page (console_scheduler.go) renders THESE rows rather than
// its own copy of the same logic — a second builder is how the two views
// come to disagree about what is scheduled.
func consoleRecurringRows(user, agentID string) []consoleRecurringRow {
	rows := []consoleRecurringRow{}
	for _, rt := range listAgentRecurringTasks(user, agentID) {
		label := firstLineLabel(rt.Payload.Prompt)
		if label == "" {
			label = "recurring task"
		}
		// Uncapped continuous-random tasks report effectiveMaxFires as MaxInt32
		// (no real ceiling) — show just the count run so far, not "3 / 2147483647".
		fires := fmt.Sprintf("%d fired", rt.Payload.FireCount)
		if cap := rt.Payload.effectiveMaxFires(); cap < math.MaxInt32 {
			fires = fmt.Sprintf("%d / %d fired", rt.Payload.FireCount, cap)
		}
		row := consoleRecurringRow{
			Name:    label,
			Cadence: recurringDetail(rt.Payload),
			Fires:   fires,
			Runs:    subAgentRunsLabel(rt.Payload.Prompt),
			NextRun: rt.RunAt,
			ID:      rt.TaskID,
			UID:     recurringTaskUID(rt.Payload),
			Agent:   rt.Payload.AgentID,
		}
		// Where an objective stands, for the rows that have a goal.
		row.Objective = objectiveStateLabel(rt.Payload.objective())
		// A next run that is not on the cadence needs a reason, or it is a time
		// nobody chose and nothing explains. Two things move one: a failing
		// streak, which Failing reports with its count, and an attempt that
		// asked to wait, which only shows up here when there is no objective
		// line already carrying it.
		row.Failing = scheduleFailingLabel(rt.Payload.ConsecutiveFailures, 0, parseSchedTime(rt.Payload.NextAttemptAt), UserLocation(user))
		if row.Objective == "" && row.Failing == "" {
			if w := strings.TrimSpace(rt.Payload.NextAttemptWhy); w != "" {
				row.State = "waiting: " + truncateObs(w, 120)
			}
		}
		if rt.Payload.Paused && !rt.Payload.Broken {
			row.Paused = true
			row.State = "paused"
			row.NextRun = "" // held: the dormant re-check isn't a real next run
		}
		if rt.Payload.Broken {
			row.Broken = true
			row.State = parkedStateLabel(recurringParkCause(rt.Payload), rt.Payload.BrokenReason)
			if RelinkFixesIt(recurringParkCause(rt.Payload)) {
				row.State += parkedDeadlineNote(schedKindRecurring, user, recurringTaskUID(rt.Payload))
			}
			row.Relinkable = RelinkFixesIt(recurringParkCause(rt.Payload))
			row.NextRun = "" // parked: the dormant re-check isn't a real next run
		}
		rows = append(rows, row)
	}
	return rows
}

// handleConsoleRecurringResume puts a PARKED task back on its cadence.
//
// Written for a stalled objective — the owner reads the reason, fixes what it
// named, and wants another go — but offered on any parked row, because "I
// believe the cause is fixed" is the same request whatever parked it. A task
// parked for a deleted agent simply re-parks on its next fire with the same
// message, which is self-correcting and says so.
//
// The history survives: Attempts, FireCount and the ledger are untouched. Only
// the attempt ALLOWANCE restarts (AttemptsBase), or the resumed task would
// stall again on its first fire.
func (T *OrchestrateApp) handleConsoleRecurringResume(c *jsonapi.Call, _ struct{}) (any, error) {
	r := c.R
	user := c.User
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		return nil, jsonapi.BadRequest("id required")
	}
	// Ownership by payload username, the same way delete and relink enforce it:
	// the scheduler bucket is global and keyed by opaque UUID.
	for _, rt := range listAgentRecurringTasks(user, "") {
		if rt.TaskID != id {
			continue
		}
		// A paused task (and not parked) is a plain resume: its attempt
		// allowance is its own, and nothing it was waiting on has changed.
		if rt.Payload.Paused && !rt.Payload.Broken {
			if _, err := setRecurringPaused(user, id, false); err != nil {
				return nil, jsonapi.Status(http.StatusInternalServerError, err.Error())
			}
			return jsonapi.Empty(http.StatusNoContent), nil
		}
		UnscheduleTask(id)
		p := rt.Payload
		p.Broken = false
		p.Paused = false
		p.BrokenReason = ""
		p.AttemptsBase = p.FireCount
		p.LastActive = time.Now().Format(time.RFC3339)
		next, err := computeNextFire(&p, time.Now().In(UserLocation(user)))
		if err != nil {
			return nil, jsonapi.Status(http.StatusInternalServerError, "resumed but couldn't schedule: "+err.Error())
		}
		if _, err := ScheduleTask(OrchestrateScheduledUpdateKind, p, next); err != nil {
			return nil, jsonapi.Status(http.StatusInternalServerError, err.Error())
		}
		Log("[orchestrate/objective] task %q resumed by %s: allowance restarts at fire %d", recurringName(p), user, p.FireCount)
		return jsonapi.Empty(http.StatusNoContent), nil
	}
	return nil, jsonapi.NotFound("recurring task not found")
}

// handleConsoleRecurringPause holds one of the user's recurring tasks: POST
// ?id=<scheduler id>. Resume is handleConsoleRecurringResume.
func (T *OrchestrateApp) handleConsoleRecurringPause(c *jsonapi.Call, _ struct{}) (any, error) {
	r := c.R
	user := c.User
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		return nil, jsonapi.BadRequest("id required")
	}
	if _, err := setRecurringPaused(user, id, true); err != nil {
		return nil, jsonapi.NotFound(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// setRecurringPaused pauses or resumes one of the owner's recurring tasks, by
// its scheduler id, and says what happened in words a caller can pass on. The
// Scheduler's buttons and the schedule tool both come here.
//
// A paused task waits on the dormant tick a parked one uses, so it stays
// listed; Resume puts it back on its own time, or its link group's. Found
// among the owner's own tasks only: the scheduler bucket is global.
func setRecurringPaused(owner, id string, paused bool) (string, error) {
	for _, rt := range listAgentRecurringTasks(owner, "") {
		if rt.TaskID != id {
			continue
		}
		p := rt.Payload
		name := recurringName(p)
		if p.Paused == paused {
			if paused {
				return fmt.Sprintf("%q is already paused.", name), nil
			}
			return fmt.Sprintf("%q is not paused.", name), nil
		}
		p.Paused = paused
		next := time.Now().Add(brokenDormantReArm)
		if !paused {
			// Resuming renews it, as an edit does: the idle guard counts from
			// here, not from before the pause.
			p.LastActive = time.Now().Format(time.RFC3339)
			n, err := computeNextFire(&p, time.Now().In(UserLocation(owner)))
			if err != nil {
				return "", fmt.Errorf("could not put %q back on its schedule: %w", name, err)
			}
			next = n
		}
		UnscheduleTask(id)
		if _, err := ScheduleTask(OrchestrateScheduledUpdateKind, p, next); err != nil {
			return "", err
		}
		Log("[orchestrate/scheduled] %s %s recurring task %q", owner, map[bool]string{true: "paused", false: "resumed"}[paused], name)
		switch {
		case p.LinkGroup != "":
			verb := "Resumed"
			if paused {
				verb = "Paused"
			}
			return fmt.Sprintf("%s %q.%s", verb, name, linkMemberNote(owner, p.LinkGroup, paused)), nil
		case paused:
			return fmt.Sprintf("Paused %q. Resume it to put it back on its schedule.", name), nil
		case p.Broken:
			return fmt.Sprintf("Resumed %q, but it is still parked: %s", name, chFirst(p.BrokenReason, "it needs attention before it runs")), nil
		}
		return fmt.Sprintf("Resumed %q. Next run: %s.", name, next.In(UserLocation(owner)).Format("Mon Jan 2 3:04 PM")), nil
	}
	return "", fmt.Errorf("no recurring task %q is yours", id)
}

// handleConsoleRecurringDelete removes a recurring task by scheduler id from the
// per-agent schedules rail. The scheduler bucket is global and keyed by opaque
// UUID, so ownership is enforced by matching the task's payload username (via
// listAgentRecurringTasks) before unscheduling — a user can't cancel another
// user's task by guessing its id.
func (T *OrchestrateApp) handleConsoleRecurringDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	r := c.R
	user := c.User
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		return nil, jsonapi.BadRequest("id required")
	}
	for _, rt := range listAgentRecurringTasks(user, "") {
		if rt.TaskID == id {
			UnscheduleTask(id)
			dropRecurringTaskNotes(rt.Payload)
			return jsonapi.Empty(http.StatusNoContent), nil
		}
	}
	return nil, jsonapi.NotFound("recurring task not found")
}

// handleConsoleRecurringRelink re-points a parked (broken) recurring task at a
// live agent and puts it straight back on its real cadence. Recurring has no
// pause/resume concept (unlike monitors/standing), so relink resumes it
// directly; LastActive is refreshed so the idle guard doesn't immediately reap a
// just-relinked task.
func (T *OrchestrateApp) handleConsoleRecurringRelink(c *jsonapi.Call, _ struct{}) (any, error) {
	r := c.R
	user := c.User
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	newAgent := strings.TrimSpace(r.URL.Query().Get("value"))
	if _, ok := loadAgent(UserDB(T.DB, user), newAgent); !ok {
		return nil, jsonapi.BadRequest("no such agent")
	}
	if err := relinkRecurringToAgent(user, id, newAgent); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleConsoleRecurringRun fires a recurring task's prompt once immediately from
// the Recurring-tasks pane's "Run now" button — a one-off manual test that does
// NOT touch the schedule (RunOrchestrateUpdateNow fires without rescheduling or
// consuming the fire budget). Ownership is enforced inside RunOrchestrateUpdateNow
// (payload-username match), and we pre-check existence here to return a clean 404.
// The fire runs off-request in a goroutine with a background context (it replays a
// full agent-loop turn and must outlive the response); the reply is appended to
// the task's thread and the run ledger updates on reload. id = the scheduler task
// id. Returns 202 once the fire is launched.
func (T *OrchestrateApp) handleConsoleRecurringRun(c *jsonapi.Call, _ struct{}) (any, error) {
	r := c.R
	user := c.User
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		return nil, jsonapi.BadRequest("id required")
	}
	found := false
	for _, rt := range listAgentRecurringTasks(user, "") {
		if rt.TaskID == id {
			found = true
			break
		}
	}
	if !found {
		return nil, jsonapi.NotFound("recurring task not found")
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				Log("[orchestrate/console] run-now panicked for recurring task %s: %v", id, rec)
			}
		}()
		if err := RunOrchestrateUpdateNow(context.Background(), user, id); err != nil {
			Log("[orchestrate/console] run-now failed for recurring task %s: %v", id, err)
		}
	}()
	return jsonapi.Empty(http.StatusAccepted), nil
}

// handleConsoleRecurringGet returns one recurring task's editable fields for the
// rail's edit modal. Owner-checked via listAgentRecurringTasks. GET ?id=<id>.
func (T *OrchestrateApp) handleConsoleRecurringGet(c *jsonapi.Call) (any, error) {
	r := c.R
	user := c.User
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	udb := UserDB(T.DB, user)
	for _, rt := range listAgentRecurringTasks(user, "") {
		if rt.TaskID != id {
			continue
		}
		p := rt.Payload
		pattern := p.Pattern
		if pattern == "" {
			pattern = RecurringFixed
		}
		// A task handed to a sub-agent shows its directive without the
		// handoff line and the sub-agent in the "Who does it" picker.
		prompt, forID := p.Prompt, ""
		var forOptions []scheduleForOption
		if !isTaskWake(p.Prompt) {
			prompt, forID, forOptions = scheduleHandoffView(udb, user, p.AgentID, p.Prompt)
		}
		return map[string]any{
			"id": rt.TaskID,
			// The STORED name, not the derived one. recurringName falls back
			// to the directive's first line, and handing that to the editor's
			// Name box posts it straight back as an explicit name: rewriting
			// an unnamed task's directive would leave its card titled with the
			// OLD first line forever. Empty here shows the placeholder that
			// says where the label comes from.
			"name":             strings.TrimSpace(p.Name),
			"prompt":           prompt,
			"for":              forID,
			"for_options":      forOptions,
			"pattern":          pattern,
			"interval_minutes": p.IntervalSeconds / 60,
			"daily_at":         strings.ReplaceAll(fmtDailyAt(p.AtMinutes), " ", ""),
			"times_per_day":    p.TimesPerDay,
			"min_gap_minutes":  p.MinGapSeconds / 60,
			"max_gap_minutes":  p.MaxGapSeconds / 60,
			"has_window":       p.HasWindow,
			"active_from":      fmtHHMM(p.WindowFromMin),
			"active_to":        fmtHHMM(p.WindowToMin),
			"max_fires":        p.MaxFires,
			"fire_count":       p.FireCount,
			"cadence":          recurringDetail(p),
			"timing":           recurringTimingOut(p),
			"until":            recurringUntilOut(p),
			"linked":           p.LinkGroup != "",
		}, nil
	}
	return nil, jsonapi.NotFound("recurring task not found")
}

// handleConsoleRecurringUpdate edits a recurring task's schedule in place: rebuild
// the spec from the stored task (session / agent / prompt preserved) with the
// posted timing, unschedule the old, schedule the edited one. On a validation
// error the original is restored so a bad edit never destroys the task. POST
// ?id=<id> with a JSON timing body.
func (T *OrchestrateApp) handleConsoleRecurringUpdate(c *jsonapi.Call, body struct {
	Pattern         string `json:"pattern"`
	IntervalMinutes int    `json:"interval_minutes"`
	DailyAt         string `json:"daily_at"`
	TimesPerDay     int    `json:"times_per_day"`
	MinGapMinutes   int    `json:"min_gap_minutes"`
	MaxGapMinutes   int    `json:"max_gap_minutes"`
	ActiveFrom      string `json:"active_from"`
	ActiveTo        string `json:"active_to"`
	MaxFires        int    `json:"max_fires"`
	// Timing, when sent, is the shared timing section (core/timing) and
	// replaces the pattern fields above.
	Timing *timing.Spec `json:"timing"`
	// Until, when sent, is the dialog's Until section (schedule_until.go).
	Until *untilBody `json:"until"`
	// Pointers so "field omitted" (nil = preserve the stored value) is
	// distinguishable from "sent empty". Prompt is required, so an empty
	// prompt preserves too; Name may legitimately be cleared (empty falls
	// back to the prompt's first line at render).
	Prompt *string `json:"prompt"`
	Name   *string `json:"name"`
	// For is the "Who does it" picker: "" for the agent itself, or a
	// sub-agent's id. Sent only when it was changed.
	For *string `json:"for"`
}) (any, error) {
	r := c.R
	user := c.User
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	var found *orchUpdatePayload
	for _, rt := range listAgentRecurringTasks(user, "") {
		if rt.TaskID == id {
			p := rt.Payload
			found = &p
			break
		}
	}
	if found == nil {
		return nil, jsonapi.NotFound("recurring task not found")
	}
	// Directive + name are editable now (not just timing). Base = the stored
	// values; a provided prompt (non-empty; prompt is required) or name overrides.
	// Computed into LOCALS, not mutated onto found — the rejected-edit restore path
	// below re-schedules *found and must keep the original directive/name intact.
	prompt, name := found.Prompt, found.Name
	var posted *string
	if body.Prompt != nil {
		if p := strings.TrimSpace(*body.Prompt); p != "" {
			prompt, posted = p, &p
		}
	}
	if body.Name != nil {
		name = strings.TrimSpace(*body.Name)
	}
	// The editor showed the directive without its handoff line (see the get
	// above), so the line is rebuilt from the picker.
	if !isTaskWake(found.Prompt) {
		text, err := scheduleHandoffEdit(UserDB(T.DB, user), user, found.AgentID, found.Prompt, posted, body.For)
		if err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
		if text != nil {
			prompt = *text
		}
		if strings.TrimSpace(prompt) == "" {
			return nil, jsonapi.BadRequest("directive (prompt) is required")
		}
	}
	spec := RecurringSpec{
		SessionID: found.SessionID,
		AgentID:   found.AgentID,
		Username:  found.Username,
		Prompt:    prompt,
		Name:      name,
		FireCount: found.FireCount,          // preserve run history across an edit (don't reset the budget)
		CreatedAt: found.CreatedAt,          // keep the original creation time, not "now"
		UID:       recurringTaskUID(*found), // the task's identity, so its notes survive the edit
		LinkGroup: found.LinkGroup,          // and the group whose time it runs on
		// The objective travels too. An edit rebuilds the payload from this
		// spec, so a goal left out of it is a goal DELETED by a retime: the
		// task silently becomes an unbounded cadence that will never stop at
		// the thing it was created to reach, and the judge's verdicts go with
		// it. The Scheduler now prints that objective on the very row whose
		// Edit button lands here, which is how this was found.
		Until:           found.Until,
		MaxAttempts:     found.MaxAttempts,
		Attempts:        found.Attempts,
		AttemptsBase:    found.AttemptsBase,
		Pattern:         strings.ToLower(strings.TrimSpace(body.Pattern)),
		IntervalSeconds: body.IntervalMinutes * 60,
		TimesPerDay:     body.TimesPerDay,
		MinGapSeconds:   body.MinGapMinutes * 60,
		MaxGapSeconds:   body.MaxGapMinutes * 60,
		MaxFires:        body.MaxFires,
	}
	if body.Timing != nil {
		spec.Timing = *body.Timing
	}
	if body.Until != nil {
		if err := applyRecurringUntil(&spec, user, *body.Until); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
	}
	if spec.Pattern == RecurringDaily || (spec.Pattern == "" && strings.TrimSpace(body.DailyAt) != "") {
		mins, derr := parseDailyAt(body.DailyAt)
		if derr != nil {
			return nil, jsonapi.BadRequest(derr.Error())
		}
		spec.Pattern, spec.AtMinutes = RecurringDaily, mins
	}
	if spec.Pattern == "" {
		spec.Pattern = RecurringFixed
	}
	from := strings.TrimSpace(body.ActiveFrom)
	to := strings.TrimSpace(body.ActiveTo)
	if (from == "") != (to == "") {
		return nil, jsonapi.BadRequest("active_from and active_to must be set together")
	}
	if from != "" {
		fMin, ferr := parseHHMM(from)
		if ferr != nil {
			return nil, jsonapi.BadRequest(ferr.Error())
		}
		tMin, terr := parseHHMM(to)
		if terr != nil {
			return nil, jsonapi.BadRequest(terr.Error())
		}
		spec.HasWindow = true
		spec.WindowFromMin = fMin
		spec.WindowToMin = tMin
	}
	// Remove the old first (so the per-session cap has room for the replacement).
	UnscheduleTask(id)
	newID, err := ScheduleOrchestrateUpdate(spec)
	if err != nil {
		// Restore the original so a rejected edit doesn't destroy the task.
		if next, cerr := computeNextFire(found, time.Now().In(UserLocation(found.Username))); cerr == nil {
			_, _ = ScheduleTask(OrchestrateScheduledUpdateKind, *found, next)
		}
		return nil, jsonapi.BadRequest(err.Error())
	}
	return map[string]any{"id": newID}, nil
}

// handleConsoleRecurringForOptions is the "Who does it" picker for the New
// recurring task modal: GET ?agent=<id>. An empty list when the agent has no
// sub-agents, and the modal then draws no picker.
func (T *OrchestrateApp) handleConsoleRecurringForOptions(c *jsonapi.Call) (any, error) {
	r := c.R
	user, udb := c.User, c.UDB
	opts := scheduleForOptions(udb, user, strings.TrimSpace(r.URL.Query().Get("agent")))
	if opts == nil {
		opts = []scheduleForOption{}
	}
	return opts, nil
}

// handleConsoleRecurringCreate creates a NEW recurring task from the Scheduler
// modal's "New recurring task" button. Unlike update (which edits an existing
// task by id), this needs a target: agent_id (the rail's current agent) and
// session_id (the open thread), defaulting to the agent's home thread when none
// is open so an agent-level task always lands somewhere sensible. The per-session
// active-task cap is enforced inside ScheduleOrchestrateUpdate. POST JSON
// {agent_id, session_id, name, prompt, pattern, timing…}.
func (T *OrchestrateApp) handleConsoleRecurringCreate(c *jsonapi.Call, body struct {
	AgentID         string `json:"agent_id"`
	SessionID       string `json:"session_id"`
	Name            string `json:"name"`
	Prompt          string `json:"prompt"`
	Pattern         string `json:"pattern"`
	IntervalMinutes int    `json:"interval_minutes"`
	DailyAt         string `json:"daily_at"`
	TimesPerDay     int    `json:"times_per_day"`
	MinGapMinutes   int    `json:"min_gap_minutes"`
	MaxGapMinutes   int    `json:"max_gap_minutes"`
	ActiveFrom      string `json:"active_from"`
	ActiveTo        string `json:"active_to"`
	MaxFires        int    `json:"max_fires"`
	// Timing, when sent, is the shared timing section (core/timing) and
	// replaces the pattern fields above.
	Timing *timing.Spec `json:"timing"`
	// Until, when sent, is the dialog's Until section (schedule_until.go).
	Until *untilBody `json:"until"`
	// For is the "Who does it" picker: a sub-agent of agent_id that the
	// task is handed to, or "" for agent_id itself.
	For string `json:"for"`
}) (any, error) {
	user := c.User
	agentID := strings.TrimSpace(body.AgentID)
	if agentID == "" {
		return nil, jsonapi.BadRequest("agent_id required")
	}
	udb := UserDB(T.DB, user)
	if _, ok := loadAgent(udb, agentID); !ok {
		return nil, jsonapi.BadRequest("unknown agent")
	}
	if strings.TrimSpace(body.Prompt) == "" {
		return nil, jsonapi.BadRequest("directive (prompt) is required")
	}
	// Made for a sub-agent: the task is agent_id's, with the line that hands
	// it on, and reports in agent_id's cortex.
	if forID := strings.TrimSpace(body.For); forID != "" && forID != topAgentOf(udb, agentID) {
		sub, err := scheduleForName(udb, user, agentID, forID)
		if err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
		body.Prompt = withSubAgentHandoff(withoutSubAgentHandoff(strings.TrimSpace(body.Prompt)), sub)
	}
	sessionID := strings.TrimSpace(body.SessionID)
	if sessionID == "" {
		sessionID = cortexSessionID(agentID) // no open thread → attach to the agent's home thread
	}
	spec := RecurringSpec{
		SessionID:       sessionID,
		AgentID:         agentID,
		Username:        user,
		Prompt:          strings.TrimSpace(body.Prompt),
		Name:            strings.TrimSpace(body.Name),
		Pattern:         strings.ToLower(strings.TrimSpace(body.Pattern)),
		IntervalSeconds: body.IntervalMinutes * 60,
		TimesPerDay:     body.TimesPerDay,
		MinGapSeconds:   body.MinGapMinutes * 60,
		MaxGapSeconds:   body.MaxGapMinutes * 60,
		MaxFires:        body.MaxFires,
	}
	if body.Timing != nil {
		spec.Timing = *body.Timing
	}
	if body.Until != nil {
		if err := applyRecurringUntil(&spec, user, *body.Until); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
	}
	if spec.Pattern == RecurringDaily || (spec.Pattern == "" && strings.TrimSpace(body.DailyAt) != "") {
		mins, derr := parseDailyAt(body.DailyAt)
		if derr != nil {
			return nil, jsonapi.BadRequest(derr.Error())
		}
		spec.Pattern, spec.AtMinutes = RecurringDaily, mins
	}
	if spec.Pattern == "" {
		spec.Pattern = RecurringFixed
	}
	from := strings.TrimSpace(body.ActiveFrom)
	to := strings.TrimSpace(body.ActiveTo)
	if (from == "") != (to == "") {
		return nil, jsonapi.BadRequest("active_from and active_to must be set together")
	}
	if from != "" {
		fMin, ferr := parseHHMM(from)
		if ferr != nil {
			return nil, jsonapi.BadRequest(ferr.Error())
		}
		tMin, terr := parseHHMM(to)
		if terr != nil {
			return nil, jsonapi.BadRequest(terr.Error())
		}
		spec.HasWindow = true
		spec.WindowFromMin = fMin
		spec.WindowToMin = tMin
	}
	newID, err := ScheduleOrchestrateUpdate(spec)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	// Same pre-flight the recurring() tool reports, for the human-authored path:
	// what this task would be refused when it fires unattended. Advisory — the
	// task is created either way, and an empty warning means it's clear.
	resp := map[string]any{"id": newID}
	if findings := T.PreflightAutonomous(spec.Username, spec.AgentID); len(findings) > 0 {
		resp["warnings"] = findings
		resp["warning"] = PreflightSummary(findings)
	}
	return resp, nil
}
