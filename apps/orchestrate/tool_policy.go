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
// BLOCK was left out of that list when this file was written, and the reason
// given was that the runner could not honor it: a segment reading "never" while
// the runner queued the tool for approval anyway would be a lie told by a
// control. That reason is gone. NoUnattendedTools on the agent record is the
// runner's half, autonomousToolAllowed refuses on it before every other clause,
// and the refusal is not queued because the owner is not being asked. So block
// is a real state now, stored the same way allow is: the record here, the list
// on the agent, written together.
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
	Policy  string // PolicyAllow | PolicyAsk | PolicyBlock
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
// reads and the lists are what the runner reads, so a write that updated only
// one would show a state the deployment does not actually have. Anything that
// is neither an explicit allow nor an explicit block is stored as ask, which is
// the state of having decided to be asked rather than of never having decided
// (that one is no record at all, and Remove is how you get back to it).
func setAutoToolPolicy(db Database, udb Database, owner, agentID, tool, policy string) {
	if db == nil || strings.TrimSpace(agentID) == "" || strings.TrimSpace(tool) == "" {
		return
	}
	if policy != PolicyAllow && policy != PolicyBlock {
		policy = PolicyAsk
	}
	db.Set(autoToolPolicyTable, autoToolKey(owner, agentID, tool), policy)
	// The two lists are exclusive by construction. A tool cannot be both
	// pre-authorized to run unwatched and marked never to, and leaving a stale
	// entry in the other list would leave the runner reading a contradiction
	// that this page shows no sign of.
	switch policy {
	case PolicyAllow:
		removeNoUnattendedTool(udb, agentID, tool)
		addAutoApproveTool(udb, agentID, tool)
	case PolicyBlock:
		removeAutoApproveTool(udb, agentID, tool)
		addNoUnattendedTool(udb, agentID, tool)
	default:
		removeAutoApproveTool(udb, agentID, tool)
		removeNoUnattendedTool(udb, agentID, tool)
	}
}

// removeAutoToolPolicy forgets the decision entirely: the record goes, the
// grant goes, and the row leaves the page. Back to never having decided.
func removeAutoToolPolicy(db Database, udb Database, owner, agentID, tool string) {
	if db != nil {
		db.Unset(autoToolPolicyTable, autoToolKey(owner, agentID, tool))
	}
	removeAutoApproveTool(udb, agentID, tool)
	removeNoUnattendedTool(udb, agentID, tool)
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
		if p != PolicyAllow && p != PolicyBlock {
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

// addNoUnattendedTool / removeNoUnattendedTool maintain the runner's half of a
// block, the mirror of the two above. Kept as their own pair rather than a
// parameterized list-editor: the two lists mean opposite things, and a helper
// that takes which-list-to-edit is one argument away from writing a grant where
// a restriction belongs.
func addNoUnattendedTool(udb Database, agentID, tool string) {
	rec, ok := loadAgent(udb, agentID)
	if !ok {
		return
	}
	for _, t := range rec.NoUnattendedTools {
		if t == tool {
			return
		}
	}
	rec.NoUnattendedTools = append(rec.NoUnattendedTools, tool)
	if _, err := saveAgent(udb, rec); err != nil {
		Log("[orchestrate.permissions] marking %s never-unattended on %s: %v", tool, agentID, err)
	}
}

func removeNoUnattendedTool(udb Database, agentID, tool string) {
	rec, ok := loadAgent(udb, agentID)
	if !ok {
		return
	}
	var kept []string
	changed := false
	for _, t := range rec.NoUnattendedTools {
		if t == tool {
			changed = true
			continue
		}
		kept = append(kept, t)
	}
	if changed {
		rec.NoUnattendedTools = kept
		if _, err := saveAgent(udb, rec); err != nil {
			Log("[orchestrate.permissions] clearing the never-unattended mark on %s/%s: %v", agentID, tool, err)
		}
	}
}
