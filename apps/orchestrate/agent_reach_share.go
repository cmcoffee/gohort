package orchestrate

// The lends a share made, so taking the share back takes them back too.
//
// This was a general fan-out ledger: sharing an agent granted every dependency
// it touched, and every grant was recorded so removing somebody could undo
// exactly what the share had done and nothing else. Dependencies now TRAVEL
// with the agent and are scoped to it, so there is nothing to grant and
// nothing to record — except the one thing that never travels.
//
// A credential lent as part of sharing an agent is still a real grant, made
// for one reason. When the person is taken off the agent, the reason is gone
// and so should the lend be. What it still must not do is claw back a lend the
// owner made by hand, months ago, for something else — which is the whole
// reason the grants are recorded rather than recomputed from what the lists
// happen to hold now.

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() { OnTeamsChanged(reconcileShareLends) }

// shareLendTable records credential lends made through an agent share: key is
// the owner, the agent and the credential; value is the people THAT share lent
// it to.
const shareLendTable = "agent_share_lends"

func shareLendKey(owner, agentID, cred string) string {
	return owner + "\x00" + agentID + "\x00" + cred
}

// recordShareLend notes that sharing this agent lent this key to these people.
func recordShareLend(owner, agentID, cred string, recipients []string) {
	if orchestrateBaseDB == nil || len(recipients) == 0 {
		return
	}
	key := shareLendKey(owner, agentID, cred)
	var existing []string
	orchestrateBaseDB.Get(shareLendTable, key, &existing)
	for _, u := range recipients {
		if !namedIn(existing, u) {
			existing = append(existing, u)
		}
	}
	orchestrateBaseDB.Set(shareLendTable, key, existing)
}

// withdrawAgentShare takes back the credential lends this agent's share made to
// the people who have just left it.
//
// Only those. A key the owner lent somebody by hand survives an un-share of an
// agent that happened to use it; without that distinction the first convenient
// share would make every later removal a thing to be afraid of.
func withdrawAgentShare(owner, agentID string, dropped []string) []string {
	if orchestrateBaseDB == nil || len(dropped) == 0 {
		return nil
	}
	var out []string
	prefix := owner + "\x00" + agentID + "\x00"
	for _, k := range orchestrateBaseDB.Keys(shareLendTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var lent []string
		if !orchestrateBaseDB.Get(shareLendTable, k, &lent) {
			continue
		}
		cred := strings.TrimPrefix(k, prefix)
		var take []string
		for _, u := range lent {
			if namedIn(dropped, u) {
				take = append(take, u)
			}
		}
		if len(take) == 0 {
			continue
		}
		if _, ok := Secure().LoadUser(owner, cred); !ok {
			orchestrateBaseDB.Unset(shareLendTable, k)
			continue
		}
		// This agent comes off their lend; a lend they hold for another
		// agent, or one made by hand, stays.
		gone, err := Secure().TakeBackForAgent(owner, cred, agentID, take)
		if err != nil {
			continue
		}
		if len(gone) > 0 {
			out = append(out, "credential "+cred+" → no longer "+strings.Join(gone, ", "))
		}
		if rest := remaining(lent, take); len(rest) > 0 {
			orchestrateBaseDB.Set(shareLendTable, k, rest)
		} else {
			orchestrateBaseDB.Unset(shareLendTable, k)
		}
	}
	return out
}

func remaining(list, taken []string) []string {
	out := []string{}
	for _, u := range list {
		if !namedIn(taken, u) {
			out = append(out, u)
		}
	}
	return out
}

// leftBehind returns the members of before that are not in after: whoever this
// write takes off a recipient list.
func leftBehind(before, after []string) []string {
	out := []string{}
	for _, u := range before {
		if !namedIn(after, u) {
			out = append(out, u)
		}
	}
	return out
}

