// Pointing a schedule at a machine, from the page rather than by asking
// an agent to do it.
//
// Standing agents could only ever be CREATED by the create_standing_agent
// tool: the Scheduler rail lists them, relinks them, pauses and deletes
// them, and had no door to make one. That was survivable while every
// schedule ran an agent — you were talking to an agent anyway — and it
// stopped being survivable once a schedule could run a machine, because
// "run this machine every night" is a thing somebody wants while looking
// at the machine, not while in conversation.
//
// Deliberately NOT a general standing-agent form. An agent schedule wants
// a mission, a controller, a report target and a tool posture; a machine
// schedule wants a machine, a subject and a cadence. This is the second
// one, and it says so.

package orchestrate

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// handleConsoleMachineOptions lists the machines a schedule may fire: this
// user's own, and only the ones marked to RUN.
//
//	GET /api/console/machine-options → [{value, label}]
func (T *OrchestrateApp) handleConsoleMachineOptions(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	type opt struct {
		Value string `json:"value"`
		Label string `json:"label"`
	}
	opts := []opt{}
	for _, d := range ListMachineDefs(udb, user) {
		if !d.Unattended {
			// A conversational machine has a step that waits for a person,
			// and a schedule fires with nobody there. Offering it would be
			// offering a choice the create below refuses.
			continue
		}
		label := strings.TrimSpace(d.Name)
		if label == "" {
			label = d.ID
		}
		opts = append(opts, opt{Value: d.ID, Label: label})
	}
	// And the ones somebody shared, labelled with whose they are: a share that
	// cannot be armed is a share that stops at reading, and two people can name
	// a machine the same thing.
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
}

// handleConsoleMachineScheduleCreate arms a machine to run on a timetable.
//
//	POST /api/console/machine-schedule/create
//	{name, machine_id, mission, cron | interval_minutes, agent_id}
func (T *OrchestrateApp) handleConsoleMachineScheduleCreate(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Name            string `json:"name"`
		MachineID       string `json:"machine_id"`
		Mission         string `json:"mission"`
		Cron            string `json:"cron"`
		IntervalMinutes int    `json:"interval_minutes"`
		AgentID         string `json:"agent_id"` // whose rail this lands on
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		http.Error(w, "give the schedule a name", http.StatusBadRequest)
		return
	}
	if _, exists := GetStandingAgent(RootDB, user, name); exists {
		http.Error(w, "a schedule called "+name+" already exists", http.StatusBadRequest)
		return
	}
	def, found := machineForUser(user, strings.TrimSpace(body.MachineID))
	if !found {
		http.Error(w, "no such machine", http.StatusBadRequest)
		return
	}
	// The same two refusals the runner makes, made HERE: a schedule that
	// could never fire cleanly should not be armed and then discovered at
	// four in the morning.
	if !def.Unattended {
		http.Error(w, "that machine converses rather than runs: a schedule fires with nobody there to answer a step that waits",
			http.StatusBadRequest)
		return
	}
	if probs := def.Problems(); len(probs) > 0 {
		http.Error(w, "that machine will not run yet: "+probs[0], http.StatusBadRequest)
		return
	}

	sa, err := buildMachineSchedule(user, name, def, strings.TrimSpace(body.Mission),
		strings.TrimSpace(body.Cron), body.IntervalMinutes, strings.TrimSpace(body.AgentID))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	SaveStandingAgent(RootDB, sa)
	if err := ScheduleStandingAgent(RootDB, sa); err != nil {
		// Saved but not armed is the worst of both, so say which happened.
		http.Error(w, "saved, but arming the schedule failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	got, _ := GetStandingAgent(RootDB, user, name)
	Log("[orchestrate.standing] user=%q scheduled machine %q as %q (%s)", user, def.Name, name, StandingScheduleLabel(got))
	writeJSON(w, map[string]any{
		"ok": true, "name": name, "schedule": StandingScheduleLabel(got),
		"next_run": got.NextRun,
	})
}

// machineScheduleCreator is the rail's button for it, offered beside the
// recurring-task one.
func machineScheduleCreator() ui.ScheduleCreator {
	return ui.ScheduleCreator{Label: "New machine run", Action: "orchestrate_new_machine_run"}
}

// buildMachineSchedule turns a form into the record, or says what is wrong
// with it.
//
// Separated from the handler because everything interesting here is a
// DECISION — what an empty subject means, what a cadence maps to, which
// cadences the scheduler can read — and the handler around it is resolve,
// save, arm. Testing the decisions should not require a running scheduler.
func buildMachineSchedule(user, name string, def MachineDef, mission, cron string, intervalMinutes int, railAgent string) (StandingAgent, error) {
	sa := StandingAgent{
		Name:      name,
		Owner:     user,
		MachineID: def.ID,
		// The run's SUBJECT, which lands in {input} / {original_input}.
		// Defaulted to the machine's own name rather than left empty,
		// because a blank subject reaches the first step as nothing.
		Mission:       mission,
		Created:       time.Now(),
		ReportAgentID: railAgent,
	}
	if sa.Mission == "" {
		sa.Mission = def.Name
	}
	switch {
	case cron != "":
		if _, err := NextCronOccurrence(cron, time.Now()); err != nil {
			return StandingAgent{}, Error("that cadence is not one the scheduler understands: " + err.Error())
		}
		sa.Cron = cron
	case intervalMinutes > 0:
		sa.IntervalSeconds = intervalMinutes * 60
	default:
		return StandingAgent{}, Error("say how often it should run")
	}
	if err := sa.ValidateTarget(); err != nil {
		// Belt and braces: the record is built here and armed elsewhere, so
		// the one invariant that matters travels with the construction.
		return StandingAgent{}, err
	}
	return sa, nil
}
