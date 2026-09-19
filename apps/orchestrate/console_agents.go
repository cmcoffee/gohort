package orchestrate

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// handleConsoleAgentOptions lists the owner's agents as picker options
// ({value:id,label:name}) — the shared source for the "Relink" row action across
// the monitors / enabled-agents / recurring panes.
func (T *OrchestrateApp) handleConsoleAgentOptions(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	type opt struct {
		Value string `json:"value"`
		Label string `json:"label"`
	}
	opts := []opt{}
	// A relink picker for a schedule that runs a PIPELINE must offer
	// pipelines. The picker source is one URL for the whole column, so it
	// is told which row it is choosing for (?row=, added by openRowPicker)
	// and answers for that row's target kind. Offering agents there would
	// be worse than offering nothing: every choice in the list gets
	// refused by the relink handler.
	if row := strings.TrimSpace(r.URL.Query().Get("row")); row != "" {
		if sa, found := GetStandingAgent(RootDB, user, row); found && sa.TargetsMachine() {
			// A machine schedule relinks to a machine, and only to one that
			// can be run this way: offering a conversational machine would
			// be offering a choice the relink handler then refuses.
			for _, d := range ListMachineDefs(UserDB(T.DB, user), user) {
				if !d.Unattended {
					continue
				}
				label := strings.TrimSpace(d.Name)
				if label == "" {
					label = d.ID
				}
				opts = append(opts, opt{Value: d.ID, Label: label})
			}
			// A schedule can fire a machine somebody shared, so relinking one
			// has to be able to CHOOSE it. Labelled with the sharer: two people
			// can name a machine the same thing, and a picker that cannot tell
			// them apart is how a schedule gets pointed at the wrong one.
			for _, sm := range sharedMachinesFor(user) {
				if !sm.Def.Unattended {
					continue
				}
				label := strings.TrimSpace(sm.Def.Name)
				if label == "" {
					label = sm.Def.ID
				}
				opts = append(opts, opt{Value: sm.Def.ID, Label: label + " (shared by " + sm.Owner + ")"})
			}
			writeJSON(w, opts)
			return
		}
		if sa, found := GetStandingAgent(RootDB, user, row); found && sa.TargetsPipeline() {
			for _, d := range ListPipelineDefs(UserDB(T.DB, user), user) {
				label := strings.TrimSpace(d.Name)
				if label == "" {
					label = d.ID
				}
				opts = append(opts, opt{Value: d.ID, Label: label})
			}
			// A schedule can fire a pipeline somebody shared, so relinking one
			// has to be able to CHOOSE it. Labelled with the sharer: two people
			// can name a pipeline the same thing, and a picker that cannot tell
			// them apart is how a schedule gets pointed at the wrong one.
			for _, sp := range sharedPipelinesFor(user) {
				label := strings.TrimSpace(sp.Def.Name)
				if label == "" {
					label = sp.Def.ID
				}
				opts = append(opts, opt{Value: sp.Def.ID, Label: label + " (shared by " + sp.Owner + ")"})
			}
			writeJSON(w, opts)
			return
		}
	}
	// with_default=1 (the monitor relink picker only) leads with a "use the
	// deployment default" choice so relinking a monitor never REQUIRES naming a
	// specific agent — you pick one only when the persona matters. Standing/
	// recurring relink omit the param: those genuinely need a real target.
	if r.URL.Query().Get("with_default") == "1" {
		opts = append(opts, opt{Value: "__default__", Label: "Default agent (recommended)"})
	}
	for _, a := range listAgents(UserDB(T.DB, user), user) {
		// Relink targets are things a person may point work at: hidden
		// app-internal agents, clone-only templates, and retired seeds are not.
		if fleetHidden(a.ID) {
			continue
		}
		label := strings.TrimSpace(a.Name)
		if label == "" {
			label = a.ID
		}
		opts = append(opts, opt{Value: a.ID, Label: label})
	}
	writeJSON(w, opts)
}

