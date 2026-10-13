package orchestrate

// Schedules whose agent was deleted.
//
// Deleting an agent parks every schedule that ran as it (broken_dependency.go):
// kept and listed as "needs relink", because what it holds is worth keeping
// for a replacement agent: its mission, its timing, any goal and the attempts
// at it, and its notes. Relinking keeps all of that; recreating starts clean.
//
// Three things make that work rather than pile up:
//
//   - Relink is one helper per kind, used by the Scheduler's Relink button and
//     by the schedule tool's relink action, so Builder can move a deleted
//     agent's schedules onto the agent it made to replace it.
//   - A schedule parked because its agent (or pipeline, machine, tool) is gone
//     is deleted after parkedExpiry if nobody relinks it. When it was first
//     seen parked lives in a side table, never on the schedule, so every park
//     path is covered without each one remembering to stamp it; the row says
//     the date. Stalled objectives are not swept: nothing is missing there.
//   - Deleting an agent asks first, and can delete its schedules outright.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	parkedSinceTable = "schedule_parked_since"
	parkedExpiry     = 30 * 24 * time.Hour
)

func parkedKey(kind, owner, id string) string { return kind + "\x00" + owner + "\x00" + id }

// parkedSince is when a schedule was first seen parked, recording now the
// first time it is asked.
func parkedSince(kind, owner, id string) time.Time {
	if RootDB == nil {
		return time.Now()
	}
	var at time.Time
	k := parkedKey(kind, owner, id)
	if RootDB.Get(parkedSinceTable, k, &at) && !at.IsZero() {
		return at
	}
	at = time.Now()
	RootDB.Set(parkedSinceTable, k, at)
	return at
}

func clearParked(kind, owner, id string) {
	if RootDB != nil {
		RootDB.Unset(parkedSinceTable, parkedKey(kind, owner, id))
	}
}

// parkedDeadlineNote is what a parked row adds to its state: when it goes.
func parkedDeadlineNote(kind, owner, id string) string {
	at := parkedSince(kind, owner, id).Add(parkedExpiry)
	return " (deleted " + at.In(UserLocation(owner)).Format("Jan 2") + " unless relinked)"
}

// sweepParkedSchedules deletes schedules that have stayed parked for want of a
// dependency past parkedExpiry, and forgets the parked time of any that are
// running again. Runs as a reconciler: at startup and every half hour.
func sweepParkedSchedules(owners []string) int {
	if RootDB == nil {
		return 0
	}
	seen := map[string]bool{}
	removed := 0
	expired := func(kind, owner, id string) bool {
		k := parkedKey(kind, owner, id)
		seen[k] = true
		return time.Since(parkedSince(kind, owner, id)) > parkedExpiry
	}
	for _, owner := range owners {
		for _, sa := range ListStandingAgents(RootDB, owner) {
			if !RelinkFixesIt(StandingParkCause(sa)) || !expired(schedKindStanding, owner, sa.Name) {
				continue
			}
			DeleteStandingAgent(RootDB, owner, sa.Name)
			clearParked(schedKindStanding, owner, sa.Name)
			Log("[orchestrate/parked] deleted scheduled agent %s/%s: parked over %s without a relink (%s)", owner, sa.Name, parkedExpiry, sa.BrokenReason)
			removed++
		}
	}
	for _, t := range ListScheduledTasks(OrchestrateScheduledUpdateKind) {
		var p orchUpdatePayload
		if json.Unmarshal(t.Payload, &p) != nil || p.Username == "" {
			continue
		}
		uid := recurringTaskUID(p)
		if uid == "" || !RelinkFixesIt(recurringParkCause(p)) || !expired(schedKindRecurring, p.Username, uid) {
			continue
		}
		UnscheduleTask(t.ID)
		dropRecurringTaskNotes(p)
		recordScheduledDrop(p, RunAttention, fmt.Sprintf("Deleted: recurring task %q stayed parked for %d days without being relinked (%s).", recurringName(p), int(parkedExpiry.Hours()/24), p.BrokenReason))
		clearParked(schedKindRecurring, p.Username, uid)
		removed++
	}
	for _, m := range ListAllEventMonitors(RootDB) {
		if m.StopCause() != MonitorStopBroken || !expired(schedKindMonitor, m.Owner, m.Name) {
			continue
		}
		DeleteEventMonitor(RootDB, m.Owner, m.Name)
		clearParked(schedKindMonitor, m.Owner, m.Name)
		Log("[orchestrate/parked] deleted monitor %s/%s: parked over %s without a relink (%s)", m.Owner, m.Name, parkedExpiry, m.BrokenReason)
		removed++
	}
	// Anything no longer parked: forget when it was.
	for _, k := range RootDB.Keys(parkedSinceTable) {
		if !seen[k] {
			RootDB.Unset(parkedSinceTable, k)
		}
	}
	return removed
}

