package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func (T *OrchestrateApp) handleSchedules(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
	rows := []map[string]any{}
	for _, m := range ListEventMonitors(RootDB, user) {
		wake := m.WakeAgent
		if wake == "" {
			wake = "seed-chat"
		}
		if agentID != "" && wake != agentID {
			continue
		}
		// Bridges have their own app (and are push-oriented, not polled
		// schedules), so they're excluded from the unified Scheduler view.
		if isBridgeMonitor(m) {
			continue
		}
		kind := string(m.Kind)
		id := url.QueryEscape(m.Name)
		row := map[string]any{
			"name":           m.Name,
			"detail":         fmt.Sprintf("%s · every %ds", kind, m.IntervalSeconds),
			"paused":         m.Paused,
			"pause_url":      "api/console/monitors/pause?id=" + id,
			"resume_url":     "api/console/monitors/resume?id=" + id,
			"delete_url":     "api/console/monitors/delete?id=" + id,
			"category":       "monitor",
			"category_label": "Event monitors",
		}
		// A monitor that has stopped says why on its own row, with the same
		// mark the channel rail uses.
		if st := monitorRowState(m); st != nil {
			row["state"] = st
		}
		// Schedulable kinds (poll / http_poll / watch) get a click-to-edit-interval
		// modal; webhook monitors are push-only, so no edit affordance.
		if IsScheduledEventKind(m.Kind) {
			row["id"] = m.Name
			row["edit_action"] = "orchestrate_edit_monitor"
		}
		rows = append(rows, row)
	}
	for _, sa := range ListStandingAgents(RootDB, user) {
		// Scope to the CONTROLLER that created + manages the task (ReportAgentID) —
		// the same owner the "Enabled agents" card uses (handleConsoleAgents) — so a
		// task that RUNS as a sub-agent shows on the PARENT's Scheduler rail where
		// it's managed, not on the sub-agent that merely executes it. Fall back to
		// the runner AgentID for legacy records that carry no controller, so they
		// still surface somewhere.
		if !standingAgentOnRailOf(sa, agentID) {
			continue
		}
		// Say what it RUNS, not just that it is scheduled. A row reading
		// "scheduled run · every 24h" is the same sentence whether it fires
		// an agent or a pipeline, and the two behave differently enough
		// that the rail should not make somebody open it to find out.
		what := "scheduled run"
		switch {
		case sa.TargetsPipeline():
			what = "pipeline run"
			if def, ok := pipelineForUser(user, sa.PipelineID); ok {
				what = "pipeline · " + def.Name
				if def.Owner != "" && def.Owner != user {
					what += " (shared by " + def.Owner + ")"
				}
			}
		case sa.TargetsMachine():
			what = "machine run"
			if def, ok := machineForUser(user, sa.MachineID); ok {
				what = "machine · " + def.Name
				if def.Owner != "" && def.Owner != user {
					what += " (shared by " + def.Owner + ")"
				}
			}
		}
		id := url.QueryEscape(sa.Name)
		rows = append(rows, map[string]any{
			"name":           sa.Name,
			"detail":         what + " · " + StandingScheduleLabel(sa) + surfaceSuffix(sa.Surface) + standingRoleSuffix(udb, sa, agentID),
			"paused":         sa.Paused,
			"pause_url":      "api/console/agents/pause?id=" + id,
			"resume_url":     "api/console/agents/resume?id=" + id,
			"delete_url":     "api/console/agents/delete?id=" + id,
			"category":       "standing",
			"category_label": "Scheduled agents",
			"id":             sa.Name,
			"edit_action":    "orchestrate_edit_standing",
		})
	}
	// Recurring tasks (the `recurring` tool → per-session scheduled updates).
	// Delete-only: these have no pause concept, so the row omits pause/resume
	// URLs and the rail renders just name + detail + ×. The label is the prompt's
	// first line (they carry no user-set name). Scoped to the agent that runs
	// them (AgentID), matching the monitor / standing-run scoping above.
	for _, rt := range listAgentRecurringTasks(user, agentID) {
		label := firstLineLabel(rt.Payload.Prompt)
		if label == "" {
			label = "recurring task"
		}
		rows = append(rows, map[string]any{
			"name":           label,
			"detail":         recurringDetail(rt.Payload) + surfaceSuffix(rt.Payload.Surface),
			"paused":         false,
			"delete_url":     "api/console/recurring/delete?id=" + url.QueryEscape(rt.TaskID),
			"id":             rt.TaskID,
			"edit_action":    "orchestrate_edit_schedule",
			"category":       "recurring",
			"category_label": "Recurring tasks",
		})
	}
	writeJSON(w, rows)
}

