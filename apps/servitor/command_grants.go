// Who may run WHAT, WHERE — per agent, per appliance.
//
// The auto-run permission was one record per user: a single set of risk
// categories that applied to every agent and every system that user owned.
// "Let this agent restart services on the lab box" had nowhere to live, and
// neither did its complement, "let it do that on the lab box and nowhere
// near production" — ticking a category for one appliance ticked it for all
// of them, on behalf of every agent.
//
// So a grant is keyed by (agent, appliance), with two coarser scopes behind it:
//
//	agent + appliance   this agent, on this system
//	agent + *           this agent, anywhere
//	the user default    the existing per-user record, unchanged
//
// MOST SPECIFIC WINS, AND IT REPLACES. A narrower grant does not add to the
// broader one, it stands in for it — otherwise an agent could never be given
// LESS than the user's own settings, which is the main thing this is for.
//
// PRESENT-BUT-EMPTY MEANS NOTHING AUTO-RUNS. It is not the same as having no
// grant: "this agent may run nothing here without asking" is a decision, and
// falling through to a broader scope would quietly overturn it. Absence is what
// falls through.
package servitor

import (
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// commandGrantsTable holds the per-(agent, appliance) records. Separate from
// ssh_allow_categories so the user-level default keeps working untouched and
// nothing has to be migrated.
const commandGrantsTable = "ssh_command_grants"

// anyAppliance is the wildcard for an agent-wide grant.
const anyAppliance = "*"

// CommandGrant is one scope's auto-run set. Categories are stored as strings so
// an unrecognized name in an old record is dropped on read rather than
// widening anything.
type CommandGrant struct {
	AgentID     string   `json:"agent_id"`
	ApplianceID string   `json:"appliance_id"` // "*" = every appliance
	Categories  []string `json:"categories"`   // empty = nothing auto-runs here
}

// commandGrantKey is the record id. Both parts are normalized so a grant saved
// from the UI and one resolved during a run cannot miss each other on case.
func commandGrantKey(agentID, applianceID string) string {
	a := strings.ToLower(strings.TrimSpace(agentID))
	p := strings.ToLower(strings.TrimSpace(applianceID))
	if p == "" {
		p = anyAppliance
	}
	return "agent:" + a + "|appliance:" + p
}

// GrantScope names which record answered a lookup. Returned alongside the set
// because "why did this command run without asking" is the question anyone
// debugging this will have, and a bare category set cannot answer it.
type GrantScope string

const (
	ScopeAgentAppliance GrantScope = "agent+appliance"
	ScopeAgentAny       GrantScope = "agent (any appliance)"
	ScopeUserDefault    GrantScope = "user default"
)

// ResolveCommandGrant returns the categories that auto-run for this agent on
// this appliance, and which scope decided it. Falls back to the per-user record
// when no grant names this agent, so a deployment that has never used grants
// behaves exactly as it did.
func ResolveCommandGrant(udb Database, agentID, applianceID string) (map[RiskCategory]bool, GrantScope) {
	if udb == nil {
		return map[RiskCategory]bool{}, ScopeUserDefault
	}
	if agentID = strings.TrimSpace(agentID); agentID != "" {
		if g, ok := loadCommandGrant(udb, agentID, applianceID); ok {
			return categorySet(g.Categories), ScopeAgentAppliance
		}
		if g, ok := loadCommandGrant(udb, agentID, anyAppliance); ok {
			return categorySet(g.Categories), ScopeAgentAny
		}
	}
	return loadAllowedCategories(udb), ScopeUserDefault
}

// loadCommandGrant reads one exact scope. The bool reports whether a record
// EXISTS — an empty Categories on a present record is a real answer (nothing
// auto-runs) and must not be mistaken for "no grant here".
func loadCommandGrant(udb Database, agentID, applianceID string) (CommandGrant, bool) {
	if udb == nil {
		return CommandGrant{}, false
	}
	var g CommandGrant
	if !udb.Get(commandGrantsTable, commandGrantKey(agentID, applianceID), &g) {
		return CommandGrant{}, false
	}
	return g, true
}

// SaveCommandGrant writes one scope, keeping only recognized category names so
// a typo cannot sit in a record looking like a permission.
func SaveCommandGrant(udb Database, agentID, applianceID string, categories []string) CommandGrant {
	g := CommandGrant{
		AgentID:     strings.TrimSpace(agentID),
		ApplianceID: strings.TrimSpace(applianceID),
		Categories:  cleanCategories(categories),
	}
	if g.ApplianceID == "" {
		g.ApplianceID = anyAppliance
	}
	if udb != nil && g.AgentID != "" {
		udb.Set(commandGrantsTable, commandGrantKey(g.AgentID, g.ApplianceID), g)
	}
	return g
}

// DeleteCommandGrant removes one scope, so lookups fall through to the next one
// out. Distinct from saving an empty grant, which denies at that scope.
func DeleteCommandGrant(udb Database, agentID, applianceID string) {
	if udb == nil {
		return
	}
	udb.Unset(commandGrantsTable, commandGrantKey(agentID, applianceID))
}

// ListCommandGrants returns every grant, agent then appliance, for a UI that
// has to show what has been handed out.
func ListCommandGrants(udb Database) []CommandGrant {
	if udb == nil {
		return nil
	}
	var out []CommandGrant
	for _, k := range udb.Keys(commandGrantsTable) {
		var g CommandGrant
		if udb.Get(commandGrantsTable, k, &g) {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AgentID != out[j].AgentID {
			return out[i].AgentID < out[j].AgentID
		}
		return out[i].ApplianceID < out[j].ApplianceID
	})
	return out
}

// cleanCategories keeps only names that are real risk categories today.
func cleanCategories(in []string) []string {
	var out []string
	for _, c := range AllRiskCategories {
		for _, want := range in {
			if strings.EqualFold(strings.TrimSpace(want), string(c)) {
				out = append(out, string(c))
				break
			}
		}
	}
	return out
}

// categorySet turns stored names into the lookup the exec path uses.
func categorySet(names []string) map[RiskCategory]bool {
	out := map[RiskCategory]bool{}
	for _, c := range AllRiskCategories {
		for _, n := range names {
			if strings.EqualFold(strings.TrimSpace(n), string(c)) {
				out[c] = true
				break
			}
		}
	}
	return out
}