// registerParkedSweep wires the sweep into the scheduler's reconcilers.
func registerParkedSweep() {
	RegisterReconciler("parked_schedule_expiry", func(ctx context.Context) error {
		if AuthDB == nil || AuthDB() == nil {
			return nil // no accounts to sweep for yet
		}
		var owners []string
		for _, u := range AuthListUsers(AuthDB()) {
			owners = append(owners, u.Username)
		}
		if n := sweepParkedSchedules(owners); n > 0 {
			Log("[orchestrate/parked] removed %d schedule(s) left parked past %s", n, parkedExpiry)
		}
		return nil
	})
}

// --- relink ------------------------------------------------------------------

// relinkStandingToAgent points a scheduled agent at another agent, clears its
// park, and, with resume, puts it back on its time (or its group's).
func relinkStandingToAgent(owner, name, agentID string, resume bool) error {
	sa, ok := GetStandingAgent(RootDB, owner, name)
	if !ok {
		return Error("no such scheduled agent")
	}
	if sa.TargetsPipeline() || sa.TargetsMachine() {
		return Error("it runs a pipeline or machine, not an agent: relink it from the Scheduler")
	}
	sa.AgentID, sa.Broken, sa.BrokenReason, sa.BrokenCause = agentID, false, "", ""
	clearParked(schedKindStanding, owner, name)
	if !resume {
		SaveStandingAgent(RootDB, sa)
		return nil
	}
	sa.Paused, sa.StopCause, sa.StopNote = false, "", ""
	return ScheduleStandingAgent(RootDB, sa)
}

// relinkRecurringToAgent points a recurring task at another agent and puts it
// straight back on its own pattern (or its group's): recurring tasks have no
// pause to leave it in.
func relinkRecurringToAgent(owner, taskID, agentID string) error {
	for _, rt := range listAgentRecurringTasks(owner, "") {
		if rt.TaskID != taskID {
			continue
		}
		UnscheduleTask(taskID)
		p := rt.Payload
		p.AgentID, p.Broken, p.BrokenReason = agentID, false, ""
		p.LastActive = time.Now().Format(time.RFC3339)
		clearParked(schedKindRecurring, owner, recurringTaskUID(p))
		next, err := computeNextFire(&p, time.Now().In(UserLocation(owner)))
		if err != nil {
			return fmt.Errorf("relinked but could not schedule: %w", err)
		}
		_, err = ScheduleTask(OrchestrateScheduledUpdateKind, p, next)
		return err
	}
	return Error("recurring task not found")
}

// relinkMonitorToAgent points a monitor's wake at another agent ("" = the
// deployment default) and clears its park; with resume it watches again.
func relinkMonitorToAgent(owner, name, agentID string, resume bool) error {
	m, ok := GetEventMonitor(RootDB, owner, name)
	if !ok {
		return Error("no such monitor")
	}
	m.WakeAgent, m.Broken, m.BrokenReason = agentID, false, ""
	clearParked(schedKindMonitor, owner, name)
	if !resume {
		SaveEventMonitor(RootDB, m)
		return nil
	}
	// The console's Resume, which this is, as it does it.
	RearmMonitorFires(&m)
	m.Paused, m.StopReason = false, ""
	SaveEventMonitor(RootDB, m)
	return ScheduleEventMonitor(RootDB, m)
}

