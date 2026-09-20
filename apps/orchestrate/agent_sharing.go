// agent_sharing.go — peer-sharing of user-owned agents (namespacing phase 5,
// deliverable 5, owner + admin side). An agent's peer-share recipient set lives
// in AgentRecord.AllowedUsers (empty = private to the owner). The owner edits it
// with the "Share with users" ACLPicker on the agent page; this file provides the
// non-owner surfaces: the candidate list the picker reads, and the admin
// governance hooks (enumerate + revoke) the admin console calls.
//
// Recipient-side resolution is BUILT (see SharedAgentsFor at the bottom): a
// shared agent appears in the recipient's fleet catalog and resolves on
// dispatch, by id and by name, carrying its owner so the run happens in the
// owner's context while the recipient's own credentials and tools resolve in
// theirs. Until that existed the ACL was decorative — an owner picked
// recipients, an admin could audit and revoke them, and nothing ever appeared
// for the person named.
package orchestrate

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	AdminListUserOwnedAgents = listUserOwnedAgentsForAdmin
	AdminRevokeAgentShare = revokeAgentShareForAdmin
	AdminPublishAgent = publishAgentForAdmin
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
				Exposed:    a.Exposed,
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

// publishAgentForAdmin is the admin's top-down "delegate to users" step: it flips
// a user-owned agent's Exposed on, so it becomes a /agents/<slug> app the admin can
// then grant to users (or reach directly). The owner's peer-share (AllowedUsers)
// is left intact — publishing widens reach, it doesn't replace the share. Only a
// top-level user agent can be published (seeds / sub-agents can't); saveAgent's own
// guards also refuse Exposed on a clone-only seed.
func publishAgentForAdmin(db Database, owner, id string) error {
	if db == nil || owner == "" || id == "" {
		return fmt.Errorf("owner and id required")
	}
	udb := UserDB(db, owner)
	a, ok := loadAgent(udb, id)
	if !ok {
		return fmt.Errorf("no agent %q owned by %q", id, owner)
	}
	if !isShareableAgent(a, owner) {
		return fmt.Errorf("only a top-level user agent can be published")
	}
	a.Owner = owner
	a.Exposed = true
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

// SharedAgentsTable indexes peer shares: recipient -> (owner, agent id).
const SharedAgentsTable = "shared_agents"

// SharedAgentsFor returns the agents other people have shared WITH this user,
// read from each owner's own store.
//
// This is the recipient-side resolution the top of this file described as "a
// separate step". Until it existed the ACL was decorative: an owner picked
// recipients, an admin could audit and revoke them, and nothing ever appeared
// for the person named. A picker that stores names and changes nothing is worse
// than no picker, because the owner believes they shared something.
//
// Each record comes back with its OWNER intact, which is what makes a run
// resolve in the owner's context while the recipient's own credentials and
// tools resolve in theirs — no secret travels with the share.
func SharedAgentsFor(db Database, user string) []AgentRecord {
	if RootDB == nil || strings.TrimSpace(user) == "" {
		return nil
	}
	var out []AgentRecord
	for _, ref := range ListPeerShares(RootDB, SharedAgentsTable, user) {
		udb := UserDB(db, ref.Owner)
		if udb == nil {
			continue
		}
		a, ok := loadAgent(udb, ref.ID)
		if !ok {
			continue
		}
		// Re-checked against the record rather than trusted from the index: the
		// list on the agent is what the owner edits and an admin revokes, the
		// index is derived, and a derived thing that can outvote its source is
		// how a revoked share keeps working.
		if !isShareableAgent(a, ref.Owner) || !userCanRunSharedAgent(a, user) {
			continue
		}
		// Hidden is the owner's own fleet-visibility choice and says nothing
		// about a share: an agent hidden from its owner's dispatch list is
		// still the thing they handed over deliberately.
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
