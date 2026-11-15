package orchestrate

// A person's main agent: their go-to assistant, where an ask that names no
// agent goes (MCP) and where Agents opens unless they chose otherwise. It is
// recorded on their account (AuthUser.MainAgent) when the first-run wizard
// makes their assistant, and they can change it on the account page. An
// account with none recorded, or whose recorded one is gone, falls back to
// what it plainly is: their one front-door agent (Fleet, as the first-run
// assistant is made), else their one agent. No guessing between several:
// with more than one candidate and none chosen, there is no main agent, and
// an ask that names none is told to name one.

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// mainAgentCandidate reports an agent that may be someone's main agent: one
// of their own, standing on its own (not a sub-agent or an app's agent), and
// not a framework seed.
func mainAgentCandidate(a AgentRecord, owner string) bool {
	return a.Owner == owner && !isSeedID(a.ID) && strings.TrimSpace(a.OwnedBy) == "" &&
		strings.TrimSpace(a.OwningApp) == "" && !isAppAgent(a.ID)
}

// mainAgentFor is owner's main agent, and whether they have one.
func mainAgentFor(owner string) (AgentRecord, bool) {
	if orchestrateBaseDB == nil || strings.TrimSpace(owner) == "" {
		return AgentRecord{}, false
	}
	udb := UserDB(orchestrateBaseDB, owner)
	if id := recordedMainAgent(owner); id != "" {
		if a, ok := loadAgent(udb, id); ok && mainAgentCandidate(a, owner) {
			return a, true
		}
	}
	var front, all []AgentRecord
	for _, a := range listAgents(udb, owner) {
		if !mainAgentCandidate(a, owner) {
			continue
		}
		all = append(all, a)
		if a.Fleet {
			front = append(front, a)
		}
	}
	if len(front) == 1 {
		return front[0], true
	}
	if len(front) == 0 && len(all) == 1 {
		return all[0], true
	}
	return AgentRecord{}, false
}

// mainAgentID is owner's main agent's id, "" when they have none.
func mainAgentID(owner string) string {
	a, _ := mainAgentFor(owner)
	return a.ID
}

// recordedMainAgent is the main agent recorded on owner's account.
func recordedMainAgent(owner string) string {
	if AuthDB == nil {
		return ""
	}
	db := AuthDB()
	if db == nil {
		return ""
	}
	u, _ := AuthGetUser(db, owner)
	return strings.TrimSpace(u.MainAgent)
}

// setMainAgent records id as owner's main agent: one of their own agents
// that may be one.
func setMainAgent(owner, id string) error {
	if AuthDB == nil || orchestrateBaseDB == nil {
		return fmt.Errorf("not ready")
	}
	a, ok := loadAgent(UserDB(orchestrateBaseDB, owner), strings.TrimSpace(id))
	if !ok || !mainAgentCandidate(a, owner) {
		return fmt.Errorf("that is not one of your own agents")
	}
	db := AuthDB()
	u, ok := AuthGetUser(db, owner)
	if !ok {
		return fmt.Errorf("no such account")
	}
	u.MainAgent = a.ID
	db.Set(AuthTable, "user:"+owner, u)
	return nil
}
