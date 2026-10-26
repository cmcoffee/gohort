package orchestrate

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// Moving an agent is a change of PLACE, not a copy: the record keeps its id,
// so its sessions, knowledge, memory, channels and history come along. A
// clone with promote was the only way out of a sub-agent before, and it left
// all of that behind on the original.
//
// One level only. A destination is a top-level agent, and an agent that owns
// sub-agents cannot itself become one: the picker groups sub-agents under a
// top-level parent, so a sub-agent's own sub-agents would be listed nowhere.

// agentMove is one requested move of an agent, checked and then applied.
type agentMove struct {
	db     Database
	owner  string
	agent  AgentRecord
	parent AgentRecord // zero when the move makes the agent top-level
	// memorySet: the caller decided KeepsMemory itself (update_agent passing
	// keeps_memory), so under leaves it as given.
	memorySet bool
}

// agentMovable reports why an agent cannot be moved at all, "" when it can.
// Framework and app agents have homes of their own; a held agent waits on its
// parent's approval, which a move would orphan.
func agentMovable(a AgentRecord) string {
	switch {
	case isSeedID(a.ID):
		return "a built-in agent can't be moved; clone it and move the copy"
	case isAppAgent(a.ID) || strings.TrimSpace(a.OwningApp) != "":
		return "an app's agent belongs to its app and can't be moved"
	case a.PendingApproval:
		return "this agent is waiting for approval; approve or discard it first"
	}
	return ""
}

