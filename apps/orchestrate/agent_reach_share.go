package orchestrate

// Sharing what an agent needs, in the one action the person actually asked for.
//
// "Share this with my team so we are all working from the same agent" is one
// request. agent_reach.go established what that agent depends on and how far
// each of those goes; this closes the gaps, by adding the agent's own
// recipients to each dependency's own recipient list, through each kind's own
// setter. No new grant model, no copies, no second ACL — the rungs are exactly
// what they were, and what changes is that the person states the outcome
// instead of performing the assembly.
//
// WHAT IT WILL NOT DO. A tool, because a user's own tool has no rung between
// private and the admin-published catalog; the panel says so rather than
// offering a button that fails. A credential, because that one is not a copy
// somebody is missing — it is whose identity the call goes out as, and the
// ordinary answer for a team is that each person supplies their own. And
// anything at all for a PUBLISHED agent, where closing a gap means the
// deployment rung and that is an administrator's to grant.
//
// TAKING IT BACK. A fan-out that could not be undone would make the first
// convenient click a permanent tail of grants nobody remembers making. So every
// grant this makes is recorded, and only those are offered back: a share the
// owner made by hand, for their own reasons, is never clawed back by a later
// un-share of some agent that happened to use the same collection. The ledger
// exists for exactly that distinction and is consulted for nothing else.