// withdrawOneCredentialLend takes back everything this agent's share lent of
// ONE key, to everybody it lent it to.
//
// The sibling of withdrawAgentShare, which drops people; this drops a key. Same
// discipline and the same reason for it: only what the ledger says this share
// granted, so a lend the owner made by hand for something else survives being
// changed here.
func withdrawOneCredentialLend(owner, agentID, cred string) string {
	if orchestrateBaseDB == nil {
		return ""
	}
	key := shareLendKey(owner, agentID, cred)
	var lent []string
	if !orchestrateBaseDB.Get(shareLendTable, key, &lent) || len(lent) == 0 {
		return ""
	}
	if _, ok := Secure().LoadUser(owner, cred); !ok {
		orchestrateBaseDB.Unset(shareLendTable, key)
		return ""
	}
	gone, err := Secure().TakeBackForAgent(owner, cred, agentID, lent)
	if err != nil {
		return ""
	}
	orchestrateBaseDB.Unset(shareLendTable, key)
	if len(gone) == 0 {
		return ""
	}
	return "credential " + cred + " → no longer " + strings.Join(gone, ", ")
}

// reconcileShareLends takes back the lends agent shares made to people those
// shares no longer reach. Run whenever teams change: someone leaving a team,
// the owner leaving it, the team losing "can share" or being deleted all end
// a team share without anybody editing the agent, so the save path that
// usually takes lends back never runs.
//
// Only takes back. Someone joining the team gets the agent but not the key;
// the agent's share panel shows them as missing it, and the owner lends it to
// them from there.
func reconcileShareLends() {
	if orchestrateBaseDB == nil {
		return
	}
	lost := map[string][]string{} // owner + "\x00" + agent id -> people
	for _, k := range orchestrateBaseDB.Keys(shareLendTable) {
		parts := strings.SplitN(k, "\x00", 3)
		if len(parts) != 3 {
			continue
		}
		owner, agentID := parts[0], parts[1]
		var lent []string
		if !orchestrateBaseDB.Get(shareLendTable, k, &lent) {
			continue
		}
		a, ok := loadAgent(UserDB(orchestrateBaseDB, owner), agentID)
		for _, u := range lent {
			if ok && a.Owner == owner && SharedWithUser(a.AllowedUsers, owner, u) {
				continue
			}
			ak := owner + "\x00" + agentID
			if !namedIn(lost[ak], u) {
				lost[ak] = append(lost[ak], u)
			}
		}
	}
	for ak, people := range lost {
		owner, agentID, _ := strings.Cut(ak, "\x00")
		if lines := withdrawAgentShare(owner, agentID, people); len(lines) > 0 {
			Log("[orchestrate.share] teams changed, so %s no longer reach agent %s of %s: %s",
				strings.Join(people, ", "), agentID, owner, strings.Join(lines, "; "))
		}
	}
}

// lendModeFor reads back what this agent's share currently does with one key,
// in the vocabulary the control offers.
//
// Read from the ledger rather than from the credential's own lists, for the
// same reason the ledger exists: a key the owner lent by hand is not this
// agent's doing, and showing its pill as "lend mine" would invite somebody to
// move it and quietly revoke a grant made for another reason entirely.
func lendModeFor(owner, agentID, cred string, disabled bool) string {
	if disabled {
		return shareSkip
	}
	if orchestrateBaseDB == nil {
		return credOwn
	}
	var lent []string
	if !orchestrateBaseDB.Get(shareLendTable, shareLendKey(owner, agentID, cred), &lent) || len(lent) == 0 {
		return credOwn
	}
	c, ok := Secure().LoadUser(owner, cred)
	if !ok {
		return credOwn
	}
	// Writes if anybody this share lent to holds the stronger grant: the
	// control sets one answer for the whole recipient list, so the stronger
	// one is what it last set.
	for _, u := range lent {
		if namedIn(c.SharedReadWrite, u) {
			return credWrite
		}
	}
	for _, u := range lent {
		if namedIn(c.SharedReadOnly, u) {
			return credRead
		}
	}
	return credOwn
}
