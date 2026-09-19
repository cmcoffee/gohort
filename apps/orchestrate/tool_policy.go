package orchestrate

// A standing decision about one tool, for one agent.
//
// The grant itself is AutoApproveTools on the agent record, and that is what
// the unattended runner reads — this does not replace it and must not, or a
// tool would run on the strength of a record the runner never consults.
//
// What this adds is the OTHER answer. AutoApproveTools is a list, so it can
// only say yes: "needs approval" is the absence of an entry, which is
// indistinguishable from never having decided. The Permissions page offered a
// control with a "Needs approval" segment anyway; choosing it removed the entry
// and the row vanished, because the row existed only while the entry did. A
// control must not offer a state its row cannot hold.
//
// So a decision gets a record: allow (and the grant is in the list), or ask
// (and it is not). Either way the row stays and the segment shows which. Remove
// deletes the record and the row goes, which is what Remove means everywhere
// else on that page.
//
// In this app rather than core: AutoApproveTools lives on AgentRecord here, the
// page is here, and core is at its file ceiling exactly (see
// TestCoreStaysUnderItsCeiling) — a file there would be a change to the shape
// of the hub for a record one page reads.

import (
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// autoToolPolicyTable stores one decision per (owner, agent, tool). Deployment
// -wide and owner-prefixed, matching the delegation and contact policy tables
// it sits beside conceptually.
const autoToolPolicyTable = "auto_tool_policy"

// autoToolPolicy is one row's worth.
type autoToolPolicy struct {
	AgentID string
	Tool    string
	Policy  string // PolicyAllow | PolicyAsk
}

// autoToolKey is the storage key. Tool names can contain almost anything, so
// the agent id and the tool are joined with a separator that an id cannot
// contain and the owner leads, matching preauthKey's shape.
func autoToolKey(owner, agentID, tool string) string {
	return owner + ":" + agentID + "\x00" + tool
}

// setAutoToolPolicy records the decision AND makes the grant match it.
//
// Both, always, and in that order of importance: the record is what the page
// reads and the list is what the runner reads, so a write that updated only one
// would show a state the deployment does not actually have. Anything that is
// not an explicit allow is stored as ask — there is no block for a tool, and
// inventing one here would mean a segment that reads as "never" while the
// runner happily queues it for approval.
func setAutoToolPolicy(db Database, udb Database, owner, agentID, tool, policy string) {
	if db == nil || strings.TrimSpace(agentID) == "" || strings.TrimSpace(tool) == "" {
		return
	}
	if policy != PolicyAllow {
		policy = PolicyAsk
	}
	db.Set(autoToolPolicyTable, autoToolKey(owner, agentID, tool), policy)
	if policy == PolicyAllow {
		addAutoApproveTool(udb, agentID, tool)
		return
	}
	removeAutoApproveTool(udb, agentID, tool)
}

// removeAutoToolPolicy forgets the decision entirely: the record goes, the
// grant goes, and the row leaves the page. Back to never having decided.
func removeAutoToolPolicy(db Database, udb Database, owner, agentID, tool string) {
	if db != nil {
		db.Unset(autoToolPolicyTable, autoToolKey(owner, agentID, tool))
	}
	removeAutoApproveTool(udb, agentID, tool)
}

// listAutoToolPolicies returns every recorded decision for this owner.
func listAutoToolPolicies(db Database, owner string) []autoToolPolicy {
	if db == nil || owner == "" {
		return nil
	}
	prefix := owner + ":"
	var out []autoToolPolicy
	for _, k := range db.Keys(autoToolPolicyTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		agentID, tool, ok := strings.Cut(k[len(prefix):], "\x00")
		if !ok || tool == "" {
			continue
		}
		var p string
		if !db.Get(autoToolPolicyTable, k, &p) || p == "" {
			continue
		}
		if p != PolicyAllow {
			p = PolicyAsk
		}
		out = append(out, autoToolPolicy{AgentID: agentID, Tool: tool, Policy: p})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AgentID != out[j].AgentID {
			return out[i].AgentID < out[j].AgentID
		}
		return out[i].Tool < out[j].Tool
	})
	return out
}

// addAutoApproveTool grants a tool, if it is not granted already. The mirror of
// removeAutoApproveTool, which existed on its own because until now the only
// way out of this list was out.
func addAutoApproveTool(udb Database, agentID, tool string) {
	rec, ok := loadAgent(udb, agentID)
	if !ok {
		return
	}
	for _, t := range rec.AutoApproveTools {
		if t == tool {
			return
		}
	}
	rec.AutoApproveTools = append(rec.AutoApproveTools, tool)
	if _, err := saveAgent(udb, rec); err != nil {
		Log("[orchestrate.permissions] granting %s to %s: %v", tool, agentID, err)
	}
}
