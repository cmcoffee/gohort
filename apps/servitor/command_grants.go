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

// NO WILDCARD. A grant names one agent and one machine, and there is no value
// meaning "all of them".
//
// A wildcard reads as a convenience and behaves as a standing decision about
// machines that do not exist yet: every appliance added afterwards is covered
// by a choice made before anyone had seen it. That is the opposite of what this
// permission is for, and it cannot be reviewed — a list of grants can be read,
// an "everything" cannot.
//
// The cost is a row per pairing, which is the honest amount of work for a
// decision that is genuinely per pairing.

// CommandGrant is one scope's auto-run set. Categories are stored as strings so
// an unrecognized name in an old record is dropped on read rather than
// widening anything.
type CommandGrant struct {
	AgentID     string   `json:"agent_id"`
	ApplianceID string   `json:"appliance_id"` // "*" = every appliance
	Categories  []string `json:"categories"`   // empty = nothing auto-runs here
}

// normalizeAgentID strips the "agent:" namespace the grantable-targets picker
// wraps around agent ids. The picker value exists to keep agents apart from
// channels in one list; the runtime dispatches with the BARE id, and a grant
// saved under the wrapped form can never be found by the run that needs it.
func normalizeAgentID(agentID string) string {
	agentID = strings.TrimSpace(agentID)
	if len(agentID) > 6 && strings.EqualFold(agentID[:6], "agent:") {
		agentID = strings.TrimSpace(agentID[6:])
	}
	return agentID
}

// commandGrantKey is the record id. Both parts are normalized so a grant saved
// from the UI and one resolved during a run cannot miss each other on case or
// on the picker's "agent:" wrapper.
func commandGrantKey(agentID, applianceID string) string {
	a := strings.ToLower(normalizeAgentID(agentID))
	p := strings.ToLower(strings.TrimSpace(applianceID))
	return "agent:" + a + "|appliance:" + p
}

// legacyCommandGrantKey is the key a grant landed under before agent ids were
// normalized: the picker's "agent:<id>" value taken verbatim, doubling the
// prefix. Kept only so records written then are found and re-keyed.
func legacyCommandGrantKey(agentID, applianceID string) string {
	a := strings.ToLower(normalizeAgentID(agentID))
	p := strings.ToLower(strings.TrimSpace(applianceID))
	return "agent:agent:" + a + "|appliance:" + p
}

// GrantScope names which record answered a lookup. Returned alongside the set
// because "why did this command run without asking" is the question anyone
// debugging this will have, and a bare category set cannot answer it.
type GrantScope string

const (
	ScopeAgentAppliance GrantScope = "agent+appliance"
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
	if agentID = normalizeAgentID(agentID); agentID != "" {
		if g, ok := loadCommandGrant(udb, agentID, applianceID); ok {
			return categorySet(g.Categories), ScopeAgentAppliance
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
	if udb.Get(commandGrantsTable, commandGrantKey(agentID, applianceID), &g) {
		g.AgentID = normalizeAgentID(g.AgentID)
		return g, true
	}
	// A record written before ids were normalized sits under the doubled key
	// with the wrapped id inside. Re-key it now so the next lookup is direct.
	if udb.Get(commandGrantsTable, legacyCommandGrantKey(agentID, applianceID), &g) {
		g.AgentID = normalizeAgentID(g.AgentID)
		udb.Set(commandGrantsTable, commandGrantKey(agentID, applianceID), g)
		udb.Unset(commandGrantsTable, legacyCommandGrantKey(agentID, applianceID))
		return g, true
	}
	return CommandGrant{}, false
}

// SaveCommandGrant writes one scope, keeping only recognized category names so
// a typo cannot sit in a record looking like a permission.
func SaveCommandGrant(udb Database, agentID, applianceID string, categories []string) CommandGrant {
	g := CommandGrant{
		AgentID:     normalizeAgentID(agentID),
		ApplianceID: strings.TrimSpace(applianceID),
		Categories:  cleanCategories(categories),
	}
	if udb != nil && g.AgentID != "" && g.ApplianceID != "" {
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
	// A pre-normalization record answers lookups through the legacy fallback,
	// so removing only the modern key would leave the connection alive.
	udb.Unset(commandGrantsTable, legacyCommandGrantKey(agentID, applianceID))
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
			g.AgentID = normalizeAgentID(g.AgentID)
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

// autoRunAllowed answers the one question the exec path asks: may this command's
// risk category run without stopping to ask, for this agent on this appliance?
//
// Returns the reason as well, because "why did that run without asking me" is
// the question anyone reads the log for, and a bare yes cannot answer it. The
// scope names WHICH record decided — the agent on this box, the agent
// anywhere, or the user's own default.
func autoRunAllowed(udb Database, agentID, applianceID string, cat RiskCategory) (bool, GrantScope) {
	set, scope := ResolveCommandGrant(udb, agentID, applianceID)
	return set[cat], scope
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
