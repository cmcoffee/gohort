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
	"github.com/cmcoffee/gohort/core/sandbox"
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
	// The agent being CALLED gets a say. Asked before the caller's policy
	// because it cannot be widened by it: an agent that accepts nothing is
	// unreachable however open the caller is.
	if !inboundAllows(target, caller) {
		return false
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
		parts = append(parts, "- network OFF (Private)")
	}
	return strings.Join(parts, " ")
}

// accessCaveat is the second register, said once rather than implied by a gap.
const accessCaveat = "The list is resolved through the same call a real turn makes, so your own tools, credential-backed ones and the tools an installed app contributes are all in it, each shown by what it reaches. A run can still add MCP tools that only exist once a session is open."

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
	switch strings.TrimSpace(r.URL.Query().Get("view")) {
	case "reach":
		rows := agentReach(udb, user, rec)
		if rows == nil {
			rows = []agentReachRow{}
		}
		writeJSON(w, rows)
		return
	case "workspace":
		writeJSON(w, T.accessWorkspaceRows(rec))
		return
	case "knowledge":
		writeJSON(w, accessKnowledgeRows(rec))
		return
	case "subagents":
		writeJSON(w, agentSubAgents(udb, user, rec))
		return
	}
	// The tools this agent's worker would actually be handed, resolved through
	// the same call the runner makes.
	//
	// This used to list from privilegeToolRows, which reads rec.AllowedTools.
	// That field is EMPTY on a default-pool agent, where empty means "every
	// catalog tool", so the one page built to answer "what can this thing do"
	// showed nothing at all for the commonest kind of agent.
	//
	// A resolution failure says so instead of rendering an empty list. "No
	// tools" and "could not work out the tools" are opposite facts and this
	// page is only worth having if it never confuses them.
	rows, err := T.resolvedAgentTools(r.Context(), udb, user, rec)
	if err != nil {
		writeJSON(w, map[string]any{
			"error": "This agent's toolset could not be resolved, so nothing below is the full picture: " + err.Error(),
		})
		return
	}
	writeJSON(w, rows)
}

// accessSideRow is a row in the two groups that are not a tool list: a fact,
// a short verdict, and where the verdict is set.
type accessSideRow struct {
	Name   string `json:"name"`
	Policy string `json:"policy"`
	Detail string `json:"detail,omitempty"`
	Where  string `json:"where,omitempty"`
}

// accessWorkspaceRows answers for the SANDBOX, which is not a tool and is
// shared by every tool that runs a command.
//
// Three questions an owner actually has, in the order they think of them: can
// it run things, can it change files, can it reach out. Each row says where
// the answer is set, because a reader who disagrees with one wants to go and
// change it rather than hunt for which of six surfaces owns it.
func (T *OrchestrateApp) accessWorkspaceRows(rec AgentRecord) []accessSideRow {
	off := map[string]bool{}
	for _, pair := range rec.DisabledToolActions {
		off[strings.TrimSpace(pair)] = true
	}
	shell := accessSideRow{Name: "Runs commands", Policy: "yes", Where: "Agent → Switched-off sub-actions"}
	if off["workspace/run"] {
		shell.Policy, shell.Detail = "no", "its file actions still work"
	}
	files := accessSideRow{Name: "Writes files", Policy: "yes", Where: "Agent → Switched-off sub-actions"}
	if off["workspace/write"] {
		files.Policy, files.Detail = "no", "it can still read what is there"
	}
	net := accessSideRow{Name: "Opens network connections", Where: "Agent → Workspace may not reach the network"}
	switch {
	case rec.ForcePrivate:
		net.Policy, net.Detail = "no", "forced private, which cuts all network, not only the workspace's"
		net.Where = "Agent → Force Private mode"
	case rec.WorkspaceNoNetwork:
		net.Policy, net.Detail = "no", "its tools and its model are unaffected"
	case sandbox.ShellNetworkClosedByDefault():
		net.Policy, net.Detail = "when declared", "this deployment requires raw_network on the tool record"
		net.Where = "Admin → Tunables → Security"
	default:
		net.Policy, net.Detail = "yes", "code in the workspace shares the host's network"
	}
	return []accessSideRow{shell, files, net}
}

