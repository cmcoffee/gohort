package orchestrate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
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
	if agentLocked(db, a) {
		return nil, fmt.Errorf("this agent is locked: unlock it (the 🔒 icon) before moving it")
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
	if n := len(a.AllowedUsers); n > 0 {
		reach = append(reach, fmt.Sprintf("shared with %d %s", n, map[bool]string{true: "person", false: "people"}[n == 1]))
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
	a.OwnedBy = m.parent.ID
	a = enforceSubAgentPosture(a)
	saved, err := saveAgentAs(m.db, a, "moved under "+m.parent.Name)
	if err != nil {
		return saved, "", err
	}
	Log("[orchestrate.agents] moved %q under %q", saved.Name, m.parent.Name)
	return saved, fmt.Sprintf("%s is now a sub-agent of %s. Only %s can call it.", saved.Name, m.parent.Name, m.parent.Name), nil
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
	if borrowed {
		notes = append(notes, "It no longer borrows its old parent's tools; add any it needs under Tools.")
	}
	if saved.DisableExplicit {
		notes = append(notes, "Memory stays off; switch it on under Memory if you want it.")
	}
	return saved, strings.Join(notes, " "), nil
}

// handleAgentMove is POST /api/agents/<id>/move {owned_by: "<parent id>" | ""}.
func (T *OrchestrateApp) handleAgentMove(w http.ResponseWriter, r *http.Request, user string, udb Database, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		OwnedBy string `json:"owned_by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	m, err := newAgentMove(udb, user, id, body.OwnedBy)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	saved, note, err := m.apply()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": saved.ID, "owned_by": saved.OwnedBy, "message": note})
}

// agentMoveSection is the editor's Move control, or false when the agent has no
// other place to go.
func agentMoveSection(db Database, owner string, a AgentRecord) (ui.Section, bool) {
	if agentMovable(a) != "" || a.Owner != owner {
		return ui.Section{}, false
	}
	var opts []ui.SelectOption
	if a.OwnedBy != "" {
		opts = append(opts, ui.SelectOption{Value: "", Label: "Top level (its own agent)"})
	}
	for _, p := range agentMoveDestinations(db, owner, a) {
		if p.ID != a.OwnedBy {
			opts = append(opts, ui.SelectOption{Value: p.ID, Label: "Sub-agent of " + p.Name})
		}
	}
	if len(opts) == 0 {
		return ui.Section{}, false
	}
	sub := "Make it a sub-agent of another of your agents."
	detail := "It keeps its sessions, knowledge and history: the same agent, in a new place. As a sub-agent only its parent can call it, and it has no dashboard card, intake form, memory or sharing. An agent that is shared, or has sub-agents of its own, has to stop being either first."
	if a.OwnedBy != "" {
		sub = "Make it a top-level agent, or move it under a different parent."
		detail = "It keeps its sessions, knowledge and history: the same agent, in a new place. As a top-level agent it shows in your agent list and other agents can call it. Its old parent can still call it (if that parent calls from a list, the agent is added to the list), unless the parent is set to Allow none."
	}
	return ui.Section{
		Title:    "Move",
		Subtitle: sub,
		Detail:   detail,
		// No Source: the form sends only the choice, defaulting to the first
		// one listed, never the agent's current place echoed back.
		Body: ui.FormPanel{
			PostURL:     "../api/agents/" + a.ID + "/move",
			Method:      "POST",
			SubmitLabel: "Move agent",
			// The editor's shape follows where the agent lives, so the page
			// reloads into the right one.
			RedirectURL:    "../agent/" + a.ID,
			RedirectTarget: "_self",
			Fields: []ui.FormField{
				{Field: "owned_by", Type: "select", Label: "Move to", Options: opts, Default: opts[0].Value},
			},
		},
	}, true
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