// handleConsoleStandingMove sets a standing agent's Surface in place — same
// Cortex/Session/Background modes as monitors; its per-run report follows.
func (T *OrchestrateApp) handleConsoleStandingMove(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("id"))
	surface, valid := normalizeSurface(r.URL.Query().Get("value"))
	if !valid {
		http.Error(w, "move target must be cortex, session, or background", http.StatusBadRequest)
		return
	}
	sa, found := GetStandingAgent(RootDB, user, name)
	if !found {
		http.Error(w, "no such standing agent", http.StatusNotFound)
		return
	}
	sa.Surface = surface
	SaveStandingAgent(RootDB, sa)
	w.WriteHeader(http.StatusNoContent)
}

// handleConsoleAgentRelink re-points a broken standing agent at a live target
// agent and clears broken (leaves paused — an explicit Resume finishes recovery).
func (T *OrchestrateApp) handleConsoleAgentRelink(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("id"))
	value := strings.TrimSpace(r.URL.Query().Get("value"))
	// Load the schedule FIRST: what the value has to be depends on what
	// this schedule runs. Validating it as an agent before knowing that
	// is how a pipeline schedule ends up with an AgentID set alongside
	// its PipelineID — the one state ValidateTarget exists to refuse,
	// where whichever the runner checks first wins forever.
	sa, found := GetStandingAgent(RootDB, user, name)
	if !found {
		http.Error(w, "no such standing agent", http.StatusNotFound)
		return
	}
	if sa.TargetsMachine() {
		// The same resolver the picker offered from, so a choice the picker
		// listed cannot be refused here — an offer the handler rejects is
		// worse than no offer.
		def, ok := machineForUser(user, value)
		if !ok {
			http.Error(w, "no such machine", http.StatusBadRequest)
			return
		}
		// The same two refusals the runner makes, made HERE instead, so a
		// repair that would fail at 3am fails now with somebody reading it.
		if !def.Unattended {
			http.Error(w, "that machine converses rather than runs: a schedule fires with nobody there to answer a step that waits", http.StatusBadRequest)
			return
		}
		if probs := def.Problems(); len(probs) > 0 {
			http.Error(w, "that machine will not run yet: "+probs[0], http.StatusBadRequest)
			return
		}
		sa.MachineID = def.ID
		sa.Broken = false
		sa.BrokenReason = ""
		SaveStandingAgent(RootDB, sa)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if sa.TargetsPipeline() {
		// pipelineForUser, not the own-store load: the picker above offers
		// pipelines somebody shared, and this refused every one of them.
		def, ok := pipelineForUser(user, value)
		if !ok {
			http.Error(w, "no such pipeline", http.StatusBadRequest)
			return
		}
		// A pipeline that would not run is not a repair. Refusing here
		// keeps the schedule broken-and-visible rather than pointing it at
		// something that fails on its next fire.
		if err := def.Validate(); err != nil {
			http.Error(w, "that pipeline would not run: "+err.Error(), http.StatusBadRequest)
			return
		}
		sa.PipelineID = def.ID
		sa.Broken = false
		sa.BrokenReason = ""
		SaveStandingAgent(RootDB, sa)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if _, ok := loadAgent(UserDB(T.DB, user), value); !ok {
		http.Error(w, "no such agent", http.StatusBadRequest)
		return
	}
	sa.AgentID = value
	sa.Broken = false
	sa.BrokenReason = ""
	SaveStandingAgent(RootDB, sa)
	w.WriteHeader(http.StatusNoContent)
}

// handleConsoleAgentGet returns a standing agent's editable record for the
// Scheduler edit modal — the mission as well as the timing, because "this runs
// at the wrong time" and "this runs doing the wrong thing" are the same
// complaint from the same page.
func (T *OrchestrateApp) handleConsoleAgentGet(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	sa, found := GetStandingAgent(RootDB, user, strings.TrimSpace(r.URL.Query().Get("id")))
	if !found {
		http.Error(w, "no such standing agent", http.StatusNotFound)
		return
	}
	startAt := ""
	if !sa.StartAt.IsZero() {
		startAt = sa.StartAt.Format(time.RFC3339)
	}
	writeJSON(w, map[string]any{
		"name":             sa.Name,
		"mission":          sa.Mission,
		"cron":             sa.Cron,
		"interval_seconds": sa.IntervalSeconds,
		"interval_minutes": sa.IntervalSeconds / 60,
		"start_at":         startAt,
		"schedule_label":   StandingScheduleLabel(sa),
		"paused":           sa.Paused,
		// What this schedule actually runs. Empty for the ordinary case (the
		// agent's own mission); "pipeline · <name>" or "machine · <name>" when
		// it drives one of those, where the mission is the run's INPUT rather
		// than a brief — the editor labels the field differently for each, so
		// it has to know which it is looking at.
		"runs":        standingRunsLabel(user, sa),
		"targets_run": sa.TargetsPipeline() || sa.TargetsMachine(),
	})
}

// handleConsoleAgentUpdate edits a standing agent's schedule (a cron spec OR an
// interval) AND its mission in place, then re-arms it. Cron takes precedence
// when set, matching StandingAgent semantics. A bad schedule (e.g. unparseable
// cron) is rejected and the original restored so an edit never strands the
// agent. POST ?id=<name> with {cron} and/or {interval_minutes}, and optionally
// {mission}.
//
// Mission is a POINTER: absent preserves what is stored, which is what the
// timing-only callers (and anything older than this field) send. Sent empty is
// REFUSED rather than treated as "clear it" — a standing agent whose mission is
// blank fires on its schedule with nothing to do, so a stray empty textarea
// would quietly turn a working schedule into a recurring no-op.
func (T *OrchestrateApp) handleConsoleAgentUpdate(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sa, found := GetStandingAgent(RootDB, user, strings.TrimSpace(r.URL.Query().Get("id")))
	if !found {
		http.Error(w, "no such standing agent", http.StatusNotFound)
		return
	}
	var body standingUpdateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
		return
	}
	before := sa
	if err := applyStandingUpdate(&sa, body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// A destination nobody chose takes the agent's default on edit, same as on
	// create: a cortex controller reads its scheduled runs in its cortex. An
	// explicit choice (stored, including "session") is left alone.
	sa.Surface = scheduleSurfaceDefault(sa.Surface, hasCortexThread(user, sa.ReportAgentID))
	var err error
	if sa.Paused {
		SaveStandingAgent(RootDB, sa)
	} else {
		err = ScheduleStandingAgent(RootDB, sa)
	}
	if err != nil {
		// Restore so a rejected edit doesn't strand the agent. The whole record
		// goes back, mission included: a schedule that would not arm must not
		// be left half-applied, holding the new brief and the old timing.
		if before.Paused {
			SaveStandingAgent(RootDB, before)
		} else {
			_ = ScheduleStandingAgent(RootDB, before)
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// standingUpdateBody is what the Scheduler's scheduled-agent editor posts.
type standingUpdateBody struct {
	Cron            string `json:"cron"`
	IntervalMinutes int    `json:"interval_minutes"`
	// Mission is a POINTER: absent preserves what is stored, which is what the
	// timing-only callers (and anything written before the field existed)
	// send. Sent EMPTY is refused rather than read as "clear it" — a standing
	// agent with a blank mission still fires on its schedule, with nothing to
	// do, so a stray empty textarea would quietly turn a working schedule into
	// a recurring no-op.
	Mission *string `json:"mission"`
}

// applyStandingUpdate writes the posted schedule and mission onto sa, refusing
// anything the fire path could not act on. Validation runs BEFORE any
// assignment, so a rejected body leaves the caller's record untouched and the
// handler has an original to restore.
func applyStandingUpdate(sa *StandingAgent, body standingUpdateBody) error {
	cron := strings.TrimSpace(body.Cron)
	secs := body.IntervalMinutes * 60
	if cron == "" && secs <= 0 {
		return Error("set a cron schedule or an interval (minutes)")
	}
	mission := sa.Mission
	if body.Mission != nil {
		if mission = strings.TrimSpace(*body.Mission); mission == "" {
			return Error("a scheduled agent needs a mission: it is what the agent is handed on every run")
		}
	}
	sa.Mission = mission
	if cron != "" {
		sa.Cron, sa.IntervalSeconds = cron, 0
	} else {
		sa.Cron, sa.IntervalSeconds = "", secs
	}
	return nil
}

type consoleAgentRow struct {
	Name string `json:"name"`
	// Mission is the standing brief handed to the agent each run — "what it's
	// told to do." Surfaced so the Enabled-agents view shows each agent's
	// instructions, not just its schedule/status. Renders as a detail line
	// under the name in the cards layout.
	Mission string `json:"mission,omitempty"`
	// Runs says WHAT fires, not just that something is scheduled. A row
	// reading "every 24h" is the same sentence whether it runs an agent, a
	// pipeline or a machine, and the three behave differently enough that the
	// list should not make somebody open one to find out. Empty for the
	// ordinary case — an agent running its own mission, which the name already
	// says. Lifted off the Schedules rail when that was retired; it was the
	// one thing the rail's rows said that these did not.
	Runs  string `json:"runs,omitempty"`
	State string `json:"state"` // active | paused
	// Objective is where this schedule's goal stands, in the judge's own words,
	// and Failing is whether it is in trouble. Both were readable on the other
	// two scheduling surfaces and on neither of the standing agent's — so the
	// one kind whose whole point is to run unattended was the one that said
	// least about how that was going. See schedule_row_state.go.
	Objective string `json:"objective,omitempty"`
	Failing   string `json:"failing,omitempty"`
	Schedule  string `json:"schedule"`
	Status    string `json:"status"`
	NextRun   string `json:"next_run"`
	ID        string `json:"_id"`               // hidden; row-action target (the agent name)
	Paused    bool   `json:"_paused"`           // hidden; gates Pause vs Resume per row
	Broken    bool   `json:"_broken,omitempty"` // hidden; parked and kept — see State for which kind
	// Relinkable gates the Relink row action: true only when something the
	// schedule needs is GONE. An objective that stalled needs attempts, not a
	// new target, and offering it a picker of live agents describes a problem
	// it does not have.
	Relinkable bool `json:"_relinkable,omitempty"`
}

// handleConsoleAgents lists the owner's standing (scheduled) agents, each
// joined with the status of its most recent run.
func (T *OrchestrateApp) handleConsoleAgents(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
	writeJSON(w, consoleAgentRows(user, udb, agentID))
}

// consoleAgentRows builds the rows for this view. Split off the handler so the
// merged Scheduler page (console_scheduler.go) renders THESE rows rather than
// its own copy of the same logic — a second builder is how the two views
// come to disagree about what is scheduled.
func consoleAgentRows(user string, udb Database, agentID string) []consoleAgentRow {
	rows := []consoleAgentRow{}
	for _, sa := range ListStandingAgents(RootDB, user) {
		// Both the agent that runs it and the one that manages it — see
		// standingAgentOnRailOf. Either alone leaves a blind spot.
		if !standingAgentOnRailOf(sa, agentID) {
			continue
		}
		state := "active"
		if lbl := scheduleStopLabel(StandingStopCause(sa), StandingStopNote(sa)); lbl != "" {
			state = lbl
		}
		row := consoleAgentRow{Name: sa.Name, Mission: sa.Mission, State: state, Schedule: StandingScheduleLabel(sa), Runs: standingRunsLabel(user, sa), ID: sa.Name, Paused: sa.Paused}
		if sa.Broken {
			row.Broken = true
			row.State = parkedStateLabel(StandingParkCause(sa), sa.BrokenReason)
			// Relink is offered only where relinking is the repair. A stalled
			// objective keeps Resume, which gives it a fresh allowance.
			row.Relinkable = RelinkFixesIt(StandingParkCause(sa))
		}
		if !sa.NextRun.IsZero() {
			row.NextRun = sa.NextRun.UTC().Format(time.RFC3339)
		}
		// Where the goal stands, and whether the runs are working. A standing
		// agent has no park bound on a failing streak — it backs off instead,
		// so the label carries the time it backed off TO rather than a count to
		// stop at.
		row.Objective = objectiveStateLabel(standingObjective(sa))
		row.Failing = scheduleFailingLabel(sa.ConsecutiveFailures, 0, sa.NextAttemptAt, UserLocation(user))
		// By identity, with the display name as the legacy fallback for runs
		// recorded before subjects existed (see RunFilter.Subject).
		if latest := ListRuns(RootDB, user, RunFilter{Limit: 1}.AboutStanding(sa.Name)); len(latest) > 0 {
			row.Status = string(latest[0].Status)
		}
		// Warn only while a subject-less run could actually be crossing the two.
		// New runs carry a subject and cannot; saying otherwise would send
		// someone renaming a pair that is already fine, and the warning would
		// never go away.
		if clash, ok := standingNameCollision(udb, user, sa); ok && hasLegacyRunsNamed(user, sa.Name) {
			row.State = "⚠ shares a name with the agent " + chFirst(clash.Name, clash.ID) +
				", and runs recorded before this release cannot tell them apart: rename one, or wait for those runs to age out"
		}
		rows = append(rows, row)
	}
	return rows
}

// standingNameCollision reports the agent whose Name is the same as this
// standing agent's, if there is one.
//
// It matters because the run ledger's Agent field is a shared, free-text
// namespace — "standing-agent / job name", per its own comment — and different
// writers put different things in it: a machine run and a guardrail run record
// an agent's NAME, agent CRUD records an agent's ID, and a standing run records
// the schedule's name. So looking up a standing agent's history with
// RunFilter{Agent: sa.Name} matches any run filed under that same text,
// including every run of an agent that happens to be called that.
//
// The result is a status and a last-run that belong to something else, shown
// without a hint that anything is wrong — a wrong answer being strictly worse
// than no answer, since nothing about the row invites doubt.
//
// Detection lives HERE, at the read, rather than as a guard at creation:
// renaming is the user's call, an agent can be renamed into a collision long
// after the schedule was made, and a check that only ran at creation would
// never see it. Matching is case-insensitive and trims, because the ledger
// lookup is exact — a pair that differs only in case does NOT actually cross,
// and flagging it would be a false alarm. Those are excluded below.
// hasLegacyRunsNamed reports whether any run filed under this display name
// predates RunRecord.Subject — the only runs a name collision can still cross.
// Every run written since carries an identity, so the ambiguity is bounded and
// drains as pruneRuns ages those rows out.
func hasLegacyRunsNamed(owner, name string) bool {
	for _, r := range ListRuns(RootDB, owner, RunFilter{Agent: name}) {
		if r.Subject == "" {
			return true
		}
	}
	return false
}

func standingNameCollision(udb Database, owner string, sa StandingAgent) (AgentRecord, bool) {
	name := strings.TrimSpace(sa.Name)
	if udb == nil || name == "" {
		return AgentRecord{}, false
	}
	for _, a := range listAgents(udb, owner) {
		// Exact, because that is what ListRuns compares.
		if strings.TrimSpace(a.Name) == name {
			return a, true
		}
	}
	return AgentRecord{}, false
}

// handleConsoleAgentDelete removes a standing agent (and cancels its schedule)
// from the Enabled-agents pane's row Delete button. id = the agent's name.
func (T *OrchestrateApp) handleConsoleAgentDelete(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("id"))
	if name == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	DeleteStandingAgent(RootDB, user, name)
	w.WriteHeader(http.StatusNoContent)
}

// setConsoleAgentPaused pauses or resumes a standing agent — the same logic as
// the set_standing_paused tool, behind the Enabled-agents pane's Pause/Resume
// row buttons. Idempotent, so a Pause click on an already-paused agent (or vice
// versa) is harmless. id = the agent's name.
func (T *OrchestrateApp) setConsoleAgentPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("id"))
	sa, found := GetStandingAgent(RootDB, user, name)
	if !found {
		http.Error(w, "no such standing agent", http.StatusNotFound)
		return
	}
	sa.Paused = paused
	if paused {
		sa.StopCause, sa.StopNote = StoppedByOwner, ""
	} else {
		sa.StopCause, sa.StopNote = "", ""
	}
	if paused {
		if sa.SchedulerID != "" {
			UnscheduleTask(sa.SchedulerID)
			sa.SchedulerID = ""
			sa.NextRun = time.Time{}
		}
		SaveStandingAgent(RootDB, sa)
	} else {
		// Resume from broken is gated: only proceed once the target agent is back,
		// clearing the broken flag on success (see the monitor path for rationale).
		if sa.Broken {
			if reason := standingAgentDependencyError(sa); reason != "" {
				http.Error(w, "can't resume: "+reason+"; relink it to a live agent or delete it", http.StatusConflict)
				return
			}
			sa.Broken = false
			sa.BrokenReason = ""
		}
		SaveStandingAgent(RootDB, sa)
		_ = ScheduleStandingAgent(RootDB, sa)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (T *OrchestrateApp) handleConsoleAgentPause(w http.ResponseWriter, r *http.Request) {
	T.setConsoleAgentPaused(w, r, true)
}

func (T *OrchestrateApp) handleConsoleAgentResume(w http.ResponseWriter, r *http.Request) {
	T.setConsoleAgentPaused(w, r, false)
}

// handleConsoleAgentRun fires a standing agent's run immediately from the
// Enabled-agents pane's "Run now" button — a one-off manual test that does NOT
// disturb the recurring schedule (RunStandingAgentNow stamps trigger "manual").
// The run executes off-request in a goroutine (an agent run can take a while and
// must outlive the response), with a background context so it isn't cancelled
// when the HTTP handler returns; the outcome reports back through the standing
// reporter, and the run ledger updates the row's status on reload. id = the
// agent's name. Returns 202 once the run is launched.
func (T *OrchestrateApp) handleConsoleAgentRun(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("id"))
	if _, found := GetStandingAgent(RootDB, user, name); !found {
		http.Error(w, "no such standing agent", http.StatusNotFound)
		return
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				Log("[orchestrate/console] run-now panicked for standing agent %s/%s: %v", user, name, rec)
			}
		}()
		RunStandingAgentNow(context.Background(), RootDB, user, name)
	}()
	w.WriteHeader(http.StatusAccepted)
}

// standingRunsLabel names the TARGET a schedule fires, or "" when it is the
// agent's own mission and the row already says so.
//
// A shared pipeline or machine names its owner too: a schedule that suddenly
// stops working because somebody else changed or unshared the thing it runs is
// otherwise a mystery with no thread to pull.
func standingRunsLabel(user string, sa StandingAgent) string {
	switch {
	case sa.TargetsPipeline():
		if def, ok := pipelineForUser(user, sa.PipelineID); ok {
			return "pipeline · " + def.Name + sharedBySuffix(user, def.Owner)
		}
		return "pipeline run"
	case sa.TargetsMachine():
		if def, ok := machineForUser(user, sa.MachineID); ok {
			return "machine · " + def.Name + sharedBySuffix(user, def.Owner)
		}
		return "machine run"
	}
	return ""
}

// sharedBySuffix names the owner of something running on somebody else's
// behalf, and nothing at all for your own.
func sharedBySuffix(user, owner string) string {
	if owner == "" || owner == user {
		return ""
	}
	return " (shared by " + owner + ")"
}
