package orchestrate

// The three kinds whose records live in this app, registered with the ledger
// that answers "what have I shared" and "what do I have because somebody
// shared it".
//
// Registration rather than a branch in the ledger: it knows nothing about an
// agent, a pipeline or a machine, and adding a fourth kind next year is a file
// like this one rather than an edit to a shared surface. The same shape
// core/ui's registries have, and for the same reason.
//
// Every provider reads the recipient list that already lives on the record and
// revokes through the setter that already owns it. Nothing here is a second
// place a share is stored.

import (
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/shareledger"
)

func registerShareProviders() {
	shareledger.Register("agent", shareledger.Provider{
		Label: "Agent",
		// The one kind with real decisions behind a share, because it is the
		// one that reaches other records: its tools, its documents, its
		// skills, and the key underneath them.
		Candidates: func(owner string) []shareledger.Grant {
			var out []shareledger.Grant
			udb := UserDB(orchestrateBaseDB, owner)
			for _, a := range listAgents(udb, owner) {
				if isShareableAgent(a, owner) && !a.Everyone {
					out = append(out, shareledger.Grant{ID: a.ID, Name: a.Name})
				}
			}
			return out
		},
		Plan:     planAgentShare,
		Share:    shareAgentGuided,
		Carries:  carriedByAgent,
		Manifest: manifestForAgent,
		Mine: func(owner string) []shareledger.Grant {
			var out []shareledger.Grant
			udb := UserDB(orchestrateBaseDB, owner)
			for _, a := range listAgents(udb, owner) {
				if !isShareableAgent(a, owner) {
					continue
				}
				switch {
				case a.Everyone:
					out = append(out, shareledger.Grant{
						ID: a.ID, Name: a.Name, Reach: "Published to everybody", Wide: true,
						Detail: agentShareDetail(udb, owner, a),
					})
				case len(a.AllowedUsers) > 0:
					out = append(out, shareledger.Grant{
						ID: a.ID, Name: a.Name, Recipients: a.AllowedUsers,
						Reach:     "Shared with " + strings.Join(a.AllowedUsers, ", "),
						Detail:    agentShareDetail(udb, owner, a),
						Revocable: true,
					})
				}
			}
			return out
		},
		ToMe: func(user string) []shareledger.Grant {
			var out []shareledger.Grant
			for _, a := range SharedAgentsFor(orchestrateBaseDB, user) {
				out = append(out, shareledger.Grant{
					ID: a.ID, Name: a.Name, Owner: a.Owner,
					Reach:  "Yours to run, not to change",
					Detail: "It runs in YOUR namespace: your tools, your credentials, your guardrails.",
				})
			}
			return out
		},
		Revoke: func(owner, id, recipient string) error {
			udb := UserDB(orchestrateBaseDB, owner)
			a, ok := loadAgent(udb, id)
			if !ok || a.Owner != owner {
				return Error("no agent " + id + " owned by " + owner)
			}
			if recipient == "" {
				a.AllowedUsers = nil
			} else {
				a.AllowedUsers = withoutOne(a.AllowedUsers, recipient)
			}
			// saveAgent takes back whatever the fan-out granted on this
			// agent's behalf, so a revoke here reaches the dependencies too.
			_, err := saveAgent(udb, a)
			return err
		},
	})

	shareledger.Register("pipeline", shareledger.Provider{
		Label: "Pipeline",
		Candidates: func(owner string) []shareledger.Grant {
			var out []shareledger.Grant
			for _, d := range ListPipelineDefs(UserDB(orchestrateBaseDB, owner), owner) {
				if d.Owner == owner && !d.Published {
					out = append(out, shareledger.Grant{ID: d.ID, Name: d.Name})
				}
			}
			return out
		},
		Share: func(owner, id string, recipients []string, _ map[string]string) []string {
			udb := UserDB(orchestrateBaseDB, owner)
			def, ok := LoadPipelineDef(udb, owner, id)
			if !ok || def.Owner != owner {
				return []string{"That pipeline is not yours to share."}
			}
			def.AllowedUsers = mergeRecipients(def.AllowedUsers, recipients)
			SavePipelineDefAs(udb, def, "shared")
			return []string{
				"Pipeline " + def.Name + " → " + strings.Join(recipients, ", "),
				"Pipeline " + def.Name + ": they run YOUR definition against their own agents, tools and credentials.",
			}
		},
		Mine: func(owner string) []shareledger.Grant {
			var out []shareledger.Grant
			for _, d := range ListPipelineDefs(UserDB(orchestrateBaseDB, owner), owner) {
				if g, ok := recipeGrant(d.ID, d.Name, d.Owner, owner, d.Published, d.AllowedUsers); ok {
					out = append(out, g)
				}
			}
			return out
		},
		ToMe: func(user string) []shareledger.Grant {
			var out []shareledger.Grant
			for _, sp := range sharedPipelinesFor(user) {
				out = append(out, recipeToMe(sp.Def.ID, sp.Def.Name, sp.Owner, sp.Def.Published, "recipe"))
			}
			return out
		},
		Revoke: func(owner, id, recipient string) error {
			udb := UserDB(orchestrateBaseDB, owner)
			def, ok := LoadPipelineDef(udb, owner, id)
			if !ok || def.Owner != owner {
				return Error("no pipeline " + id + " owned by " + owner)
			}
			before := def.AllowedUsers
			def.AllowedUsers = revokeList(def.AllowedUsers, recipient)
			breakSchedulesForLostRecipients(SavePipelineDefAs(udb, def, "share taken back"), before)
			return nil
		},
	})

	shareledger.Register("machine", shareledger.Provider{
		Label: "Machine",
		Candidates: func(owner string) []shareledger.Grant {
			var out []shareledger.Grant
			for _, d := range ListMachineDefs(UserDB(orchestrateBaseDB, owner), owner) {
				if d.Owner == owner && !d.Published {
					out = append(out, shareledger.Grant{ID: d.ID, Name: d.Name})
				}
			}
			return out
		},
		Share: func(owner, id string, recipients []string, _ map[string]string) []string {
			udb := UserDB(orchestrateBaseDB, owner)
			def, ok := LoadMachineDef(udb, owner, id)
			if !ok || def.Owner != owner {
				return []string{"That machine is not yours to share."}
			}
			def.AllowedUsers = mergeRecipients(def.AllowedUsers, recipients)
			SaveMachineDefAs(udb, def, "shared")
			return []string{
				"Machine " + def.Name + " → " + strings.Join(recipients, ", "),
				"Machine " + def.Name + ": they run YOUR definition against their own agents, tools and credentials.",
			}
		},
		Mine: func(owner string) []shareledger.Grant {
			var out []shareledger.Grant
			for _, d := range ListMachineDefs(UserDB(orchestrateBaseDB, owner), owner) {
				if g, ok := recipeGrant(d.ID, d.Name, d.Owner, owner, d.Published, d.AllowedUsers); ok {
					out = append(out, g)
				}
			}
			return out
		},
		ToMe: func(user string) []shareledger.Grant {
			var out []shareledger.Grant
			for _, sm := range sharedMachinesFor(user) {
				out = append(out, recipeToMe(sm.Def.ID, sm.Def.Name, sm.Owner, sm.Def.Published, "procedure"))
			}
			return out
		},
		Revoke: func(owner, id, recipient string) error {
			udb := UserDB(orchestrateBaseDB, owner)
			def, ok := LoadMachineDef(udb, owner, id)
			if !ok || def.Owner != owner {
				return Error("no machine " + id + " owned by " + owner)
			}
			before := def.AllowedUsers
			def.AllowedUsers = revokeList(def.AllowedUsers, recipient)
			breakMachineSchedulesForLostRecipients(SaveMachineDefAs(udb, def, "share taken back"), before)
			return nil
		},
	})
}

