// agent_sharing.go — peer-sharing of user-owned agents (namespacing phase 5,
// deliverable 5, owner + admin side). An agent's peer-share recipient set lives
// in AgentRecord.AllowedUsers (empty = private to the owner). The owner edits it
// with the "Share with users" ACLPicker on the agent page; this file provides the
// non-owner surfaces: the candidate list the picker reads, and the admin
// governance hooks (enumerate + revoke) the admin console calls.
//
// Recipient-side resolution (a shared agent appearing in a recipient's fleet and
// running in the owner's context with the recipient's own credentials) is a
// separate step — the AllowedUsers data + userCanRunSharedAgent gate below are its
// foundation.
package orchestrate

import (
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	AdminListUserOwnedAgents = listUserOwnedAgentsForAdmin
	AdminRevokeAgentShare = revokeAgentShareForAdmin
}

// isShareableAgent reports whether an agent record is a user's OWN top-level agent
// — the only kind that can be peer-shared. Seeds (framework agents) and sub-agents
// (OwnedBy set — components of a parent, not independently shareable) are excluded.
func isShareableAgent(a AgentRecord, owner string) bool {
	return a.Owner == owner && owner != "" && !isSeedID(a.ID) && a.OwnedBy == ""
}

// userCanRunSharedAgent reports whether reqUser (not the owner) may run a shared
// agent: the agent lists them in AllowedUsers. The owner always can via their own
// store, so this is only consulted for a non-owner. The foundation for
// recipient-side resolution.
func userCanRunSharedAgent(a AgentRecord, reqUser string) bool {
	if reqUser == "" {
		return false
	}
	for _, u := range a.AllowedUsers {
		if u == reqUser {
			return true
		}
	}
	return false
}

// listUserOwnedAgentsForAdmin walks every user's agent store and returns the
// agents they OWN (seeds + sub-agents excluded), each with its peer-share
// recipient list. The admin governance view over user-owned agents — the admin
// app is otherwise blind to them (they live in per-user UDBs).
func listUserOwnedAgentsForAdmin(db Database) []UserOwnedAgentRow {
	out := []UserOwnedAgentRow{}
	if db == nil {
		return out
	}
	for _, u := range AuthListUsers(db) {
		udb := UserDB(db, u.Username)
		for _, a := range listAgents(udb, u.Username) {
			if !isShareableAgent(a, u.Username) {
				continue
			}
			out = append(out, UserOwnedAgentRow{
				ID:         a.ID,
				Owner:      a.Owner,
				Name:       a.Name,
				SharedWith: strings.Join(a.AllowedUsers, ", "),
				Shared:     len(a.AllowedUsers) > 0,
			})
		}
	}
	return out
}

// revokeAgentShareForAdmin clears an agent's peer-share recipient list (the owner
// keeps the agent; recipients lose access). Admin-driven from the governance
// console. Loads + saves in the owner's own store.
func revokeAgentShareForAdmin(db Database, owner, id string) error {
	if db == nil || owner == "" || id == "" {
		return fmt.Errorf("owner and id required")
	}
	udb := UserDB(db, owner)
	a, ok := loadAgent(udb, id)
	if !ok {
		return fmt.Errorf("no agent %q owned by %q", id, owner)
	}
	a.Owner = owner
	a.AllowedUsers = nil
	_, err := saveAgent(udb, a)
	return err
}

// handleUserCandidates serves the ACL-picker candidate list ([{value,label}]) for
// the "Share with users" picker on the agent page. Any authenticated user may
// share their own agent, so every approved user is a candidate — unlike the
// admin-only api/user-candidates, this one is available to the whole fleet.
func (T *OrchestrateApp) handleUserCandidates(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(UserCandidatesJSON(AuthDB()))
}
