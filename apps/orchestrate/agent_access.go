// What an agent can actually do, in one place, for the person who granted it.
//
// The pieces existed and none of them answered the question. The privileges
// card (privilege_card.go) classifies the toolset well, but it is emitted into
// the conversation at the moment an authoring tool changes an agent, so it is a
// receipt printed once rather than a standing answer — and its own header says
// why that matters: "the build happened in one place and its consequences were
// reviewed in another, and the common path was to never review them at all".
// The editor shows the inputs (an allowlist here, a dispatch policy there, a
// credential picker below) and never composes them.
//
// And nothing computed REACH. Every surface describes an agent in isolation,
// while the larger number is who else it can hand work to: an agent held to
// web_search that can dispatch to six others effectively holds what those six
// hold. That is the half this file adds.
//
// THE HONESTY RULE, which this surface lives or dies by. Two registers:
//
//   - EXACT, because the record determines it: the allowlist, the dispatch
//     policy and who it resolves to, the capability flags, the unattended
//     policy per tool.
//   - NAMED BUT NOT ENUMERATED: credential-backed tools, custom tools,
//     app-provided tools and MCP tools are assembled per session, which is why
//     effectiveExtraToolsets is prose rather than a list. A view that presents
//     an incomplete list as complete is worse than one that says so — the same
//     argument that file already makes for the model's own introspection.

package orchestrate

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// agentReachRow is one thing this agent can hand work to.
type agentReachRow struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Adds string `json:"adds"`
}

// dispatchReachable answers whether caller may dispatch to target, by the same
// rules agentsRunGate enforces one refusal at a time.
//
// A MIRROR, and the drift that implies is real: the gate must keep its own
// branches because each one writes a different refusal for the model to act on,
// while a listing needs one predicate. The matrix test beside this is what
// holds them together — if the gate grows a rule, that test is where the
// disagreement shows up.
func dispatchReachable(caller, target AgentRecord) bool {
	if target.ID == caller.ID {
		return false
	}
	if sub := strings.TrimSpace(target.OwnedBy); sub != "" {
		// A sub-agent is private to its owner: it runs with that parent's
		// authority, so reaching it from elsewhere would hand over the parent's.
		return sub == caller.ID
	}
	switch effectiveDispatchMode(caller) {
	case dispatchNone:
		return false
	case dispatchOnly:
		// The explicit pick wins both ways, Hidden included.
		return dispatchListContains(caller, target.ID)
	case dispatchExcept:
		return !dispatchListContains(caller, target.ID) && !target.Hidden
	default: // dispatchAll
		return !target.Hidden
	}
}

