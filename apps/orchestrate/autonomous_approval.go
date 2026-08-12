// Autonomous-run tool approval — the one policy that unattended fires (standing
// agents AND recurring scheduled-updates) share for high-consequence (NeedsConfirm)
// tools, replacing the two contradictory behaviors those surfaces used to have:
// standing_runner DENIED every such tool (deny-by-default), while scheduled_updates
// AUTO-APPROVED every such tool (silently bypassing the "Require confirm" contract).
//
// The reconciled policy: a NeedsConfirm tool runs on an autonomous fire ONLY if the
// agent has it in AutoApproveTools (the owner pre-authorized it). Otherwise it's
// refused for THIS fire and queued as an "autonomous_tool" authorization — it shows
// in the Authorizations pane and surfaces to the agent's cortex. Approving it adds
// the tool to AutoApproveTools (console.go handleApprove), so the NEXT fire runs it.
// No human present ≠ silent success or silent failure; it becomes a pending, visible
// grant the owner acts on once.
package orchestrate

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// channelSenderAuthorized reports whether agentID may deliver to a channel WITHOUT
// the proactive-send approval queue because of an explicit grant it holds or
// INHERITS — either it (or an ancestor up the OwnedBy chain) is in the channel's
// AuthorizedSenders, or an ANCESTOR is the channel's bound agent (a sub-agent
// inheriting its parent's channel). It deliberately does NOT fire merely because
// the agent is ITSELF the channel's bound agent: that agent's own proactive sends
// follow the normal approval rules (reply-in-thread / pre-authorized / queue), so
// this bypass never loosens an existing agent's behavior — it only adds the
// sub-agent grant path.
func channelSenderAuthorized(udb Database, owner, chatID, handle, agentID string) bool {
	seen := map[string]bool{}
	first := true
	for id := agentID; id != "" && !seen[id]; {
		seen[id] = true
		for _, ch := range ListChannels(RootDB, owner) {
			if ch.Address != "" && ch.Address != chatID && ch.Address != handle {
				continue // a per-conversation channel that isn't this conversation
			}
			for _, s := range ch.AuthorizedSenders {
				if s == id {
					return true // explicit grant on this agent or an ancestor
				}
			}
			if !first && ch.AgentID == id {
				return true // an ANCESTOR owns the channel → the sub-agent inherits it
			}
		}
		first = false
		rec, ok := loadAgent(udb, id)
		if !ok {
			break
		}
		id = rec.OwnedBy
	}
	return false
}

// autonomousGate is the shared confirm backend for one unattended run. It records
// which NeedsConfirm tools it refused (queued) so the caller can flag the run as
// needing attention.
type autonomousGate struct {
	app      *OrchestrateApp
	owner    string
	agentID  string
	subAgent bool // OwnedBy set → runs under the parent's authority
	auto     map[string]bool
	sess     *ToolSession // resolves a tool call to the credential it dispatches through
	queued   []string     // tool names refused + queued this run (for the caller's attention line)
}

// newAutonomousGate builds the gate for an agent's autonomous run, snapshotting
// its pre-authorized tool set INHERITED down the ownership chain, and whether it's
// a sub-agent (which runs under its parent's authority).
//
// sess is the run's tool session, used to resolve a tool call to the credential
// it dispatches through. It may be nil (a standing fire creates its session
// inside the dispatch); alwaysConfirms then resolves the credential from the
// owner's stored kit instead, so both unattended surfaces apply one policy.
func (app *OrchestrateApp) newAutonomousGate(owner, agentID string, sess *ToolSession) *autonomousGate {
	udb := UserDB(app.DB, owner)
	sub := false
	if rec, ok := loadAgent(udb, agentID); ok {
		sub = strings.TrimSpace(rec.OwnedBy) != ""
	}
	return &autonomousGate{
		app: app, owner: owner, agentID: agentID, subAgent: sub,
		auto: autonomousApprovedSet(udb, agentID),
		sess: sess,
	}
}

// alwaysConfirms reports whether a tool is CONFIGURED to ask before every call —
// the owner-facing "Require confirm before each call" toggle on the credential it
// dispatches through. This is the only thing that should stop an unattended run.
//
// The distinction matters because NeedsConfirm answers a different question. It
// is a heuristic tier ("this reaches outside the sandbox", "this is a raw
// endpoint"), and the gate used to refuse on it — so a tool the owner had
// attached to the agent, and which ran without a murmur in chat, was refused the
// moment the same agent ran on a timer. Nothing about the tool changed; only
// whether a human happened to be watching. tempToolNeedsConfirm already
// documents this split for the credential case ("tools enabled on the agent but
// the scheduler can't call them"); this closes it for the rest.
//
// Attaching a tool to an agent IS the authorization — the same reasoning the
// sub-agent bypass below already uses for a parent's toolset. What stays gated
// is what the owner explicitly marked as gated, and outward-facing actions that
// carry their own approval queue (a proactive send is still queued at the
// channel layer, whatever this returns).
func (g *autonomousGate) alwaysConfirms(name string) bool {
	return toolAlwaysConfirms(UserDB(g.app.DB, g.owner), g.owner, g.sess, name)
}

// toolAlwaysConfirms is alwaysConfirms without a gate to hang it on, so the
// surfaces that PREDICT the gate (the schedule pre-flight, the authoring
// privileges card) can ask the same question the gate asks at fire time. Every
// one of them used to answer it their own way and drifted; see
// autonomousToolAllowed for why that drift is expensive.
func toolAlwaysConfirms(udb Database, owner string, sess *ToolSession, name string) bool {
	cred := credentialForToolCall(sess, name)
	if cred == "" {
		// A standing fire builds its session INSIDE the dispatch, so the gate is
		// constructed without one. The name→credential mapping doesn't need a
		// session though — it's on the stored tool. Resolving it here is what
		// keeps the two unattended surfaces on the same policy instead of the
		// session-less one quietly allowing everything.
		if p, ok := UserToolByName(udb, owner, name); ok {
			cred = strings.TrimSpace(p.Tool.Credential)
		}
	}
	return credentialAlwaysConfirms(owner, cred)
}

