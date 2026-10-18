package orchestrate

// What runs as an agent, read beside the agent's own record.
//
// An agent's behaviour is written in two places: its record (prompt, rules,
// tools) and the instructions of whatever runs as it on a clock or a trigger.
// Reading an agent showed only the first, so a post cap written into a
// schedule's mission was invisible: asked to remove it, Builder found nothing,
// added a rule to undo a cap it never saw, and reported the cap removed.
// agents(get) now lists both, so the second place is in front of whoever
// reads the agent, whether or not they think to look.

import (
	"encoding/json"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// runsOnMissionMax is how much of each mission the listing shows; get on the
// schedule has the rest.
const runsOnMissionMax = 400

// runsOnEntry is one thing that runs as the agent.
type runsOnEntry struct {
	Kind  string `json:"kind"` // scheduled run | monitor | recurring task
	Name  string `json:"name"`
	When  string `json:"when,omitempty"`
	State string `json:"state,omitempty"`
	Does  string `json:"does"`
}

const runsOnNote = "These run as this agent, and what each one does is instructions too: a change to how the agent behaves may belong in one of them rather than in its record. schedule(action=\"get\", agent, name) shows one in full and schedule(action=\"edit\") changes it."

// agentRunsOn lists owner's scheduled runs, monitors and recurring tasks
// that run as agentID.
func agentRunsOn(owner, agentID string) []runsOnEntry {
	if RootDB == nil || owner == "" || agentID == "" {
		return nil
	}
	var out []runsOnEntry
	for _, sa := range ListStandingAgents(RootDB, owner) {
		if sa.AgentID != agentID {
			continue
		}
		state := "active"
		if sa.Paused {
			state = "paused"
		}
		if RelinkFixesIt(StandingParkCause(sa)) {
			state = "parked, needs relink"
		}
		out = append(out, runsOnEntry{Kind: "scheduled run", Name: sa.Name, When: StandingScheduleLabel(sa), State: state, Does: runsOnClip(sa.Mission)})
	}
	for _, m := range ListEventMonitors(RootDB, owner) {
		if m.WakeAgent != agentID {
			continue
		}
		state := "active"
		if m.Paused {
			state = "paused"
		}
		out = append(out, runsOnEntry{Kind: "monitor", Name: m.Name, State: state, Does: runsOnClip(m.WakeBrief)})
	}
	for _, r := range listAgentRecurringTasks(owner, agentID) {
		out = append(out, runsOnEntry{Kind: "recurring task", Name: chFirst(r.Payload.Name, r.TaskID), When: "next " + r.RunAt, Does: runsOnClip(r.Payload.Prompt)})
	}
	return out
}

func runsOnClip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > runsOnMissionMax {
		return truncate(s, runsOnMissionMax) + " (shortened)"
	}
	return s
}

// withRunsOn adds what runs as the agent to its JSON view, after the fields
// it already has, so the record reads as before with the rest beneath it.
func withRunsOn(raw []byte, owner, agentID string) []byte {
	entries := agentRunsOn(owner, agentID)
	if len(entries) == 0 {
		return raw
	}
	s := strings.TrimRight(string(raw), " \n")
	if !strings.HasSuffix(s, "}") {
		return raw
	}
	list, _ := json.Marshal(entries)
	note, _ := json.Marshal(runsOnNote)
	return []byte(s[:len(s)-1] + `,"runs_on":` + string(list) + `,"_runs_on_note":` + string(note) + "}")
}