// accessKnowledgeRows answers what it reads before it answers.
func accessKnowledgeRows(rec AgentRecord) []accessSideRow {
	rows := []accessSideRow{}
	if n := len(rec.AttachedCollections); n > 0 {
		rows = append(rows, accessSideRow{
			Name:   fmt.Sprintf("%d collection%s", n, plural(n)),
			Policy: "attached", Detail: strings.Join(rec.AttachedCollections, ", ") +
				" - searched every turn, and carried to anybody it is shared with",
			Where: "Agent → Knowledge",
		})
	}
	if n := len(rec.AllowedSkills); n > 0 {
		rows = append(rows, accessSideRow{
			Name: fmt.Sprintf("%d skill%s", n, plural(n)), Policy: "attached",
			Detail: strings.Join(rec.AllowedSkills, ", "), Where: "Agent → Skills",
		})
	}
	rows = append(rows,
		accessSideRow{Name: "Saved notes", Policy: accessOnOff(!rec.DisableExplicit),
			Detail: "facts it keeps in every prompt", Where: "Agent → Memory"},
		accessSideRow{Name: "Inferred memory", Policy: accessOnOff(!rec.DisableInferred),
			Detail: "what it works out for itself and searches later", Where: "Agent → Memory"},
	)
	return rows
}

func accessOnOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
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
	return "No tools resolved from this agent's allowlist: every name on it matches nothing, so the agent cannot act."
}

// Inbound reach: who may dispatch TO an agent, decided by the agent being
// called.
//
// Every other reachability rule is expressed from the CALLER's side, so "only
// these two may call me" could previously only be arranged by visiting every
// other agent in the fleet and excluding this one. Nobody does that, and
// nothing checks it held.
const (
	inboundAny  = ""     // any agent, subject to Hidden and the caller's policy
	inboundOnly = "only" // only AllowedCallers
	inboundNone = "none" // nothing reaches it
)

// inboundAllows reports whether target accepts a dispatch from caller.
//
// Asked AFTER the caller's own policy, never instead of it: the two narrow
// together and neither widens the other. A caller allowed to reach everything
// still cannot reach an agent that accepts nothing.
//
// A sub-agent's parent is exempt. Ownership IS the link - a sub-agent runs
// with its parent's authority and exists to be called by it - and an inbound
// rule that locked a parent out of its own child would strand the child with
// no way to be reached at all.
func inboundAllows(target, caller AgentRecord) bool {
	if strings.TrimSpace(target.OwnedBy) != "" && target.OwnedBy == caller.ID {
		return true
	}
	// The owner's default reaches an agent that has not answered, the same way
	// every other setting does.
	switch resolveSetting(RootDB, agentDefaultsOwner(target, ""), target, defaultInboundMode) {
	case inboundNone:
		return false
	case inboundOnly:
		return dispatchListContainsID(target.AllowedCallers, caller.ID)
	}
	return true
}

// dispatchListContainsID is the plain membership test, separate from
// dispatchListContains because that one reads a caller's OWN list off its
// record and this asks about a list belonging to somebody else.
func dispatchListContainsID(list []string, id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for _, v := range list {
		if strings.TrimSpace(v) == id {
			return true
		}
	}
	return false
}

// inboundRefusal is what the model is told, in the words that let it stop
// rather than retry. It names the agent that refused and who has to change it,
// because a refusal the caller could act on is one it will try to route round.
func inboundRefusal(target AgentRecord) string {
	name := agentName(target)
	if strings.TrimSpace(target.InboundMode) == inboundNone {
		return "agents(run): " + name + " accepts no dispatches from any agent (Security -> Delegation on " + name +
			"). This is set on the agent being called, so nothing on this side changes it: do the work yourself, or ask the user."
	}
	return "agents(run): " + name + " only accepts dispatches from agents on its own caller list, and this one is not on it" +
		" (Security -> Delegation on " + name + "). Do not retry; only the user can add it."
}