// scheduleRelink is the schedule tool's relink action: name is a parked
// schedule's name (or a recurring task's id), runBy the agent to run it now.
// Only parked schedules: moving a working schedule to another agent is an edit
// the person makes, not a repair.
func scheduleRelink(owner, name, runBy string) (string, error) {
	runBy = strings.TrimSpace(runBy)
	if runBy == "" {
		return "", Error("relink needs run_by: the agent that runs it from now on, by name or id")
	}
	if orchestrateBaseDB == nil {
		return "", Error("no agent store")
	}
	a, ok := findAgentByNameOrID(UserDB(orchestrateBaseDB, owner), owner, runBy)
	if !ok {
		return "", fmt.Errorf("no agent %q: agents(action=\"list\") shows them", runBy)
	}
	notParked := fmt.Errorf("%q is not parked: relink is for a schedule whose agent was deleted (schedule(action=\"list\") marks them \"needs relink\")", name)
	if sa, ok := GetStandingAgent(RootDB, owner, name); ok {
		if !RelinkFixesIt(StandingParkCause(sa)) {
			return "", notParked
		}
		if err := relinkStandingToAgent(owner, name, a.ID, true); err != nil {
			return "", err
		}
		return fmt.Sprintf("Scheduled agent %q now runs as %s and is back on its schedule. Its mission, timing, goal and notes are unchanged.", name, a.Name), nil
	}
	if m, ok := GetEventMonitor(RootDB, owner, name); ok {
		if m.StopCause() != MonitorStopBroken {
			return "", notParked
		}
		if err := relinkMonitorToAgent(owner, name, a.ID, true); err != nil {
			return "", err
		}
		return fmt.Sprintf("Monitor %q now wakes %s and is watching again.", name, a.Name), nil
	}
	hits := userRecurringMatches(owner, name)
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("nothing scheduled is named %q: schedule(action=\"list\", agent=\"all\") shows what there is", name)
	case 1:
	default:
		return "", fmt.Errorf("%q names %d recurring tasks (%s): pass one id as name", name, len(hits), recurringIDs(hits))
	}
	if !RelinkFixesIt(recurringParkCause(hits[0].Payload)) {
		return "", notParked
	}
	if err := relinkRecurringToAgent(owner, hits[0].TaskID, a.ID); err != nil {
		return "", err
	}
	return fmt.Sprintf("Recurring task %q now runs as %s and is back on its schedule. Its directive, timing, goal and notes are unchanged.", recurringName(hits[0].Payload), a.Name), nil
}

// --- deleting an agent -------------------------------------------------------

// agentFamilyIDs is an agent and every sub-agent it owns, all the way down:
// what one delete removes.
func agentFamilyIDs(udb Database, owner, id string) map[string]bool {
	ids := map[string]bool{id: true}
	for grew := true; grew; {
		grew = false
		for _, a := range listAgents(udb, owner) {
			if a.Owner == owner && !ids[a.ID] && ids[a.OwnedBy] {
				ids[a.ID] = true
				grew = true
			}
		}
	}
	return ids
}

// agentScheduleNames lists the schedules that run as any of ids: what deleting
// those agents parks.
func agentScheduleNames(owner string, ids map[string]bool) []string {
	var out []string
	for _, sa := range ListStandingAgents(RootDB, owner) {
		if ids[sa.AgentID] {
			out = append(out, sa.Name)
		}
	}
	for _, rt := range listAgentRecurringTasks(owner, "") {
		if ids[rt.Payload.AgentID] {
			out = append(out, recurringName(rt.Payload))
		}
	}
	for _, m := range ListEventMonitors(RootDB, owner) {
		if m.WakeAgent != "" && ids[m.WakeAgent] {
			out = append(out, m.Name)
		}
	}
	return out
}

// deleteAgentSchedules deletes, rather than parks, the schedules that ran as
// any of ids. For an agent deletion the person chose to take them with it.
func deleteAgentSchedules(owner string, ids map[string]bool) int {
	n := 0
	for _, sa := range ListStandingAgents(RootDB, owner) {
		if ids[sa.AgentID] {
			DeleteStandingAgent(RootDB, owner, sa.Name)
			clearParked(schedKindStanding, owner, sa.Name)
			n++
		}
	}
	for _, rt := range listAgentRecurringTasks(owner, "") {
		if ids[rt.Payload.AgentID] {
			UnscheduleTask(rt.TaskID)
			dropRecurringTaskNotes(rt.Payload)
			clearParked(schedKindRecurring, owner, recurringTaskUID(rt.Payload))
			n++
		}
	}
	for _, m := range ListEventMonitors(RootDB, owner) {
		if m.WakeAgent != "" && ids[m.WakeAgent] {
			DeleteEventMonitor(RootDB, owner, m.Name)
			clearParked(schedKindMonitor, owner, m.Name)
			n++
		}
	}
	return n
}