// recipeGrant is the owner's-side row for a pipeline or a machine, which share
// a rule: the record stays put and the grant widens on it, so publishing and
// naming people are the same field answered two ways.
func recipeGrant(id, name, recOwner, owner string, published bool, users []string) (shareledger.Grant, bool) {
	if recOwner != owner {
		return shareledger.Grant{}, false
	}
	switch {
	case published:
		return shareledger.Grant{ID: id, Name: name, Reach: "Deployment-wide", Wide: true,
			Detail: "Take it back from its own page, without asking anybody."}, true
	case len(users) > 0:
		return shareledger.Grant{ID: id, Name: name, Recipients: users,
			Reach:     "Shared with " + strings.Join(users, ", "),
			Revocable: true}, true
	}
	return shareledger.Grant{}, false
}

func recipeToMe(id, name, owner string, published bool, noun string) shareledger.Grant {
	reach, wide := "Yours to run and copy, not to edit", false
	if published {
		reach, wide = "Published to everybody", true
	}
	return shareledger.Grant{ID: id, Name: name, Owner: owner, Reach: reach, Wide: wide,
		Detail: "You run their " + noun + " against your own agents, tools and credentials."}
}

// agentShareDetail is the manifest line: what somebody holding this agent still
// has to supply. It is the reach walk's own answer, said in one line.
func agentShareDetail(udb Database, owner string, a AgentRecord) string {
	switch n := agentReachOf(udb, owner, a).Gaps; n {
	case 0:
		return ""
	case 1:
		return "1 thing it uses does not reach them yet."
	default:
		return strconv.Itoa(n) + " things it uses do not reach them yet."
	}
}

func revokeList(users []string, recipient string) []string {
	if recipient == "" {
		return nil
	}
	return withoutOne(users, recipient)
}

func withoutOne(list []string, drop string) []string {
	out := []string{}
	for _, u := range list {
		if u != drop {
			out = append(out, u)
		}
	}
	return out
}