// credentialAlwaysConfirms is the innermost question — does THIS credential ask
// before every call — split out for callers holding the credential already (the
// privileges card classifies a tool the authoring call just committed, which is
// not yet resolvable by name through either the session or the store).
func credentialAlwaysConfirms(owner, cred string) bool {
	cred = strings.TrimSpace(cred)
	if cred == "" {
		return false // no credential in play → nothing configured to ask
	}
	// Resolve, not Load: a credential the user OWNS (@u:<owner>:<name>) is absent
	// from the global namespace Load reads, so a Load-only lookup missed it and
	// fell to the fail-closed branch below — queueing an approval on every fire
	// for a tool that dispatches perfectly well in chat, where dispatch resolves
	// the same name user-aware. The lookup has to match the one dispatch does.
	c, ok := Secure().Resolve(cred, owner)
	if !ok {
		return true // named a credential we can't resolve — fail closed
	}
	return c.RequiresConfirm
}

// autonomousToolAllowed is THE rule for whether a tool may run on an unattended
// fire. Every surface that decides or predicts that answer calls this — the gate
// itself (via allows), the schedule pre-flight, the authoring privileges card —
// because when they each re-implemented it they drifted, and the drift is
// invisible until a user is told their tool will be refused and it isn't (or
// worse, the reverse).
//
// The three clauses, in the order they stop mattering: a SUB-AGENT runs under
// its parent's authority; a PRE-AUTHORIZED tool was granted by the owner; and
// everything else runs unless the credential it dispatches through is configured
// to ask before each call. Attaching a tool to an agent IS the authorization.
func autonomousToolAllowed(subAgent bool, approved map[string]bool, name string, alwaysConfirms func(string) bool) bool {
	if subAgent || approved[name] {
		return true
	}
	return !alwaysConfirms(name)
}

// allows applies the rule to this gate's agent, with no side effects — the
// predicting surfaces need the verdict without queueing anything.
func (g *autonomousGate) allows(name string) bool {
	return autonomousToolAllowed(g.subAgent, g.auto, name, g.alwaysConfirms)
}

// autonomousApprovedSet is the union of an agent's AutoApproveTools and every
// ancestor's up the OwnedBy chain — a sub-agent INHERITS its parent's autonomous
// authorizations, because ownership is trust delegation (the parent created it and
// dispatches to it). This is APPROVAL inheritance, distinct from the opt-in
// InheritParentTools flag (which resolves the parent's non-consequential TOOLSET);
// here the tools already resolve, we're just not re-asking the owner to approve for
// the child what they approved for the parent. The seen-guard stops a cycle.
func autonomousApprovedSet(udb Database, agentID string) map[string]bool {
	set := map[string]bool{}
	seen := map[string]bool{}
	for id := agentID; id != "" && !seen[id]; {
		seen[id] = true
		rec, ok := loadAgent(udb, id)
		if !ok {
			break
		}
		for _, t := range rec.AutoApproveTools {
			set[t] = true
		}
		id = rec.OwnedBy // walk up to the parent
	}
	return set
}

// confirm is the ConfirmFunc the agent loop calls for a NeedsConfirm tool. Returns
// true when the tool may run unattended, else queues an approval and denies this
// call.
//
// A SUB-AGENT runs under its PARENT's authority: the parent built it for a task and
// chose its toolset, so its tools need no separate per-sub-agent approval — the
// parent's act of creating it IS the authorization. Reaching BEYOND the parent (a
// channel / recipient the parent isn't authorized for) is still flagged, but at the
// send/channel layer (channelSenderAuthorized queues it), not here. A TOP-LEVEL
// agent has no parent to vouch for it, so the owner is its direct authority — but
// attaching the tool is how that authority is exercised, so what stays gated is
// what the owner explicitly marked as gated. The rule itself is
// autonomousToolAllowed; this is the half that has a queue to write to.
func (g *autonomousGate) confirm(name, args string) bool {
	if g.allows(name) {
		return true
	}
	g.queue(name, args)
	return false
}

// blocked reports whether any tool was refused this run (for the RunAttention line).
func (g *autonomousGate) blocked() string {
	if len(g.queued) == 0 {
		return ""
	}
	return g.queued[0]
}

// queue records a pending autonomous-tool authorization (deduped) and surfaces it
// to the agent's cortex.
func (g *autonomousGate) queue(name, args string) {
	g.queued = append(g.queued, name)
	for _, ex := range ListAuthorizations(RootDB, g.owner) {
		if ex.Action == "autonomous_tool" && ex.Agent == g.agentID && ex.Brief == name {
			return // already pending — don't stack duplicates
		}
	}
	SaveAuthorization(RootDB, Authorization{
		Owner:  g.owner,
		Action: "autonomous_tool",
		Agent:  g.agentID,
		Brief:  name,
		Text:   truncateObs(args, 200),
	})
	// Best-effort awareness card (no-op if the agent has cortex off; the
	// Authorizations pane is the primary surface either way).
	appendCortexObs(UserDB(g.app.DB, g.owner), g.agentID, "Approval needed",
		cortexKindOverflow, "Wanted to use \""+name+"\" on a scheduled run but it isn't pre-authorized. Approve it in the Authorizations pane to allow it on future runs.")
}