import (
	"sort"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// fanoutLedgerTable records the grants a fan-out made: key is the agent and the
// record it granted, value is the recipients THIS action added. Kept in the
// app's own store beside the share indexes.
const fanoutLedgerTable = "agent_share_fanout"

func fanoutKey(owner, agentID, kind, id string) string {
	return owner + "\x00" + agentID + "\x00" + kind + "\x00" + id
}

// fanOutAgentShare gives every dependency the agent's own recipients.
//
// Returns one line per record it touched, and one per gap it could not close,
// because a report that lists only successes is how somebody concludes the job
// is done when a tool is still missing.
func fanOutAgentShare(udb Database, owner string, a AgentRecord) []string {
	reach := agentReachOf(udb, owner, a)
	if reach.audience != reachNamed {
		if reach.audience == reachDeployment {
			return []string{"This agent is published to everybody, so closing a gap means publishing each thing it needs — which an admin approves, one kind at a time."}
		}
		return []string{"This agent is private, so there is nobody to share anything with yet."}
	}
	var done, refused []string
	for _, it := range reach.shareableGaps() {
		added, err := grantToRecipients(udb, owner, it, a.AllowedUsers)
		switch {
		case err != nil:
			refused = append(refused, it.Kind+" "+it.Name+": "+err.Error())
		case len(added) > 0:
			recordFanout(owner, a.ID, it, added)
			done = append(done, it.Kind+" "+it.Name+" → "+strings.Join(added, ", "))
		}
	}
	out := done
	// Everything the owner still has to do themselves, named. The tools are
	// the usual answer here, and a person who is not told will believe their
	// team has a working agent.
	for _, it := range reach.Items {
		if it.Gap && it.Fix != "" && it.kind == "tool" {
			refused = append(refused, it.Kind+" "+it.Name+": "+it.Fix)
		}
	}
	if len(out) == 0 && len(refused) == 0 {
		return []string{"Nothing to do: everybody who has this agent already has everything it uses."}
	}
	return append(out, refused...)
}

// withdrawAgentShare takes back only what a fan-out gave.
//
// Only. A collection the owner shared with somebody by hand, months ago, for a
// reason that has nothing to do with this agent, is not this function's to
// touch — which is the entire reason the grants were recorded rather than
// recomputed from what the lists happen to hold now.
func withdrawAgentShare(udb Database, owner string, a AgentRecord, dropped []string) []string {
	if orchestrateBaseDB == nil || len(dropped) == 0 {
		return nil
	}
	var out []string
	prefix := owner + "\x00" + a.ID + "\x00"
	for _, k := range orchestrateBaseDB.Keys(fanoutLedgerTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var granted []string
		if !orchestrateBaseDB.Get(fanoutLedgerTable, k, &granted) {
			continue
		}
		parts := strings.Split(k, "\x00")
		if len(parts) != 4 {
			continue
		}
		take := intersect(granted, dropped)
		if len(take) == 0 {
			continue
		}
		name, err := revokeFromRecipients(udb, owner, parts[2], parts[3], take)
		if err != nil {
			continue
		}
		out = append(out, name+" → no longer "+strings.Join(take, ", "))
		if rest := without(granted, take); len(rest) > 0 {
			orchestrateBaseDB.Set(fanoutLedgerTable, k, rest)
		} else {
			orchestrateBaseDB.Unset(fanoutLedgerTable, k)
		}
	}
	return out
}

// grantToRecipients adds users to one record's own recipient list, through the
// setter that owns that list. Returns the ones it actually added.
func grantToRecipients(udb Database, owner string, it reachItem, users []string) ([]string, error) {
	switch it.kind {
	case "skill":
		for _, s := range LoadSkills(udb, owner) {
			if s.ID != it.id {
				continue
			}
			add := missingFrom(s.AllowedUsers, users)
			if len(add) == 0 {
				return nil, nil
			}
			s.AllowedUsers = append(s.AllowedUsers, add...)
			_, err := SaveSkillAs(udb, owner, s, "shared with an agent")
			return add, err
		}
	case "collection":
		cdb := UserDB(CollectionsDB(), owner)
		if c, ok := LoadCollection(cdb, owner, it.id); ok && c.Owner == owner {
			add := missingFrom(c.AllowedUsers, users)
			if len(add) == 0 {
				return nil, nil
			}
			c.AllowedUsers = append(c.AllowedUsers, add...)
			SaveCollection(cdb, c)
			return add, nil
		}
	case "pipeline":
		if def, ok := LoadPipelineDef(udb, owner, it.id); ok && def.Owner == owner {
			add := missingFrom(def.AllowedUsers, users)
			if len(add) == 0 {
				return nil, nil
			}
			def.AllowedUsers = append(def.AllowedUsers, add...)
			SavePipelineDefAs(udb, def, "shared with an agent")
			return add, nil
		}
	case "machine":
		if def, ok := LoadMachineDef(udb, owner, it.id); ok && def.Owner == owner {
			add := missingFrom(def.AllowedUsers, users)
			if len(add) == 0 {
				return nil, nil
			}
			def.AllowedUsers = append(def.AllowedUsers, add...)
			SaveMachineDefAs(udb, def, "shared with an agent")
			return add, nil
		}
	}
	return nil, Error("it is no longer there")
}

// revokeFromRecipients is grantToRecipients backwards, and returns the record's
// name so the report can say what changed rather than quoting an id back.
func revokeFromRecipients(udb Database, owner, kind, id string, users []string) (string, error) {
	switch kind {
	case "skill":
		for _, s := range LoadSkills(udb, owner) {
			if s.ID != id {
				continue
			}
			s.AllowedUsers = without(s.AllowedUsers, users)
			_, err := SaveSkillAs(udb, owner, s, "unshared with an agent")
			return "Skill " + s.Name, err
		}
	case "collection":
		cdb := UserDB(CollectionsDB(), owner)
		if c, ok := LoadCollection(cdb, owner, id); ok && c.Owner == owner {
			c.AllowedUsers = without(c.AllowedUsers, users)
			SaveCollection(cdb, c)
			return "Knowledge " + c.Name, nil
		}
	case "pipeline":
		if def, ok := LoadPipelineDef(udb, owner, id); ok && def.Owner == owner {
			before := def.AllowedUsers
			def.AllowedUsers = without(def.AllowedUsers, users)
			saved := SavePipelineDefAs(udb, def, "unshared with an agent")
			// A share taken back has to take back everything it enabled, which
			// for a recipe includes whatever somebody armed against it.
			breakSchedulesForLostRecipients(saved, before)
			return "Pipeline " + def.Name, nil
		}
	case "machine":
		if def, ok := LoadMachineDef(udb, owner, id); ok && def.Owner == owner {
			before := def.AllowedUsers
			def.AllowedUsers = without(def.AllowedUsers, users)
			saved := SaveMachineDefAs(udb, def, "unshared with an agent")
			breakMachineSchedulesForLostRecipients(saved, before)
			return "Machine " + def.Name, nil
		}
	}
	return "", Error("it is no longer there")
}

func recordFanout(owner, agentID string, it reachItem, added []string) {
	if orchestrateBaseDB == nil {
		return
	}
	key := fanoutKey(owner, agentID, it.kind, it.id)
	var existing []string
	orchestrateBaseDB.Get(fanoutLedgerTable, key, &existing)
	orchestrateBaseDB.Set(fanoutLedgerTable, key, append(existing, missingFrom(existing, added)...))
}

// missingFrom returns the members of want that have not got it yet, sorted so
// a report reads the same twice.
func missingFrom(have, want []string) []string {
	got := map[string]bool{}
	for _, u := range have {
		got[u] = true
	}
	var out []string
	for _, u := range want {
		if u = strings.TrimSpace(u); u != "" && !got[u] {
			got[u] = true
			out = append(out, u)
		}
	}
	sort.Strings(out)
	return out
}

func intersect(a, b []string) []string {
	in := map[string]bool{}
	for _, s := range b {
		in[s] = true
	}
	var out []string
	for _, s := range a {
		if in[s] {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func without(list, drop []string) []string {
	gone := map[string]bool{}
	for _, s := range drop {
		gone[s] = true
	}
	out := []string{}
	for _, s := range list {
		if !gone[s] {
			out = append(out, s)
		}
	}
	return out
}

// fanoutSummary is the one line the button carries, so somebody knows what they
// are about to do before they do it rather than from the result.
func fanoutSummary(r agentReachMap) string {
	n := len(r.shareableGaps())
	if n == 0 {
		return ""
	}
	return "Share the " + strconv.Itoa(n) + " thing(s) above that " + andList(r.recipients) + " cannot reach"
}

func andList(users []string) string {
	switch len(users) {
	case 0:
		return "they"
	case 1:
		return users[0]
	default:
		return strings.Join(users[:len(users)-1], ", ") + " and " + users[len(users)-1]
	}
}
