// Autonomous-run tool policy — what an agent may do when nobody is watching.
//
// One rule, shared by both unattended surfaces (standing agents and recurring
// scheduled updates), because they used to do opposite things: standing DENIED
// every NeedsConfirm tool while recurring AUTO-APPROVED every one of them.
//
// The rule is autonomousToolAllowed, and its default is ALLOW: a tool attached
// to an agent is a tool that agent may use, on a timer exactly as in chat. Two
// things stop one. The owner may mark a tool NEVER UNATTENDED, which is refused
// outright and is not a request for permission — they already answered. Or the
// credential it dispatches through may be set to ask before every call, which
// IS a request: it is refused for this fire and queued as an "autonomous_tool"
// authorization, visible in the Permissions pane and on the agent's cortex, and
// approving it records a standing grant so the next fire runs.
//
// No human present is neither silent success nor silent failure.
package orchestrate

import (
	"context"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// unattendedKey marks a context as a run nobody is watching: a standing or
// recurring fire, a channel turn or monitor wake, a poll check. Everything
// started under it inherits the mark, which is the point. A pipeline or a
// machine step dispatched from inside a 3am fire is as unwatched as the fire,
// and used to approve every tool call because the dispatch it went through
// was written for a person clicking Run.
type unattendedKey struct{}

// withUnattended marks ctx as unattended.
func withUnattended(ctx context.Context) context.Context {
	return context.WithValue(ctx, unattendedKey{}, true)
}

// isUnattended reports whether ctx is, or was started by, a run nobody is
// watching.
func isUnattended(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	on, _ := ctx.Value(unattendedKey{}).(bool)
	return on
}

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
	app     *OrchestrateApp
	owner   string
	agentID string
	auto    map[string]bool
	// noUnattended is the owner's explicit "not without somebody watching",
	// inherited DOWN the ownership chain (the mirror of auto, which inherits up
	// as trust: a restriction has to travel the other way or it is optional).
	noUnattended map[string]bool
	sess         *ToolSession // resolves a tool call to the credential it dispatches through
	queued       []string     // tool names refused + queued this run (for the caller's attention line)
	// withheld is the other refusal kind, kept apart because it means something
	// different to the owner: nothing is waiting on them, they already decided.
	withheld []string
	// why is what the model is told about the last refusal (refusal).
	why string
}

