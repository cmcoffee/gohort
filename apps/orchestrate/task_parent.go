// Containment: one schedule saying it exists to serve another.
//
// The question this answers is the one the vocabulary could not. gohort has
// three words that sound like the same thing at different sizes, and they are
// not: a TASK is a record that runs, a MISSION is what it does each time it
// runs, and a GOAL is the condition under which it should stop existing. A
// standing agent carries a mission and a goal at once, so they cannot be the
// same thing scaled up and down.
//
// What was genuinely missing is the other axis. There was no way to say that
// three schedules are pieces of one larger piece of work, so a person who
// decomposed something got three unrelated rows and had to hold the connection
// in their head.
//
// So: a LINK, and deliberately nothing more. An objective is a schedule with a
// completion check, never its own table and never a new trigger kind
// (docs/loop-objectives.md, decision locked by that build), and a container
// record that does not fire is exactly the thing that decision refused. A
// parent here is an ordinary schedule that happens to have children.
//
// The link carries NO authority, and that restraint is the whole design:
//   - a child is not paused, resumed or retired by its parent
//   - a parent is not met because its children are met
//   - nothing inherits: not tools, not permissions, not guardrails
//
// Each of those would be a policy, and every one of them is arguable in both
// directions. Inventing them alongside the link that needs them is how a link
// becomes a container, and then a container becomes the fifth trigger kind the
// objectives build spent its whole design avoiding. If one of these earns its
// place, it earns it after somebody has lived with the link.
//
// See docs/task-containment.md.

package orchestrate