// agentMoveDestinations lists the agents a can be moved under: the owner's own
// top-level agents that are not a itself, built-in, an app's, or held.
func agentMoveDestinations(db Database, owner string, a AgentRecord) []AgentRecord {
	var out []AgentRecord
	for _, p := range listAgents(db, owner) {
		if p.ID == a.ID || p.Owner != owner || p.OwnedBy != "" || agentMovable(p) != "" {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// newAgentMove loads and checks a move of id under parentID ("" = top-level).
func newAgentMove(db Database, owner, id, parentID string) (*agentMove, error) {
	a, ok := loadAgent(db, id)
	if !ok {
		return nil, fmt.Errorf("agent not found")
	}
	if why := agentMovable(a); why != "" {
		return nil, fmt.Errorf("%s", why)
	}
	if a.Owner != owner {
		return nil, fmt.Errorf("not your agent")
	}
	m := &agentMove{db: db, owner: owner, agent: a}
	parentID = strings.TrimSpace(parentID)
	if parentID == strings.TrimSpace(a.OwnedBy) {
		if parentID == "" {
			return nil, fmt.Errorf("%s is already a top-level agent", a.Name)
		}
		return nil, fmt.Errorf("%s is already a sub-agent of that agent", a.Name)
	}
	if parentID == "" {
		return m, nil
	}
	for _, p := range agentMoveDestinations(db, owner, a) {
		if p.ID == parentID {
			m.parent = p
		}
	}
	if m.parent.ID == "" {
		return nil, fmt.Errorf("an agent can only move under one of your own top-level agents")
	}
	if n := len(m.children()); n > 0 {
		return nil, fmt.Errorf("%s has %d sub-agent%s of its own, which would be listed nowhere once it is a sub-agent. Move or delete them first", a.Name, n, plural(n))
	}
	// Sharing and publishing are a sub-agent's to lack: it runs with its
	// parent's authority, so nobody else may reach it. Clearing them here
	// would take the agent away from people who were given it, without
	// telling them, so the owner stops sharing first and knows they did.
	var reach []string
	if len(a.AllowedUsers) > 0 {
		reach = append(reach, "shared with "+ShareRecipientsText(a.AllowedUsers))
	}
	if a.Everyone {
		reach = append(reach, "published to everyone")
	}
	if a.MCPExposed {
		reach = append(reach, "reachable over MCP")
	}
	if len(reach) > 0 {
		return nil, fmt.Errorf("%s is %s. A sub-agent is private to its parent, so turn that off on its Security page (Share tab) first", a.Name, strings.Join(reach, " and "))
	}
	return m, nil
}

// children lists the sub-agents the moving agent owns.
func (m *agentMove) children() []AgentRecord {
	var out []AgentRecord
	for _, c := range listAgents(m.db, m.owner) {
		if c.OwnedBy == m.agent.ID {
			out = append(out, c)
		}
	}
	return out
}

// apply saves the move and returns the moved record and a note on what else
// changed because of it.
func (m *agentMove) apply() (AgentRecord, string, error) {
	if m.parent.ID != "" {
		return m.under()
	}
	return m.out()
}

// under makes the agent a sub-agent of m.parent. enforceSubAgentPosture is what
// a sub-agent loads as, so it is stored that way too rather than left to differ
// from what every reader sees.
func (m *agentMove) under() (AgentRecord, string, error) {
	a := m.agent
	// A top-level agent that has been remembering goes on remembering: what it
	// learned is its own and still useful where it now works. Without this
	// enforceSubAgentPosture turns its memory off, so an agent folded into a
	// family stopped reading everything it knew. Nothing is lost either way
	// (memory is keyed by its id); this keeps it in use.
	keptMemory := false
	if strings.TrimSpace(a.OwnedBy) == "" && !m.memorySet && !a.KeepsMemory && (!a.DisableExplicit || !a.DisableInferred) {
		a.KeepsMemory, keptMemory = true, true
	}
	a.OwnedBy = m.parent.ID
	a = enforceSubAgentPosture(a)
	saved, err := saveAgentAs(m.db, a, "moved under "+m.parent.Name)
	if err != nil {
		return saved, "", err
	}
	Log("[orchestrate.agents] moved %q under %q", saved.Name, m.parent.Name)
	notes := []string{fmt.Sprintf("%s is now a sub-agent of %s. Only %s can call it.", saved.Name, m.parent.Name, m.parent.Name)}
	if keptMemory {
		notes = append(notes, "It keeps its own memory, as it did at the top level; switch off Keeps its own memory on its page to make it start every run fresh.")
	}
	if strings.TrimSpace(saved.BuildNotes) != "" {
		notes = append(notes, fmt.Sprintf("Its build notes stay on it but go unread while it is a sub-agent: what still matters belongs in %s's build notes, which speak for the whole family.", m.parent.Name))
	}
	// A sub-agent has no way in of its own: its schedules now wake the
	// parent, which hands their work to it (scheduleAgentFor).
	if n := foldSubAgentSchedules(m.owner, saved.ID); n > 0 {
		notes = append(notes, fmt.Sprintf("Its %d schedule%s now wake %s, which hands the work to %s; they are listed on %s's Scheduler and report in %s's thread.",
			n, plural(n), m.parent.Name, saved.Name, m.parent.Name, m.parent.Name))
	}
	// A sub-agent holds no channel: its parent answers and can hand the work
	// on (channelAgentFor). Rebound now rather than at the next start-up, so
	// the parent answers from the moment the page says it does.
	var moved []string
	for _, ch := range ListChannelsForAgent(RootDB, m.owner, saved.ID) {
		SaveChannel(RootDB, ch)
		moved = append(moved, chFirst(ch.Name, ch.ID))
	}
	if len(moved) > 0 {
		notes = append(notes, fmt.Sprintf("Its channel%s (%s) %s now answered by %s, which can hand the work to it.",
			plural(len(moved)), strings.Join(moved, ", "), chIf(len(moved) == 1, "is", "are"), m.parent.Name))
	}
	return saved, strings.Join(notes, " "), nil
}

// out makes a sub-agent top-level. Its parent called it by ownership, which
// the move ends, so a parent that calls from a list gets it added to the list:
// moving an agent should not quietly break the one that was using it.
func (m *agentMove) out() (AgentRecord, string, error) {
	a := m.agent
	parentID := a.OwnedBy
	a.OwnedBy = ""
	a.Hidden = false
	// Borrowing the parent's tools is a sub-agent arrangement; with no parent
	// there is nothing to borrow from.
	borrowed := a.InheritParentTools
	a.InheritParentTools = false
	saved, err := saveAgentAs(m.db, a, "moved to top level")
	if err != nil {
		return saved, "", err
	}
	Log("[orchestrate.agents] moved %q to top level (was under %q)", saved.Name, parentID)
	notes := []string{saved.Name + " is now a top-level agent."}
	if parent, ok := loadAgent(m.db, parentID); ok && parent.Owner == m.owner {
		// The HEALED mode: a list whose every entry is gone already reads as
		// Allow all, and giving it one live entry would narrow it to this.
		switch healedDispatchMode(parent, m.db, m.owner, m.db, m.owner) {
		case dispatchOnly:
			if !slices.Contains(parent.AllowedDispatchTargets, saved.ID) {
				parent.AllowedDispatchTargets = append(parent.AllowedDispatchTargets, saved.ID)
				if _, err := saveAgentAs(m.db, parent, "kept "+saved.Name+" callable after it moved out"); err != nil {
					Log("[orchestrate.agents] move: could not add %q to %q's dispatch list: %v", saved.Name, parent.Name, err)
				} else {
					notes = append(notes, "Added it to "+parent.Name+"'s dispatch list, so "+parent.Name+" can still call it.")
				}
			}
		case dispatchNone:
			notes = append(notes, parent.Name+" is set to Allow none, so it can no longer call it.")
		}
	}
	if n := scheduleHandoffs(m.owner, parentID, saved.Name); n > 0 {
		notes = append(notes, fmt.Sprintf("%d schedule%s on its old parent still hand work to it and stay there; give it schedules of its own from its Scheduler.", n, plural(n)))
	}
	if borrowed {
		notes = append(notes, "It no longer borrows its old parent's tools; add any it needs under Tools.")
	}
	if saved.DisableExplicit {
		notes = append(notes, "Memory stays off; switch it on under Memory if you want it.")
	}
	return saved, strings.Join(notes, " "), nil
}

// agentMoveOptions is GET /api/agents/<id>/move: the places the agent menu's
// Move offers. POST {owned_by: "<parent id>" | ""} is agentMove.
func (T *OrchestrateApp) agentMoveOptions(c *jsonapi.Call) (any, error) {
	return agentMoveChoices(c.UDB, c.User, c.R.PathValue("id")), nil
}

// agentMove re-parents the agent under owned_by, after newAgentMove has
// checked the destination.
func (T *OrchestrateApp) agentMove(c *jsonapi.Call, body struct {
	OwnedBy string `json:"owned_by"`
}) (any, error) {
	m, err := newAgentMove(c.UDB, c.User, c.R.PathValue("id"), body.OwnedBy)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	saved, note, err := m.apply()
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return map[string]any{"id": saved.ID, "owned_by": saved.OwnedBy, "message": note}, nil
}

// moveChoice is one place the agent menu's Move offers.
type moveChoice struct {
	Value  string `json:"value"`
	Label  string `json:"label"`
	Detail string `json:"detail"`
}

// agentMoveChoices is what the agent menu's Move shows: every place the agent
// can go, each already checked, or why it can go nowhere. Checked per place
// rather than listed and refused on the click, so a shared agent says "stop
// sharing first" instead of offering a list where every choice fails.
func agentMoveChoices(db Database, owner, id string) map[string]any {
	a, ok := loadAgent(db, id)
	if !ok {
		return map[string]any{"options": []moveChoice{}, "why": "Pick one of your own agents first."}
	}
	// Before ownership: a built-in is nobody's, and saying how to move a copy
	// helps more than "not yours".
	if why := agentMovable(a); why != "" {
		return map[string]any{"options": []moveChoice{}, "why": strings.ToUpper(why[:1]) + why[1:] + "."}
	}
	if a.Owner != owner {
		return map[string]any{"options": []moveChoice{}, "why": "Pick one of your own agents first."}
	}
	var places []moveChoice
	if a.OwnedBy != "" {
		places = append(places, moveChoice{Value: "", Label: "Top level (its own agent)",
			Detail: "It shows in your agent list and other agents can call it. Its old parent can still call it, unless that parent is set to Allow none."})
	}
	for _, p := range agentMoveDestinations(db, owner, a) {
		if p.ID == a.OwnedBy {
			continue
		}
		detail := "Only " + p.Name + " can call it. Its schedules and channels go to " + p.Name + ", which hands their work on."
		if a.OwnedBy == "" && (!a.DisableExplicit || !a.DisableInferred) {
			detail += " It keeps its own memory."
		}
		places = append(places, moveChoice{Value: p.ID, Label: "Sub-agent of " + p.Name, Detail: detail})
	}
	var options []moveChoice
	why := ""
	for _, c := range places {
		if _, err := newAgentMove(db, owner, id, c.Value); err != nil {
			if why == "" {
				why = err.Error()
			}
			continue
		}
		options = append(options, c)
	}
	if len(options) == 0 && why == "" {
		why = "There is nowhere to move " + a.Name + ": it can only go under one of your own top-level agents, and you have no other."
	}
	if options == nil {
		options = []moveChoice{}
	}
	return map[string]any{"name": a.Name, "options": options, "why": why}
}

// checkOwnedByChain refuses a parent link that loops: an agent owning itself,
// or owning one of its own ancestors. Every walk up the chain (the lock, the
// unattended-approval sets, channel inheritance) assumes it ends, and a delete
// cascades down it. Held at the save, so no path that sets OwnedBy can skip it.
func checkOwnedByChain(db Database, a AgentRecord) error {
	parent := strings.TrimSpace(a.OwnedBy)
	if parent == "" || a.ID == "" {
		return nil
	}
	seen := map[string]bool{}
	for id := parent; id != "" && !seen[id]; {
		if id == a.ID {
			return fmt.Errorf("an agent can't be a sub-agent of itself or of its own sub-agent")
		}
		seen[id] = true
		p, ok := loadAgent(db, id)
		if !ok {
			break
		}
		id = strings.TrimSpace(p.OwnedBy)
	}
	return nil
}