// newAutonomousGate builds the gate for an agent's autonomous run, snapshotting
// its pre-authorized tool set INHERITED down the ownership chain.
//
// sess is the run's tool session, used to resolve a tool call to the credential
// it dispatches through. It may be nil (a standing fire creates its session
// inside the dispatch); alwaysConfirms then resolves the credential from the
// owner's stored kit instead, so both unattended surfaces apply one policy.
func (app *OrchestrateApp) newAutonomousGate(owner, agentID string, sess *ToolSession) *autonomousGate {
	udb := UserDB(app.DB, owner)
	noUnattended := autonomousNoUnattendedSet(udb, agentID)
	// An agent someone SHARED with this runner lives in its owner's store, not
	// the runner's, so its "never unattended" marks were read from a store that
	// does not hold it and came back empty. They are the owner's restriction
	// and travel with the agent; the runner's own marks still add to them.
	for _, a := range SharedAgentsFor(app.DB, owner) {
		if a.ID == agentID && strings.TrimSpace(a.Owner) != "" && a.Owner != owner {
			for t := range autonomousNoUnattendedSet(UserDB(app.DB, a.Owner), agentID) {
				noUnattended[t] = true
			}
			break
		}
	}
	// The owner's per-tool mark, which says it for every agent at once and is
	// the only way to say it for a tool no agent record lists.
	for _, p := range LoadPersistentTempTools(app.DB, owner) { // resolves to the tool store itself
		if p.Tool.NoUnattended {
			noUnattended[p.Tool.Name] = true
		}
	}
	return &autonomousGate{
		app: app, owner: owner, agentID: agentID,
		auto:         autonomousApprovedSet(udb, agentID),
		noUnattended: noUnattended,
		sess:         sess,
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
// Attaching a tool to an agent IS the authorization. What stays gated
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
	creds := credentialsForToolCall(sess, name)
	if len(creds) == 0 {
		// A standing fire builds its session INSIDE the dispatch, so the gate is
		// constructed without one. The name→credential mapping doesn't need a
		// session though — it's on the stored tool. Resolving it here is what
		// keeps the two unattended surfaces on the same policy instead of the
		// session-less one quietly allowing everything.
		if p, ok := UserToolByName(udb, owner, name); ok {
			creds = toolCredentials(p.Tool)
		}
	}
	for _, cred := range creds {
		if credentialAlwaysConfirms(owner, cred) {
			return true
		}
	}
	return false
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
// The clauses, in the order they stop mattering: a tool the owner marked NEVER
// UNATTENDED is refused outright; a PRE-AUTHORIZED tool was granted by the
// owner, on this agent or on any agent above it; and everything else runs
// unless the credential it dispatches through is configured to ask before each
// call. Attaching a tool to an agent IS the authorization.
//
// A sub-agent has no clause of its own. It used to pass every check below the
// mark on the grounds that it ran under its parent's authority, but a model
// can create a sub-agent, so that was a way to run a credential that asks
// before every call with nobody asked. Its parent's authority is what the
// approved set already carries: a grant on the parent covers the child.
//
// The default direction is what makes the first clause worth having. When a
// rule allows almost everything, the useful thing to write down is the
// exception, and the exception people actually have is "not unless somebody is
// watching who can pull the plug". Before this, the only way to say that was to
// set Require-confirm on the CREDENTIAL: wrong scope (every agent and every user
// of that credential), and unavailable at all to a tool that dispatches through
// no credential, which is most of the ones worth worrying about.
func autonomousToolAllowed(approved, noUnattended map[string]bool, name string, alwaysConfirms func(string) bool) bool {
	// The owner's own word, and it beats everything below it including a
	// grant. A restriction a parent carries has to reach its children, or
	// building a sub-agent is how you launder it: the same direction
	// guardrails already inherit.
	if noUnattended[name] {
		return false
	}
	if approved[name] {
		return true
	}
	return !alwaysConfirms(name)
}

// allows applies the rule to this gate's agent, with no side effects — the
// predicting surfaces need the verdict without queueing anything.
func (g *autonomousGate) allows(name string) bool {
	return autonomousToolAllowed(g.auto, g.noUnattended, name, g.alwaysConfirms)
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

// autonomousNoUnattendedSet is the union of an agent's NoUnattendedTools and
// every ancestor's, walking the SAME OwnedBy chain autonomousApprovedSet walks
// and for the opposite reason.
//
// Approval inherits UP as trust: the parent vouched for the child, so what the
// owner allowed the parent the child may do. A restriction has to inherit the
// same way or it is advisory: an owner who says "this agent never sends mail
// unwatched" and then watches it build a sub-agent that does has been told a
// policy was applied that was not. Same direction guardrails already travel.
func autonomousNoUnattendedSet(udb Database, agentID string) map[string]bool {
	set := map[string]bool{}
	seen := map[string]bool{}
	for id := agentID; id != "" && !seen[id]; {
		seen[id] = true
		rec, ok := loadAgent(udb, id)
		if !ok {
			break
		}
		for _, t := range rec.NoUnattendedTools {
			if t = strings.TrimSpace(t); t != "" {
				set[t] = true
			}
		}
		id = rec.OwnedBy
	}
	return set
}

// confirm is the ConfirmFunc the agent loop calls for a NeedsConfirm tool, or
// one the turn asks first about. Returns true when the tool may run unattended,
// else queues an approval and denies this call.
//
// Attaching a tool is how the owner's authority is exercised, so what stays
// gated is what the owner explicitly marked as gated, for a sub-agent as for
// any other: what its parent was granted, it is granted too. The rule itself
// is autonomousToolAllowed; this is the half that has a queue to write to.
func (g *autonomousGate) confirm(name, args string) bool {
	// Approved on a kept card and made by the turn the answer woke, which is
	// a background run (approval_resume.go).
	if takeResumePass(g.owner, g.agentID, name, args) {
		return true
	}
	if g.allows(name) && !g.writeWaitsForOwner(name, args) && !g.ownerAsksFirst(name) {
		return true
	}
	// Marked never-unattended: refuse, and do NOT queue. A queue asks the owner
	// to decide something they have already decided, and approving it would
	// write a grant that contradicts the mark. The breadcrumb is the withheld
	// list, which the run's attention line reports in its own words.
	if g.noUnattended[name] {
		g.withheld = append(g.withheld, name)
		g.why = "NOT RUN: the owner has marked " + name + " never to run without somebody watching, and nobody is watching this run. " +
			"Do not try to get the same result another way: finish without it, and say plainly that " + name + " needs doing while the owner is there."
		Log("[orchestrate/autonomous] agent=%s withheld %q: marked never unattended", g.agentID, name)
		// The only durable owner-facing trace this refusal gets. There is no
		// queue entry and no badge, by design, so without this the schedule
		// quietly does three quarters of its job forever.
		g.app.notifyToolWithheld(g.owner, g.agentID, name)
		return false
	}
	// No agent behind the run (a scheduled machine, a pipeline stage): an
	// approval would be a grant on nothing, and approving it would change
	// nothing, so the call is refused: the step or the model is told it was
	// not approved, and the log says why.
	if strings.TrimSpace(g.agentID) == "" {
		g.withheld = append(g.withheld, name)
		g.why = "NOT RUN: " + name + " asks the owner first, and this unattended run has no agent the owner could approve it for. " +
			"Do not try to get the same result another way: finish without it, and say plainly that " + name + " was not run."
		Log("[orchestrate/autonomous] owner=%s withheld %q: it asks first, and an unattended run with no agent behind it has nobody to ask", g.owner, name)
		return false
	}
	g.queue(name, args)
	g.why = "NOT RUN: " + name + " needs the owner's approval, and nobody is watching this run. The request is waiting in their Authorizations; once they approve it there, it runs on later runs. " +
		"Do not try to get the same result another way: finish without it, and say plainly that " + name + " is waiting for the owner's approval."
	return false
}

// confirmRefusalKey carries a gate's refusal to a dispatch that was handed the
// gate's confirm by its caller (a standing run), so the run can say why too.
type confirmRefusalKey struct{}

func withConfirmRefusal(ctx context.Context, refusal func(string) string) context.Context {
	return context.WithValue(ctx, confirmRefusalKey{}, refusal)
}

// confirmRefusalFrom is the refusal a caller put on ctx, or nil.
func confirmRefusalFrom(ctx context.Context) func(string) string {
	if ctx == nil {
		return nil
	}
	f, _ := ctx.Value(confirmRefusalKey{}).(func(string) string)
	return f
}

// refusal is the agent loop's ConfirmRefusal for a run this gate approves:
// why the last call was not run, once. Without it the model was told "denied
// by user" on a run nobody was watching, and a Builder sent work by another
// agent, refused three updates "for reasons it couldn't see", gave up on the
// tool and made a new one.
func (g *autonomousGate) refusal(string) string {
	if g == nil {
		return ""
	}
	why := g.why
	g.why = ""
	return why
}

// ownerAsksFirst reports whether the owner asked to be consulted before every
// call of this tool (the tool's own ConfirmInChat, or the owner's ask mark for
// this agent). With nobody watching, that is a queued approval, not a pass;
// a tool the owner approved for unattended runs, here or on an agent above
// this one, is already answered.
func (g *autonomousGate) ownerAsksFirst(name string) bool {
	if g.auto[name] {
		return false
	}
	if tt, _ := toolForCall(g.sess, name); tt != nil && tt.ConfirmInChat {
		return true
	}
	if AuthDB != nil {
		if db := AuthDB(); db != nil && UserToolAsksInChat(db, g.owner, g.agentID, name) {
			return true
		}
	}
	return false
}

// writeWaitsForOwner reports whether this call changes something through a
// credential that asks before writes, with nothing standing in for the
// owner's answer: a tool the owner approved for unattended runs is already
// answered, as for a credential that asks every time. Otherwise the call is
// queued for the owner, whoever sent the message that started the run.
func (g *autonomousGate) writeWaitsForOwner(name, args string) bool {
	if g.auto[name] {
		return false
	}
	tt, action := toolForCall(g.sess, name)
	if tt == nil {
		if p, ok := UserToolByName(UserDB(g.app.DB, g.owner), g.owner, name); ok {
			tt = &p.Tool
		}
	}
	var creds []string
	if tt != nil {
		creds = toolCredentials(*tt)
	} else {
		creds = credentialsForToolCall(nil, name)
	}
	writes := callMayWrite(tt, action, name, args)
	for _, cn := range creds {
		if c, ok := Secure().Resolve(cn, g.owner); ok && c.ConfirmWrites && writes {
			return true
		}
	}
	return false
}

// blocked names a tool refused this run, for the RunAttention line. A QUEUED
// one comes first: it is the kind the owner can do something about, and a run
// that mentions the settled refusal while hiding the pending one points at the
// wrong half of the problem.
func (g *autonomousGate) blocked() string {
	if len(g.queued) > 0 {
		return g.queued[0]
	}
	if len(g.withheld) > 0 {
		return g.withheld[0]
	}
	return ""
}

// neverUnattended is the predicate half of the mark, for the surfaces that
// PREDICT this gate. They ask the gate rather than reading the record, for the
// same reason they ask it about everything else: a second reader of the same
// field is a second rule waiting to drift from this one.
func (g *autonomousGate) neverUnattended(name string) bool { return g.noUnattended[name] }

// withheldTool names a tool refused because the owner marked it never
// unattended, as opposed to one waiting on an approval. The caller says
// different things about the two: one is a decision already made, the other is
// a decision waiting.
func (g *autonomousGate) withheldTool() string {
	if len(g.withheld) == 0 {
		return ""
	}
	return g.withheld[0]
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
	// What it was about to send, the decisive arguments first, as the live
	// card shows them (confirm_summary.go): raw JSON clipped at 200
	// characters could cut off the one value the owner is judging.
	shown := args
	if lines := namedArgs(args); len(lines) > 0 {
		shown = strings.Join(lines, "; ")
	}
	SaveAuthorization(RootDB, Authorization{
		Owner:  g.owner,
		Action: "autonomous_tool",
		Agent:  g.agentID,
		Brief:  name,
		Text:   truncateObs(shown, 600),
	})
	// And a notice, so the fact can leave the building. The pane owns the
	// decision and the badge; this is how a 5am fire is news before 9am.
	g.app.notifyToolQueued(g.owner, g.agentID, name)
	// Best-effort card in the agent's cortex, its record of what happened;
	// the Authorizations pane is the primary surface either way.
	appendCortexObs(UserDB(g.app.DB, g.owner), g.agentID, "Approval needed",
		cortexKindOverflow, "Wanted to use \""+name+"\" on a scheduled run but it isn't pre-authorized. Approve it in the Authorizations pane to allow it on future runs.")
}
