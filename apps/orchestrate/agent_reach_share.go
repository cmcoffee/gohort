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
		c, ok := Secure().LoadUser(owner, cred)
		if !ok {
			orchestrateBaseDB.Unset(shareLendTable, k)
			continue
		}
		read, write := c.SharedReadOnly, c.SharedReadWrite
		for _, u := range take {
			read, write = withoutOne(read, u), withoutOne(write, u)
		}
		if err := Secure().SetCredentialShares(owner, cred, read, write); err != nil {
			continue
		}
		out = append(out, "credential "+cred+" → no longer "+strings.Join(take, ", "))
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