// handleAgentSchedules answers the delete confirmation: GET
// /api/agents/<id>/schedules -> {names: [...]}, the schedules that deleting
// this agent (and its sub-agents) would park.
func (T *OrchestrateApp) handleAgentSchedules(w http.ResponseWriter, udb Database, user, id string) {
	names := agentScheduleNames(user, agentFamilyIDs(udb, user, id))
	if names == nil {
		names = []string{}
	}
	writeJSON(w, map[string]any{"names": names, "keep_days": int(parkedExpiry.Hours() / 24)})
}

// agentDeleteData is the editor Delete button's data for the client action.
func agentDeleteData(id, label, redirect string) string {
	b, _ := json.Marshal(map[string]string{"id": id, "base": "../", "label": label, "redirect": redirect})
	return string(b)
}

// agentDeleteAskJS is the delete confirmation for an agent, on both of the
// pages that delete one: it names the schedules the agent runs and offers to
// keep them parked for a replacement (the default) or delete them with it.
// Registered as the client action "orchestrate_delete_agent_ask", whose
// ctx.action.data is JSON {id, base, label, redirect}: base is the prefix the
// page reaches the API through ("../" from the editor), redirect where to go
// once the agent is gone. window.orchDeleteAgentAsk is the same flow for a
// page that already knows those (the chat page's own Delete).
//
// No backticks: this is a Go raw string.
const agentDeleteAskJS = `<script>
(function() {
  function register() {
    if (!window.uiRegisterClientAction) return;
    window.orchDeleteAgentAsk = function(base, id, label, done) {
      var api = base + 'api/agents/' + encodeURIComponent(id);
      fetch(api + '/schedules', {credentials: 'same-origin'})
        .then(function(r) { return r.ok ? r.json() : {names: []}; })
        .catch(function() { return {names: []}; })
        .then(function(info) {
          var names = info.names || [], days = info.keep_days || 30;
          window.uiOpenModal({
            title: 'Delete ' + (label || 'this agent'),
            width: '480px',
            mount: function(body) {
              function line(text, mute) {
                var d = document.createElement('div');
                d.style.cssText = 'margin:0 0 0.5rem;line-height:1.45' + (mute ? ';color:var(--text-mute);font-size:var(--fs-sm)' : '');
                d.textContent = text;
                body.appendChild(d);
              }
              line('Its conversations, memory, knowledge and any sub-agents it owns are deleted. This cannot be undone.');
              body.dataset.schedules = 'keep';
              if (!names.length) return;
              line('It runs ' + names.length + ' schedule' + (names.length === 1 ? '' : 's') + ': ' + names.join(', ') + '.');
              function radio(value, text, checked) {
                var w = document.createElement('label');
                w.style.cssText = 'display:flex;align-items:flex-start;gap:0.5rem;margin:0.35rem 0;cursor:pointer';
                var r = document.createElement('input');
                r.type = 'radio'; r.name = 'delagent-sched'; r.value = value; r.checked = !!checked;
                r.style.cssText = 'margin-top:0.2rem';
                r.addEventListener('change', function() { body.dataset.schedules = value; });
                w.appendChild(r);
                w.appendChild(document.createTextNode(text));
                body.appendChild(w);
              }
              radio('keep', 'Keep them parked for ' + days + ' days, so they can be moved to a replacement agent (Relink), then delete them', true);
              radio('delete', 'Delete them now, with the agent', false);
            },
            actions: [
              {label: 'Cancel', onClick: function(m) { m.close(); }},
              {label: 'Delete agent', primary: true, variant: 'danger', onClick: function(m) {
                var body = m.body || document.querySelector('.ui-modal-body');
                var del = body && body.dataset.schedules === 'delete';
                fetch(api + (del ? '?schedules=delete' : ''), {method: 'DELETE', credentials: 'same-origin'})
                  .then(function(r) {
                    if (!r.ok && r.status !== 204) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
                    m.close();
                    if (done) done();
                  })
                  .catch(function(e) { window.uiAlert('Not deleted: ' + e.message); });
              }}
            ]
          });
        });
    };
    window.uiRegisterClientAction('orchestrate_delete_agent_ask', function(ctx) {
      var d = {};
      try { d = JSON.parse(((ctx && ctx.action) || {}).data || '{}'); } catch (e) {}
      if (!d.id) return;
      window.orchDeleteAgentAsk(d.base || '', d.id, d.label || '', function() {
        if (d.redirect) window.uiGoTo(d.redirect);
      });
    });
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', register);
  else register();
})();
</` + `script>`