import (
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// taskParentRef is how one schedule names another: the surface it lives on and
// its id, joined.
//
// The surface is in the reference because ids are only unique WITHIN one: a
// monitor and a standing agent may both be called "nightly", and a reference
// that could mean either would silently resolve to whichever was looked up
// first. Same reasoning as the Scheduler's _kind, and it reuses those constants
// so the two cannot disagree about what a surface is called.
func taskParentRef(surface, id string) string {
	surface, id = strings.TrimSpace(surface), strings.TrimSpace(id)
	if surface == "" || id == "" {
		return ""
	}
	return surface + ":" + id
}

func parseTaskParent(ref string) (surface, id string, ok bool) {
	surface, id, ok = strings.Cut(strings.TrimSpace(ref), ":")
	if !ok || strings.TrimSpace(surface) == "" || strings.TrimSpace(id) == "" {
		return "", "", false
	}
	return surface, id, true
}

// taskParentOf returns the parent reference stored on one schedule, resolving
// the id the same way the console rows do.
//
// A recurring task is keyed by its UID rather than by the scheduler task id the
// row is actioned by, because that id is re-minted on EVERY fire: a reference
// to it would name an occurrence that has already been replaced. This is the
// same identity the task's notes hang on, and the reason the UID exists.
func taskParentOf(user, surface, id string) (parent string, found bool) {
	switch surface {
	case schedKindStanding:
		if sa, ok := GetStandingAgent(RootDB, user, id); ok {
			return strings.TrimSpace(sa.Parent), true
		}
	case schedKindMonitor:
		if m, ok := GetEventMonitor(RootDB, user, id); ok {
			return strings.TrimSpace(m.Parent), true
		}
	case schedKindRecurring:
		for _, rt := range listAgentRecurringTasks(user, "") {
			if recurringTaskUID(rt.Payload) == id {
				return strings.TrimSpace(rt.Payload.Parent), true
			}
		}
	}
	return "", false
}

// taskParentLabel is what a row says about its parent.
//
// A parent that no longer exists still SHOWS, as a deleted one. The link is not
// quietly dropped and the child is not touched: a schedule whose sibling work
// was deleted is still doing its own job, and the broken-dependency posture
// everywhere else in this console is to keep the thing, say what is wrong, and
// let a person decide. Silently clearing it would lose the only record that
// this was ever part of something.
func taskParentLabel(user, ref string) string {
	surface, id, ok := parseTaskParent(ref)
	if !ok {
		return ""
	}
	switch surface {
	case schedKindStanding:
		if sa, ok := GetStandingAgent(RootDB, user, id); ok {
			return firstNonEmptyStr(sa.Name, id)
		}
	case schedKindMonitor:
		if m, ok := GetEventMonitor(RootDB, user, id); ok {
			return firstNonEmptyStr(m.Name, id)
		}
	case schedKindRecurring:
		for _, rt := range listAgentRecurringTasks(user, "") {
			if recurringTaskUID(rt.Payload) == id {
				return recurringName(rt.Payload)
			}
		}
	}
	return id + " (deleted)"
}

// taskParentWouldLoop reports whether pointing `child` at `parent` closes a
// cycle: the parent is the child itself, or reaches it by following its own
// parents up.
//
// Guarded rather than tolerated. A cycle is not an error anybody would see at
// the moment they made it, and every reader afterwards walks it forever.
func taskParentWouldLoop(user, childSurface, childID, parentRef string) bool {
	childRef := taskParentRef(childSurface, childID)
	seen := map[string]bool{}
	for ref := strings.TrimSpace(parentRef); ref != ""; {
		if ref == childRef {
			return true
		}
		if seen[ref] {
			// Already-looping data upstream. Refusing here is the conservative
			// answer: the caller is about to attach to a chain that does not
			// terminate, whoever made it that way.
			return true
		}
		seen[ref] = true
		surface, id, ok := parseTaskParent(ref)
		if !ok {
			return false
		}
		next, found := taskParentOf(user, surface, id)
		if !found {
			return false // a dangling parent ends the walk; it cannot loop
		}
		ref = next
	}
	return false
}

// setTaskParent stores (or clears, with an empty ref) one schedule's parent.
//
// Ownership is the lookup: each surface is read from the asking user's own
// records, so somebody else's id is not refused, it is simply not found.
func setTaskParent(user, surface, id, parentRef string) error {
	parentRef = strings.TrimSpace(parentRef)
	if parentRef != "" {
		if _, _, ok := parseTaskParent(parentRef); !ok {
			return fmt.Errorf("that is not a schedule reference")
		}
		if taskParentWouldLoop(user, surface, id, parentRef) {
			return fmt.Errorf("that would make a loop: the schedule you picked already reports up to this one")
		}
	}
	switch surface {
	case schedKindStanding:
		sa, ok := GetStandingAgent(RootDB, user, id)
		if !ok {
			return fmt.Errorf("no such scheduled agent")
		}
		sa.Parent = parentRef
		SaveStandingAgent(RootDB, sa)
		return nil
	case schedKindMonitor:
		m, ok := GetEventMonitor(RootDB, user, id)
		if !ok {
			return fmt.Errorf("no such monitor")
		}
		m.Parent = parentRef
		SaveEventMonitor(RootDB, m)
		return nil
	case schedKindRecurring:
		// A recurring task lives only as its scheduler entry, so changing it
		// means rewriting that entry's payload in place. The occurrence id is
		// what UpdateScheduledTaskPayload needs; the UID is what the reference
		// is keyed on. Both are in hand here and they are not the same thing.
		for _, rt := range listAgentRecurringTasks(user, "") {
			if recurringTaskUID(rt.Payload) != id {
				continue
			}
			p := rt.Payload
			p.Parent = parentRef
			if !UpdateScheduledTaskPayload(rt.TaskID, p) {
				return fmt.Errorf("the schedule could not be updated")
			}
			return nil
		}
		return fmt.Errorf("no such recurring task")
	}
	return fmt.Errorf("unknown schedule kind")
}

// --- the owner's side -------------------------------------------------------

// handleConsoleSchedulerParent sets or clears one schedule's parent.
//
// The row is identified the way every other Scheduler action identifies it, by
// (kind, id), which for a recurring task is the id of its next OCCURRENCE. The
// link is stored against the task's own UID instead, because the occurrence id
// is re-minted on every fire. Translating here rather than making setTaskParent
// accept either keeps one meaning per argument: a function that takes whichever
// id you happen to have is one that cannot tell you when you had the wrong one.
func (T *OrchestrateApp) handleConsoleSchedulerParent(w http.ResponseWriter, r *http.Request) {
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
	// An empty value clears the link, which is what the picker's first option
	// sends. Nothing else about the schedule changes: a row that stops being
	// part of something is still the same row doing the same work.
	if err := setTaskParent(user, kind, id, strings.TrimSpace(r.URL.Query().Get("value"))); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// recurringUIDForTaskID maps the id a console row is actioned by to the task's
// own identity.
func recurringUIDForTaskID(user, taskID string) (string, bool) {
	for _, rt := range listAgentRecurringTasks(user, "") {
		if rt.TaskID == taskID {
			return recurringTaskUID(rt.Payload), true
		}
	}
	return "", false
}

// handleConsoleSchedulerParentOptions lists what a schedule can be made part
// of: every other schedule this user owns.
//
// Everything, across all three surfaces, because the decomposition people
// actually do crosses them: a goal checked by a standing agent, gathered by a
// recurring task, and triggered by a monitor is one piece of work in three
// shapes. Offering only same-kind parents would describe a filing system rather
// than the work.
func (T *OrchestrateApp) handleConsoleSchedulerParentOptions(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	type opt struct {
		Value string `json:"value"`
		Label string `json:"label"`
	}
	// The first option is the way out. A picker that can only ever ATTACH is
	// one that makes a mistake permanent.
	out := []opt{{Value: "", Label: "Not part of anything"}}
	for _, sa := range ListStandingAgents(RootDB, user) {
		out = append(out, opt{Value: taskParentRef(schedKindStanding, sa.Name), Label: "Scheduled agent - " + sa.Name})
	}
	for _, rt := range listAgentRecurringTasks(user, "") {
		uid := recurringTaskUID(rt.Payload)
		if uid == "" {
			continue // nothing stable to point at; see recurringTaskUID
		}
		out = append(out, opt{Value: taskParentRef(schedKindRecurring, uid), Label: "Recurring task - " + recurringName(rt.Payload)})
	}
	for _, m := range ListEventMonitors(RootDB, user) {
		out = append(out, opt{Value: taskParentRef(schedKindMonitor, m.Name), Label: "Event monitor - " + m.Name})
	}
	writeJSON(w, out)
}