type consoleRecurringRow struct {
	// Recurring tasks carry no user-set name — the label is the prompt's first
	// line (matching the Schedules rail). It renders as the card title.
	Name    string `json:"name"`
	Cadence string `json:"cadence"`            // human cadence ("recurring · every 30m")
	Fires   string `json:"fires,omitempty"`    // "<fired> / <cap>" so far
	NextRun string `json:"next_run,omitempty"` // RFC3339 next fire (matches consoleAgentRow)
	State   string `json:"state,omitempty"`    // visible only when broken ("⚠ needs relink — …")
	ID      string `json:"_id"`                // hidden; row-action target (the scheduler task id)
	Broken  bool   `json:"_broken,omitempty"`  // hidden gate (Delete-only on a broken row)
	// Relinkable gates the Relink row action: only a schedule whose target is
	// GONE has anything to relink. A stalled objective gets Resume instead.
	Relinkable bool `json:"_relinkable,omitempty"`
}

// handleConsoleRecurring lists the owner's recurring tasks (the `recurring` tool
// → per-session scheduled updates) for the Recurring-tasks nav card view — the
// status-card sibling of Enabled agents / Event monitors. Scoped to the agent
// that runs them (AgentID), matching the other two panes. Recurring tasks have
// no pause concept, so the only row action is Delete.
func (T *OrchestrateApp) handleConsoleRecurring(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
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
			Cadence: recurringDetail(rt.Payload) + surfaceSuffix(rt.Payload.Surface),
			Fires:   fires,
			NextRun: rt.RunAt,
			ID:      rt.TaskID,
		}
		// Where an objective stands, for the rows that have a goal. Broken wins
		// below: a parked task's reason is the more urgent thing to read, and
		// for a stalled objective it already names the goal's last verdict.
		row.State = objectiveStateLabel(rt.Payload.objective())
		if rt.Payload.Broken {
			row.Broken = true
			row.State = parkedStateLabel(recurringParkCause(rt.Payload), rt.Payload.BrokenReason)
			row.Relinkable = recurringParkCause(rt.Payload) == ParkedByDependency
			row.NextRun = "" // parked: the dormant re-check isn't a real next run
		}
		rows = append(rows, row)
	}
	writeJSON(w, rows)
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
func (T *OrchestrateApp) handleConsoleRecurringResume(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	// Ownership by payload username, the same way delete and relink enforce it:
	// the scheduler bucket is global and keyed by opaque UUID.
	for _, rt := range listAgentRecurringTasks(user, "") {
		if rt.TaskID != id {
			continue
		}
		UnscheduleTask(id)
		p := rt.Payload
		p.Broken = false
		p.BrokenReason = ""
		p.AttemptsBase = p.FireCount
		p.LastActive = time.Now().Format(time.RFC3339)
		next, err := computeNextFire(&p, time.Now().In(UserLocation(user)))
		if err != nil {
			http.Error(w, "resumed but couldn't schedule: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := ScheduleTask(OrchestrateScheduledUpdateKind, p, next); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		Log("[orchestrate/objective] task %q resumed by %s — allowance restarts at fire %d", recurringName(p), user, p.FireCount)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Error(w, "recurring task not found", http.StatusNotFound)
}

// handleConsoleRecurringDelete removes a recurring task by scheduler id from the
// per-agent schedules rail. The scheduler bucket is global and keyed by opaque
// UUID, so ownership is enforced by matching the task's payload username (via
// listAgentRecurringTasks) before unscheduling — a user can't cancel another
// user's task by guessing its id.
func (T *OrchestrateApp) handleConsoleRecurringDelete(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	for _, rt := range listAgentRecurringTasks(user, "") {
		if rt.TaskID == id {
			UnscheduleTask(id)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	http.Error(w, "recurring task not found", http.StatusNotFound)
}

// handleConsoleRecurringMove sets a recurring task's Surface in place. Recurring
// lives only as a scheduler entry, so this re-arms it with the updated payload
// (mirrors relink). The home session (SessionID) is left intact.
func (T *OrchestrateApp) handleConsoleRecurringMove(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	surface, valid := normalizeSurface(r.URL.Query().Get("value"))
	if !valid {
		http.Error(w, "move target must be cortex, session, or background", http.StatusBadRequest)
		return
	}
	for _, rt := range listAgentRecurringTasks(user, "") {
		if rt.TaskID != id {
			continue
		}
		UnscheduleTask(id)
		p := rt.Payload
		p.Surface = surface
		p.LastActive = time.Now().Format(time.RFC3339)
		next, err := computeNextFire(&p, time.Now().In(UserLocation(user)))
		if err != nil {
			http.Error(w, "moved but couldn't reschedule: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := ScheduleTask(OrchestrateScheduledUpdateKind, p, next); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.NotFound(w, r)
}

// handleConsoleRecurringRelink re-points a parked (broken) recurring task at a
// live agent and puts it straight back on its real cadence. Recurring has no
// pause/resume concept (unlike monitors/standing), so relink resumes it
// directly; LastActive is refreshed so the idle guard doesn't immediately reap a
// just-relinked task.
func (T *OrchestrateApp) handleConsoleRecurringRelink(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	newAgent := strings.TrimSpace(r.URL.Query().Get("value"))
	if _, ok := loadAgent(UserDB(T.DB, user), newAgent); !ok {
		http.Error(w, "no such agent", http.StatusBadRequest)
		return
	}
	for _, rt := range listAgentRecurringTasks(user, "") {
		if rt.TaskID != id {
			continue
		}
		UnscheduleTask(id)
		p := rt.Payload
		p.AgentID = newAgent
		p.Broken = false
		p.BrokenReason = ""
		p.LastActive = time.Now().Format(time.RFC3339)
		next, err := computeNextFire(&p, time.Now().In(UserLocation(user)))
		if err != nil {
			http.Error(w, "relinked but couldn't schedule: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := ScheduleTask(OrchestrateScheduledUpdateKind, p, next); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Error(w, "recurring task not found", http.StatusNotFound)
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
func (T *OrchestrateApp) handleConsoleRecurringRun(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	found := false
	for _, rt := range listAgentRecurringTasks(user, "") {
		if rt.TaskID == id {
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "recurring task not found", http.StatusNotFound)
		return
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
	w.WriteHeader(http.StatusAccepted)
}

// handleConsoleRecurringGet returns one recurring task's editable fields for the
// rail's edit modal. Owner-checked via listAgentRecurringTasks. GET ?id=<id>.
func (T *OrchestrateApp) handleConsoleRecurringGet(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	for _, rt := range listAgentRecurringTasks(user, "") {
		if rt.TaskID != id {
			continue
		}
		p := rt.Payload
		pattern := p.Pattern
		if pattern == "" {
			pattern = RecurringFixed
		}
		writeJSON(w, map[string]any{
			"id":               rt.TaskID,
			"name":             recurringName(p),
			"prompt":           p.Prompt,
			"pattern":          pattern,
			"interval_minutes": p.IntervalSeconds / 60,
			"times_per_day":    p.TimesPerDay,
			"min_gap_minutes":  p.MinGapSeconds / 60,
			"max_gap_minutes":  p.MaxGapSeconds / 60,
			"has_window":       p.HasWindow,
			"active_from":      fmtHHMM(p.WindowFromMin),
			"active_to":        fmtHHMM(p.WindowToMin),
			"max_fires":        p.MaxFires,
			"fire_count":       p.FireCount,
			"cadence":          recurringDetail(p),
		})
		return
	}
	http.Error(w, "recurring task not found", http.StatusNotFound)
}

// handleConsoleRecurringUpdate edits a recurring task's schedule in place: rebuild
// the spec from the stored task (session / agent / prompt preserved) with the
// posted timing, unschedule the old, schedule the edited one. On a validation
// error the original is restored so a bad edit never destroys the task. POST
// ?id=<id> with a JSON timing body.
func (T *OrchestrateApp) handleConsoleRecurringUpdate(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
		http.Error(w, "recurring task not found", http.StatusNotFound)
		return
	}
	var body struct {
		Pattern         string `json:"pattern"`
		IntervalMinutes int    `json:"interval_minutes"`
		TimesPerDay     int    `json:"times_per_day"`
		MinGapMinutes   int    `json:"min_gap_minutes"`
		MaxGapMinutes   int    `json:"max_gap_minutes"`
		ActiveFrom      string `json:"active_from"`
		ActiveTo        string `json:"active_to"`
		MaxFires        int    `json:"max_fires"`
		// Pointers so "field omitted" (nil = preserve the stored value) is
		// distinguishable from "sent empty". Prompt is required, so an empty
		// prompt preserves too; Name may legitimately be cleared (empty falls
		// back to the prompt's first line at render).
		Prompt *string `json:"prompt"`
		Name   *string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Directive + name are editable now (not just timing). Base = the stored
	// values; a provided prompt (non-empty; prompt is required) or name overrides.
	// Computed into LOCALS, not mutated onto found — the rejected-edit restore path
	// below re-schedules *found and must keep the original directive/name intact.
	prompt, name := found.Prompt, found.Name
	if body.Prompt != nil {
		if p := strings.TrimSpace(*body.Prompt); p != "" {
			prompt = p
		}
	}
	if body.Name != nil {
		name = strings.TrimSpace(*body.Name)
	}
	spec := RecurringSpec{
		SessionID: found.SessionID,
		AgentID:   found.AgentID,
		Username:  found.Username,
		Prompt:    prompt,
		Name:      name,
		// An edit re-schedules the task, so the destination has to travel with
		// it or a retime would silently send the reports somewhere else. A
		// surface the user actually chose is preserved verbatim; an unchosen one
		// takes the agent's default (its cortex, when it has one).
		Surface:         scheduleSurfaceDefault(found.Surface, hasCortexThread(user, found.AgentID)),
		FireCount:       found.FireCount, // preserve run history across an edit (don't reset the budget)
		CreatedAt:       found.CreatedAt, // keep the original creation time, not "now"
		Pattern:         strings.ToLower(strings.TrimSpace(body.Pattern)),
		IntervalSeconds: body.IntervalMinutes * 60,
		TimesPerDay:     body.TimesPerDay,
		MinGapSeconds:   body.MinGapMinutes * 60,
		MaxGapSeconds:   body.MaxGapMinutes * 60,
		MaxFires:        body.MaxFires,
	}
	if spec.Pattern == "" {
		spec.Pattern = RecurringFixed
	}
	from := strings.TrimSpace(body.ActiveFrom)
	to := strings.TrimSpace(body.ActiveTo)
	if (from == "") != (to == "") {
		http.Error(w, "active_from and active_to must be set together", http.StatusBadRequest)
		return
	}
	if from != "" {
		fMin, ferr := parseHHMM(from)
		if ferr != nil {
			http.Error(w, ferr.Error(), http.StatusBadRequest)
			return
		}
		tMin, terr := parseHHMM(to)
		if terr != nil {
			http.Error(w, terr.Error(), http.StatusBadRequest)
			return
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
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"id": newID})
}

// handleConsoleRecurringCreate creates a NEW recurring task from the Scheduler
// modal's "New recurring task" button. Unlike update (which edits an existing
// task by id), this needs a target: agent_id (the rail's current agent) and
// session_id (the open thread), defaulting to the agent's home thread when none
// is open so an agent-level task always lands somewhere sensible. The per-session
// active-task cap is enforced inside ScheduleOrchestrateUpdate. POST JSON
// {agent_id, session_id, name, prompt, pattern, timing…}.
func (T *OrchestrateApp) handleConsoleRecurringCreate(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		AgentID         string `json:"agent_id"`
		SessionID       string `json:"session_id"`
		Name            string `json:"name"`
		Prompt          string `json:"prompt"`
		Pattern         string `json:"pattern"`
		IntervalMinutes int    `json:"interval_minutes"`
		TimesPerDay     int    `json:"times_per_day"`
		MinGapMinutes   int    `json:"min_gap_minutes"`
		MaxGapMinutes   int    `json:"max_gap_minutes"`
		ActiveFrom      string `json:"active_from"`
		ActiveTo        string `json:"active_to"`
		MaxFires        int    `json:"max_fires"`
		// Where its fires report — "" (let the agent decide), "session",
		// "cortex", or "background". The modal doesn't offer it today; the
		// field is here so a client that wants to choose can, and so the
		// default below is the only thing an omission relies on.
		Surface string `json:"surface"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
		return
	}
	agentID := strings.TrimSpace(body.AgentID)
	if agentID == "" {
		http.Error(w, "agent_id required", http.StatusBadRequest)
		return
	}
	if _, ok := loadAgent(UserDB(T.DB, user), agentID); !ok {
		http.Error(w, "unknown agent", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Prompt) == "" {
		http.Error(w, "directive (prompt) is required", http.StatusBadRequest)
		return
	}
	sessionID := strings.TrimSpace(body.SessionID)
	if sessionID == "" {
		sessionID = cortexSessionID(agentID) // no open thread → attach to the agent's home thread
	}
	surface, valid := normalizeSurface(body.Surface)
	if !valid {
		http.Error(w, "surface must be cortex, session, or background", http.StatusBadRequest)
		return
	}
	// Unchosen → the agent's default: its cortex when it has one. The home
	// session stays the open thread either way, so Move-to → Session returns it.
	surface = scheduleSurfaceDefault(surface, hasCortexThread(user, agentID))
	spec := RecurringSpec{
		SessionID:       sessionID,
		Surface:         surface,
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
	if spec.Pattern == "" {
		spec.Pattern = RecurringFixed
	}
	from := strings.TrimSpace(body.ActiveFrom)
	to := strings.TrimSpace(body.ActiveTo)
	if (from == "") != (to == "") {
		http.Error(w, "active_from and active_to must be set together", http.StatusBadRequest)
		return
	}
	if from != "" {
		fMin, ferr := parseHHMM(from)
		if ferr != nil {
			http.Error(w, ferr.Error(), http.StatusBadRequest)
			return
		}
		tMin, terr := parseHHMM(to)
		if terr != nil {
			http.Error(w, terr.Error(), http.StatusBadRequest)
			return
		}
		spec.HasWindow = true
		spec.WindowFromMin = fMin
		spec.WindowToMin = tMin
	}
	newID, err := ScheduleOrchestrateUpdate(spec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Same pre-flight the recurring() tool reports, for the human-authored path:
	// what this task would be refused when it fires unattended. Advisory — the
	// task is created either way, and an empty warning means it's clear.
	resp := map[string]any{"id": newID}
	if findings := T.PreflightAutonomous(spec.Username, spec.AgentID); len(findings) > 0 {
		resp["warnings"] = findings
		resp["warning"] = PreflightSummary(findings)
	}
	writeJSON(w, resp)
}
