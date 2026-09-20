// What a sub-agent can reach that its parent cannot.
//
// Ownership is a trust delegation: autonomousApprovedSet walks UP the OwnedBy
// chain so a sub-agent inherits every ancestor's approvals, and
// inheritDelegatorGuardrails now carries restrictions DOWN. Neither says
// anything about the TOOL SURFACE, which is additive — InheritParentTools
// unions the parent's inheritable catalog with the child's own AllowedTools,
// and nothing compares the two. A sub-agent can therefore hold a tool its
// parent has never been granted, authored by a Builder that was never asked to
// check.
//
// This REPORTS that, and does not enforce it. Capping would change what
// existing sub-agents can do, and an agent that has been working for months is
// not something to break on a rule written today — the owner decides, the same
// way the memory audit points and never deletes. Enforcement, if it comes, is a
// second step taken on evidence this produces.
//
// Record-level on purpose: no turn, no session, no catalog build. It runs
// wherever an owner is looking at agents, which is not inside a run.
package orchestrate

import (
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// SubAgentToolException is one sub-agent reaching past its parent.
type SubAgentToolException struct {
	ChildID    string `json:"child_id"`
	ChildName  string `json:"child_name"`
	ParentID   string `json:"parent_id"`
	ParentName string `json:"parent_name"`
	// Tools the child can reach and the parent cannot, sorted.
	Tools []string `json:"tools"`
	// Capabilities the child holds and the parent does not, as owner-facing
	// names ("Conductor tools", "Authoring").
	//
	// A FLAG, not a tool name, and the report is incomplete without it. Fleet
	// and Author each mint a whole catalog at dispatch time — delegate,
	// message_contact, notify_owner, standing-agent and monitor management — and
	// enforceSubAgentPosture pins Hidden, Exposed, PublicName, AllowExplorer
	// and IntakeForm on a sub-agent while leaving both of these alone.
	//
	// Which makes this the case that matters most. tool_inheritance.go goes to
	// deliberate trouble to inherit only the parent's NON-consequential slice —
	// "can OBSERVE but not act on the owner's behalf, no texting people, no
	// running the fleet" — and a child carrying the flag itself walks straight
	// past that care. A report comparing only allowlists would hand the most
	// consequential escalation a clean bill.
	Capabilities []string `json:"capabilities,omitempty"`
	// Unrestricted is the loudest shape and needs its own field: the child has
	// NO allowlist — which means the whole default pool — while the parent is
	// curated. Listing every tool in the pool as an exception would bury the
	// one fact that matters, which is that the child was never narrowed.
	Unrestricted bool `json:"unrestricted"`
}

// effectiveToolNames is what an agent can reach, as names, ignoring the
// always-on set every agent gets regardless.
//
// Returns nil,false when the agent is unrestricted (no allowlist): the caller
// has to treat that as "everything", and a nil slice would read as "nothing".
func effectiveToolNames(a AgentRecord) (map[string]bool, bool) {
	if isNoToolsSentinel(a.AllowedTools) {
		return map[string]bool{}, true
	}
	if len(a.AllowedTools) == 0 {
		return nil, false
	}
	out := make(map[string]bool, len(a.AllowedTools))
	for _, n := range a.AllowedTools {
		if n = strings.TrimSpace(n); n != "" {
			out[n] = true
		}
	}
	return out, true
}

// alwaysOnTool reports the tools every agent gets whatever its allowlist says,
// so they are never reported as reaching past anything.
func alwaysOnTool(name string) bool {
	if name == "workspace" {
		return true
	}
	for _, n := range frameworkUtilityTools {
		if n == name {
			return true
		}
	}
	return false
}

// SubAgentToolExceptions lists every owned agent holding tools its parent
// cannot reach. Empty when every sub-agent sits inside its parent's surface.
func SubAgentToolExceptions(udb Database, user string) []SubAgentToolException {
	if udb == nil || user == "" {
		return nil
	}
	all := listAgents(udb, user)
	byID := make(map[string]AgentRecord, len(all))
	for _, a := range all {
		byID[a.ID] = a
	}
	var out []SubAgentToolException
	for _, child := range all {
		parentID := strings.TrimSpace(child.OwnedBy)
		if parentID == "" {
			continue
		}
		ex := SubAgentToolException{
			ChildID: child.ID, ChildName: chFirst(child.Name, child.ID), ParentID: parentID,
		}
		// A dangling OwnedBy cannot arrive here: listAgents promotes an orphan
		// to top-level and persists that, precisely because a sub-agent is
		// pinned Hidden and an orphan would be invisible. Skipped rather than
		// reported, so this does not grow a branch for a state the loader has
		// already healed.
		parent, ok := byID[parentID]
		if !ok {
			continue
		}
		ex.ParentName = chFirst(parent.Name, parent.ID)

		// Flags first: they are independent of the allowlist comparison below,
		// and a child can exceed its parent by capability while sitting
		// comfortably inside its tool list.
		if child.Fleet && !parent.Fleet {
			ex.Capabilities = append(ex.Capabilities, "Conductor tools (delegate, scheduling, monitors)")
		}
		if agentCanAuthor(child) && !agentCanAuthor(parent) {
			ex.Capabilities = append(ex.Capabilities, "Authoring (build agents, tools, apps)")
		}

		childNames, childBounded := effectiveToolNames(child)
		parentNames, parentBounded := effectiveToolNames(parent)

		// An unbounded parent reaches the whole POOL, so nothing the child
		// names can be beyond it — but a pool is tools, not capabilities. Fleet
		// and Author are flags on the record, and an agent without them does
		// not hold them however wide its allowlist is.
		if !parentBounded {
			if len(ex.Capabilities) > 0 {
				out = append(out, ex)
			}
			continue
		}
		if !childBounded {
			ex.Unrestricted = true
			out = append(out, ex)
			continue
		}
		for n := range childNames {
			if !parentNames[n] && !alwaysOnTool(n) {
				ex.Tools = append(ex.Tools, n)
			}
		}
		if len(ex.Tools) > 0 || len(ex.Capabilities) > 0 {
			sort.Strings(ex.Tools)
			out = append(out, ex)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChildName < out[j].ChildName })
	return out
}