// agentReach lists everything this agent can hand work to, annotated with what
// that target adds beyond the caller's own reach.
//
// Recipes are listed only when they REACH AN AGENT. A pipeline of worker stages
// cannot exceed its caller (a stage's tool list is an intersection with the
// inherited catalog, and a dispatched pipeline inherits none), so listing it
// under "what this agent can reach" would pad the answer with rows that grant
// nothing. See dispatch_escalation.go.
func agentReach(udb Database, user string, caller AgentRecord) []agentReachRow {
	var out []agentReachRow
	auth := dispatchAuthority{
		AgentID: caller.ID, AgentName: caller.Name,
		Mode:    effectiveDispatchMode(caller),
		Targets: caller.AllowedDispatchTargets,
	}
	for _, target := range listAgents(udb, user) {
		if !dispatchReachable(caller, target) {
			continue
		}
		adds := dispatchEscalationNote(caller, target)
		if adds == "" {
			adds = "nothing this agent does not already have"
		}
		out = append(out, agentReachRow{Name: target.Name, Kind: "agent", Adds: adds})
	}
	for _, def := range ListPipelineDefs(udb, user) {
		if r := pipelineReach(def); r.escalates() && auth.allowsPipeline(def) {
			out = append(out, agentReachRow{Name: def.Name, Kind: "pipeline", Adds: r.note()})
		}
	}
	for _, def := range ListMachineDefs(udb, user) {
		if r := machineReach(def); r.escalates() && auth.allowsMachine(def) {
			out = append(out, agentReachRow{Name: def.Name, Kind: "machine", Adds: r.note()})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// agentAccessSummary is the one line the owner reads first: how much, before
// how exactly.
func agentAccessSummary(rec AgentRecord, reach []agentReachRow) string {
	var parts []string
	switch {
	case isNoToolsSentinel(rec.AllowedTools):
		parts = append(parts, "No tools of its own")
	case len(rec.AllowedTools) == 0:
		parts = append(parts, "The default tool pool (every read and network tool)")
	default:
		parts = append(parts, fmt.Sprintf("%d tool(s) by allowlist", len(rec.AllowedTools)))
	}
	var caps []string
	if rec.Fleet {
		caps = append(caps, "conductor")
	}
	if agentCanAuthor(rec) {
		caps = append(caps, "authoring")
	}
	if len(caps) > 0 {
		parts = append(parts, "plus the "+strings.Join(caps, " and ")+" toolset(s)")
	}
	if n := len(reach); n == 0 {
		parts = append(parts, "and hands work to nothing")
	} else {
		parts = append(parts, fmt.Sprintf("and can hand work to %d other target(s)", n))
	}
	if agentForcesPrivate(rec) {
		parts = append(parts, "· network OFF (Private)")
	}
	return strings.Join(parts, " ")
}

// accessCaveat is the second register, said once rather than implied by a gap.
const accessCaveat = "Credential-backed, custom, app-provided and MCP tools are assembled per run and are not listed here — this is what the record determines, not a transcript of one turn."

// handleAgentAccess serves the two lists the access sections read.
func (T *OrchestrateApp) handleAgentAccess(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rec, found := findAgentByNameOrID(udb, user, strings.TrimSpace(r.URL.Query().Get("id")))
	if !found {
		http.NotFound(w, r)
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("view")) == "reach" {
		rows := agentReach(udb, user, rec)
		if rows == nil {
			rows = []agentReachRow{}
		}
		writeJSON(w, rows)
		return
	}
	sess := &ToolSession{Username: user, DB: udb}
	rows := []map[string]any{}
	for _, g := range privilegeToolRows(sess, rec, nil) {
		rows = append(rows, map[string]any{
			"name": g.Name, "detail": g.Detail, "policy": privilegePolicyLabel(g.Policy),
		})
	}
	writeJSON(w, rows)
}

// privilegePolicyLabel says what happens on an UNATTENDED run in words, because
// "ask" on a row the owner is reading in a browser invites the reading that
// THEY will be asked. Nobody is asked on a schedule: the call is refused and
// queued.
func privilegePolicyLabel(policy string) string {
	switch policy {
	case "auto":
		return "runs unattended"
	case "allow":
		return "pre-authorized"
	default:
		return "queued for approval"
	}
}

// agentToolsEmptyText is what the tool table says when it has no rows, and it
// is the difference between a truthful view and a dangerous one.
//
// The rows come from the ALLOWLIST, and an empty allowlist means the default
// pool — the widest an agent gets. An empty table under a heading that says
// "what this agent can do" would read as "nothing", which is exactly backwards,
// and is the failure effectiveExtraToolsets warns about in the model's own
// introspection: a surface that under-reports is worse than none.
func agentToolsEmptyText(rec AgentRecord) string {
	if isNoToolsSentinel(rec.AllowedTools) {
		return "No tools at all. This agent answers from what it knows and cannot act."
	}
	if len(rec.AllowedTools) == 0 {
		return "No allowlist, which means the DEFAULT POOL: every read and network tool this deployment has. Narrow it above to change that."
	}
	return "No tools resolved from this agent's allowlist — every name on it matches nothing, so the agent cannot act."
}
