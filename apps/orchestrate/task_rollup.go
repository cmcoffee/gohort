// Rollup: a parent that finishes when everything under it has finished.
//
// The open question the containment work deliberately left. A parent is one of
// two very different things and the link cannot tell them apart: some are a
// REAL CHECK ("the newsletter went out"), where the children being done is not
// the same claim at all, and some are a HEADING ("Q4 launch"), where the
// children being done is the entire meaning. So it is opt-in per parent, and
// rolling up is something an owner says rather than something inferred.
//
// EVENT-DRIVEN, not polled. It runs when a child records a met objective, walks
// up, and asks whether that was the last one. A parent that had to fire to
// notice would cost an LLM turn per check and would notice late; this costs
// nothing and notices immediately, and it means a heading parent needs no
// meaningful cadence at all.
//
// The conservative direction throughout is DO NOT FINISH. A child that cannot
// report done blocks its parent rather than being skipped, because the failure
// of the other choice is a piece of work marked finished while it is still
// running, which nobody goes looking for.
//
// See docs/task-containment.md.

package orchestrate

import (
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// rollUpDepth bounds the walk. The link layer already refuses a cycle, so this
// is a backstop against data that predates the guard or was written by hand:
// a walk that cannot terminate is worse than one that stops early and says so.
const rollUpDepth = 8

// childState is what a rollup needs to know about one child.
type childState struct {
	name string
	// met is the only thing that lets a parent finish. checkable says whether
	// this child could EVER report it: a schedule with no completion check
	// never can, and a parent waiting on one would otherwise wait forever
	// without anything saying why.
	met, checkable bool
}

// rollUpChildren reads every schedule that names this one as its parent.
func rollUpChildren(user, parentRef string) []childState {
	var out []childState
	for _, sa := range ListStandingAgents(RootDB, user) {
		if strings.TrimSpace(sa.Parent) != parentRef {
			continue
		}
		out = append(out, childState{
			name:      sa.Name,
			met:       StandingStopCause(sa) == StoppedByMet || objectiveMet(sa.Attempts),
			checkable: strings.TrimSpace(sa.Until) != "" || sa.RollUp,
		})
	}
	for _, rt := range listAgentRecurringTasks(user, "") {
		p := rt.Payload
		if strings.TrimSpace(p.Parent) != parentRef {
			continue
		}
		out = append(out, childState{
			name:      recurringName(p),
			met:       objectiveMet(p.Attempts),
			checkable: strings.TrimSpace(p.Until) != "" || p.RollUp,
		})
	}
	for _, m := range ListEventMonitors(RootDB, user) {
		if strings.TrimSpace(m.Parent) != parentRef {
			continue
		}
		out = append(out, childState{
			name:      m.Name,
			met:       m.StopCause() == MonitorStopMet || objectiveMet(m.Attempts),
			checkable: strings.TrimSpace(m.Until) != "" || m.RollUp,
		})
	}
	return out
}

// rollUpVerdict answers whether a parent may finish, and says why when it may
// not, in words an owner can act on.
//
// A parent with NO children does not roll up. "Everything under it is done" is
// vacuously true of nothing, and a heading that finishes the moment it is
// created is the most confusing possible behaviour.
func rollUpVerdict(children []childState) (done bool, why string) {
	if len(children) == 0 {
		return false, "nothing is under it yet"
	}
	var waiting, uncheckable []string
	for _, c := range children {
		switch {
		case !c.checkable:
			uncheckable = append(uncheckable, c.name)
		case !c.met:
			waiting = append(waiting, c.name)
		}
	}
	// Named, not counted. "waiting on 2" tells an owner they have to go and
	// find out which two, which is the work the sentence was supposed to save.
	if len(uncheckable) > 0 {
		return false, fmt.Sprintf("%s cannot report finishing (no completion check), so this cannot finish either",
			strings.Join(uncheckable, ", "))
	}
	if len(waiting) > 0 {
		return false, "waiting on " + strings.Join(waiting, ", ")
	}
	return true, fmt.Sprintf("everything under it finished (%d)", len(children))
}

// rollUpFrom is called when a schedule records a met objective. It walks up its
// parents and finishes any that are now complete.
//
// The walk continues past a parent it finishes, because finishing one can be
// the last thing its own parent was waiting for. It stops at the first parent
// that is not ready, since nothing above that can be either.
func rollUpFrom(user, childSurface, childID string) {
	ref, found := taskParentOf(user, childSurface, childID)
	if !found {
		return
	}
	for depth := 0; ref != "" && depth < rollUpDepth; depth++ {
		surface, id, ok := parseTaskParent(ref)
		if !ok {
			return
		}
		if !rollUpEnabled(user, surface, id) {
			return // a parent judged on its own evidence; nothing to do here
		}
		done, why := rollUpVerdict(rollUpChildren(user, ref))
		if !done {
			Log("[orchestrate/rollup] %s not finished: %s", ref, why)
			return
		}
		if !finishRolledUpParent(user, surface, id, why) {
			return
		}
		Log("[orchestrate/rollup] %s finished: %s", ref, why)
		next, found := taskParentOf(user, surface, id)
		if !found {
			return
		}
		ref = next
	}
}

// rollUpEnabled reports whether this schedule finishes on its children.
func rollUpEnabled(user, surface, id string) bool {
	switch surface {
	case schedKindStanding:
		if sa, ok := GetStandingAgent(RootDB, user, id); ok {
			return sa.RollUp
		}
	case schedKindMonitor:
		if m, ok := GetEventMonitor(RootDB, user, id); ok {
			return m.RollUp
		}
	case schedKindRecurring:
		for _, rt := range listAgentRecurringTasks(user, "") {
			if recurringTaskUID(rt.Payload) == id {
				return rt.Payload.RollUp
			}
		}
	}
	return false
}

// finishRolledUpParent stops a parent the way each surface already stops one
// whose objective was met, so a rolled-up finish is indistinguishable from any
// other finish on the console: same pause, same cause, same visible reason.
//
// Reusing those paths rather than inventing a fourth state is the point. A
// schedule that stopped for a reason nobody else writes is one no existing
// screen knows how to explain.
func finishRolledUpParent(user, surface, id, why string) bool {
	reason := "rolled up: " + why
	switch surface {
	case schedKindStanding:
		sa, ok := GetStandingAgent(RootDB, user, id)
		if !ok || sa.Paused {
			return false // already stopped; nothing to announce twice
		}
		sa.Attempts = appendObjectiveAttempt(sa.Attempts, true, reason)
		sa.Paused = true
		sa.StopCause = StoppedByMet
		sa.StopNote = reason
		SaveStandingAgent(RootDB, sa)
		return true
	case schedKindMonitor:
		m, ok := GetEventMonitor(RootDB, user, id)
		if !ok || m.Paused {
			return false
		}
		m.Attempts = appendObjectiveAttempt(m.Attempts, true, reason)
		SaveEventMonitor(RootDB, m)
		StopEventMonitor(RootDB, user, id, MonitorStopMet,
			"Stopped: "+reason+" Nothing is broken; resume it to watch again.")
		return true
	case schedKindRecurring:
		for _, rt := range listAgentRecurringTasks(user, "") {
			p := rt.Payload
			if recurringTaskUID(p) != id {
				continue
			}
			p.Attempts = appendObjectiveAttempt(p.Attempts, true, reason)
			// A met objective retires the task, which is what the judge's own
			// met path does: the occurrence is cancelled and nothing is armed.
			// The run ledger carries why, so it does not simply vanish.
			if err := CancelOrchestrateUpdate(p.SessionID, rt.TaskID); err != nil {
				Log("[orchestrate/rollup] recurring %q could not be stopped: %v", recurringName(p), err)
				return false
			}
			recordScheduledDrop(p, RunOK, "Finished: "+reason)
			dropRecurringTaskNotes(p)
			return true
		}
	}
	return false
}

// --- the owner's side -------------------------------------------------------

// handleConsoleSchedulerRollup turns rollup on or off for one schedule.
//
// A toggle rather than a picker, and it takes the row's own (kind, id) exactly
// as the parent action does, including the recurring occurrence-id translation.
func (T *OrchestrateApp) handleConsoleSchedulerRollup(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	kind, id, found := strings.Cut(strings.TrimSpace(r.URL.Query().Get("id")), ":")
	if !found || id == "" {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if kind == schedKindRecurring {
		uid, ok := recurringUIDForTaskID(user, id)
		if !ok {
			http.Error(w, "no such recurring task", http.StatusNotFound)
			return
		}
		id = uid
	}
	on := !rollUpEnabled(user, kind, id)
	if err := setRollUp(user, kind, id, on); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Turning it ON checks immediately. Everything under it may already be
	// finished, and a setting that waits for the next child to complete before
	// it can possibly act would look broken on the one case where the owner
	// enabled it because the work was already done.
	if on {
		if done, why := rollUpVerdict(rollUpChildren(user, taskParentRef(kind, id))); done {
			finishRolledUpParent(user, kind, id, why)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func setRollUp(user, surface, id string, on bool) error {
	switch surface {
	case schedKindStanding:
		sa, ok := GetStandingAgent(RootDB, user, id)
		if !ok {
			return fmt.Errorf("no such scheduled agent")
		}
		sa.RollUp = on
		SaveStandingAgent(RootDB, sa)
		return nil
	case schedKindMonitor:
		m, ok := GetEventMonitor(RootDB, user, id)
		if !ok {
			return fmt.Errorf("no such monitor")
		}
		m.RollUp = on
		SaveEventMonitor(RootDB, m)
		return nil
	case schedKindRecurring:
		for _, rt := range listAgentRecurringTasks(user, "") {
			if recurringTaskUID(rt.Payload) != id {
				continue
			}
			p := rt.Payload
			p.RollUp = on
			if !UpdateScheduledTaskPayload(rt.TaskID, p) {
				return fmt.Errorf("the schedule could not be updated")
			}
			return nil
		}
		return fmt.Errorf("no such recurring task")
	}
	return fmt.Errorf("unknown schedule kind")
}

// rollUpStateLabel is what a parent's row says about the arrangement: that it
// finishes on its children, and what it is still waiting for.
//
// The reason is on the row rather than behind a click because "why has this not
// finished" is the only question somebody asks about a rollup parent, and an
// answer they have to go and assemble from three other rows is not an answer.
func rollUpStateLabel(user, surface, id string) string {
	if !rollUpEnabled(user, surface, id) {
		return ""
	}
	_, why := rollUpVerdict(rollUpChildren(user, taskParentRef(surface, id)))
	return "Finishes on its children: " + why
}
