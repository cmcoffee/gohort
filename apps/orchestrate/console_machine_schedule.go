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
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/timing"
)

// handleConsoleMachineOptions lists the machines a schedule may fire: this
// user's own, and only the ones marked to RUN.
//
//	GET /api/console/machine-options → [{value, label}]
func (T *OrchestrateApp) handleConsoleMachineOptions(c *jsonapi.Call) (any, error) {
	user, udb := c.User, c.UDB
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
	return opts, nil
}

// handleConsoleMachineScheduleCreate arms a machine to run on a timetable.
//
//	POST /api/console/machine-schedule/create
//	{name, machine_id, mission, cron | interval_minutes, agent_id}
func (T *OrchestrateApp) handleConsoleMachineScheduleCreate(c *jsonapi.Call, body struct {
	Name            string `json:"name"`
	MachineID       string `json:"machine_id"`
	Mission         string `json:"mission"`
	Cron            string `json:"cron"`
	IntervalMinutes int    `json:"interval_minutes"`
	AgentID         string `json:"agent_id"` // whose rail this lands on
	// Timing is the shared timing section (core/timing); when sent it
	// replaces cron / interval_minutes.
	Timing *timing.Spec `json:"timing"`
	// Until is the shared Until section (schedule_until.go).
	Until *untilBody `json:"until"`
}) (any, error) {
	user := c.User
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return nil, jsonapi.BadRequest("give the schedule a name")
	}
	if _, exists := GetStandingAgent(RootDB, user, name); exists {
		return nil, jsonapi.BadRequest("a schedule called " + name + " already exists")
	}
	def, found := machineForUser(user, strings.TrimSpace(body.MachineID))
	if !found {
		return nil, jsonapi.BadRequest("no such machine")
	}
	// The same two refusals the runner makes, made HERE: a schedule that
	// could never fire cleanly should not be armed and then discovered at
	// four in the morning.
	if !def.Unattended {
		return nil, jsonapi.BadRequest("that machine converses rather than runs: a schedule fires with nobody there to answer a step that waits")
	}
	if probs := machineRunProblems(UserDB(T.DB, user), user, def); len(probs) > 0 {
		return nil, jsonapi.BadRequest("that machine will not run yet: " + probs[0])
	}

	cronIn, intervalIn := strings.TrimSpace(body.Cron), body.IntervalMinutes
	if body.Timing != nil {
		if err := body.Timing.Validate(); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
		cronIn, intervalIn = "", 1 // satisfies the builder; Timing replaces it below
	}
	sa, err := buildMachineSchedule(user, name, def, strings.TrimSpace(body.Mission),
		cronIn, intervalIn, strings.TrimSpace(body.AgentID))
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	if body.Timing != nil {
		sa.Timing, sa.IntervalSeconds = *body.Timing, 0
	}
	if body.Until != nil {
		if err := applyStandingUntil(&sa, *body.Until, true); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
	}
	SaveStandingAgent(RootDB, sa)
	if err := ScheduleStandingAgent(RootDB, sa); err != nil {
		// Saved but not armed is the worst of both, so say which happened.
		return nil, jsonapi.Status(http.StatusInternalServerError, "saved, but arming the schedule failed: "+err.Error())
	}
	got, _ := GetStandingAgent(RootDB, user, name)
	Log("[orchestrate.standing] user=%q scheduled machine %q as %q (%s)", user, def.Name, name, StandingScheduleLabel(got))
	return map[string]any{
		"ok": true, "name": name, "schedule": StandingScheduleLabel(got),
		"next_run": got.NextRun,
	}, nil
}

// machineRunCreatorAction names the client action that opens the "schedule a
// machine run" form. Here rather than in the page literal so the action's name
// sits with the handler it reaches; the LABEL stays a literal in the nav, which
// is how the nav guard finds it.
const machineRunCreatorAction = "orchestrate_new_machine_run"

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
