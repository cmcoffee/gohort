package orchestrate

// A Builder run another agent sent works within what that agent may use.
//
// Builder is everybody's deputy: it loads every tool, so it can run and fix
// any of them, and an API narrowed to "Only these agents" does not apply to it
// when the owner opens Builder themselves. Without a limit on the other path,
// an agent that may not touch an API could ask Builder to call it, or to
// change a tool an allowed agent uses, and Builder would do it with the
// owner's reach. The restriction would only stop agents that never thought to
// ask.
//
// So when an agent sends Builder a job (a dispatch, or a build the owner
// approved on its behalf), that run carries the sending agent's limits:
//
//   - its kit holds only the tools the sender would load itself;
//   - it is denied every API the sender is denied;
//   - tool_def refuses to create, change, delete or test a tool the sender
//     could not use, or one that names an API the sender may not use;
//   - Builder's API tools refuse an API the sender may not use;
//   - create_agent, update_agent, add_tool and tool_template refuse to give
//     an agent a tool the sender could not use, or a definition naming an API
//     the sender may not use (agentToolsRefusal), so the sender cannot reach
//     past its limits through an agent Builder builds for it.
//
// The owner driving Builder is unaffected. The sender is read once per run;
// one that cannot be read is treated as having no tools and no APIs it is
// narrowed to, so the run fails closed.

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// builderDelegator is the agent this Builder session is working for, when
// another agent sent it. ok is false for any other session, including the
// owner's own Builder chat.
func builderDelegator(sess *ToolSession, db Database) (AgentRecord, bool) {
	if sess == nil || !isBuilderAgent(sess.AgentID) {
		return AgentRecord{}, false
	}
	id := strings.TrimSpace(sess.DispatchParentAgentID)
	if id == "" || isBuilderAgent(id) {
		return AgentRecord{}, false
	}
	if db != nil {
		if a, ok := loadAgent(db, id); ok {
			return a, true
		}
	}
	return AgentRecord{ID: id, AllowedTools: []string{noToolsSentinel}}, true
}

// delegatorName is how a refusal names the sending agent.
func delegatorName(a AgentRecord) string {
	if n := strings.TrimSpace(a.Name); n != "" {
		return n
	}
	return a.ID
}

// credentialDenyFor is credentialDenySet for a session: a Builder run another
// agent sent is also denied everything that agent is denied.
func credentialDenyFor(a AgentRecord, sess *ToolSession) map[string]bool {
	user := ""
	var db Database
	if sess != nil {
		user, db = sess.Username, sess.DB
	}
	deny := credentialDenySet(a, user)
	from, ok := builderDelegator(sess, db)
	if !ok {
		return deny
	}
	for name := range credentialDenySet(from, user) {
		if deny == nil {
			deny = map[string]bool{}
		}
		deny[name] = true
	}
	return deny
}

// applyBuilderDelegation puts the sending agent's limits on a Builder run's
// session: its API deny set, and the tool_def refusal. db is the store the
// agents and the tool pool live in. A no-op for any other session.
func applyBuilderDelegation(sess *ToolSession, db Database) {
	from, ok := builderDelegator(sess, db)
	if !ok {
		return
	}
	for name := range credentialDenySet(from, sess.Username) {
		if sess.DeniedCredentials == nil {
			sess.DeniedCredentials = map[string]bool{}
		}
		sess.DeniedCredentials[name] = true
	}
	gate := newPoolToolGate(db, from)
	who := delegatorName(from)
	owner := sess.Username
	sess.ToolEditRefusal = func(toolName, credName string) string {
		if credName != "" && sess.CredentialDenied(credName) {
			return delegationRefusal(who, "the API "+credName)
		}
		for _, p := range LoadPersistentTempTools(db, owner) {
			if p.Tool.Name != toolName {
				continue
			}
			if !gate.admits(p) || sess.CredentialDenied(p.Tool.Credential) {
				return delegationRefusal(who, "the tool "+toolName)
			}
			break
		}
		return ""
	}
	Log("[orchestrate.delegation] Builder run for %s works within %s's limits (%d API(s) denied)", owner, from.ID, len(sess.DeniedCredentials))
}

// delegationRefusal is what a sent Builder run returns instead of using or
// changing something its sender may not use.
func delegationRefusal(who, what string) string {
	return fmt.Sprintf("NOT DONE: %s sent this job, and %s may not use %s, so this run cannot use or change it either. "+
		"Do not look for another way to do it. Say in your reply that the owner has to make this change: they can open Builder themselves, "+
		"or allow %s in that tool's or API's Security panel and send the job again.", who, who, what, who)
}

// builderCredentialToolNames are Builder's API tools that act on one named
// API (its name argument).
var builderCredentialToolNames = map[string]bool{
	"draft_api_credential":    true,
	"draft_oauth_credential":  true,
	"update_api_credential":   true,
	"store_credential_secret": true,
	"check_credential":        true,
}

// limitCredentialToolsToDelegator wraps Builder's API tools on a sent run so
// each refuses an API the sender may not use. Returns the tools unchanged on
// any other session.
func limitCredentialToolsToDelegator(sess *ToolSession, tools []AgentToolDef) []AgentToolDef {
	if sess == nil {
		return tools
	}
	from, ok := builderDelegator(sess, sess.DB)
	if !ok {
		return tools
	}
	who := delegatorName(from)
	for i := range tools {
		if !builderCredentialToolNames[tools[i].Tool.Name] || tools[i].Handler == nil {
			continue
		}
		inner := tools[i].Handler
		tools[i].Handler = func(ctx context.Context, args map[string]any) (string, error) {
			if name := strings.TrimSpace(stringArg(args, "name")); name != "" && sess.CredentialDenied(name) {
				return delegationRefusal(who, "the API "+name), nil
			}
			return inner(ctx, args)
		}
	}
	return tools
}

// poolToolGate is the rule for which of the owner's pool tools an agent
// loads, as one value: the turn applies it to its own agent, and a Builder
// run another agent sent applies it to that agent as well. Private mode is the
// turn's, not the agent's, and is checked by the caller.
type poolToolGate struct {
	id       string
	builder  bool // Builder loads everything, so it can run and fix it
	noTools  bool
	disabled map[string]bool // the agent's per-tool opt-outs
	allow    map[string]bool // a user-made agent's allow list; nil = all
	parents  []string        // a sub-agent's parents, nearest first
	inherit  bool            // a sub-agent that carries its parents' tools
}

// newPoolToolGate is the gate for agent a. db is the agent store, read for a
// sub-agent's parents; nil reads none.
func newPoolToolGate(db Database, a AgentRecord) poolToolGate {
	g := poolToolGate{id: a.ID, builder: isBuilderAgent(a.ID), noTools: isNoToolsSentinel(a.AllowedTools),
		inherit: a.InheritParentTools}
	if strings.TrimSpace(a.OwnedBy) != "" {
		g.parents = agentAncestors(db, a.ID)
	}
	g.disabled = make(map[string]bool, len(a.DisabledPersistentTools))
	for _, n := range a.DisabledPersistentTools {
		g.disabled[n] = true
	}
	// Treat any agent backed by an in-code seed as a seed regardless of
	// whether its per-user shadow carries Owner==seedOwner. The seed-chat
	// shadow may have Owner==username when the user saved customizations
	// through the admin UI, but the AllowedTools gate must still be nil so
	// all approved persistent tools (including toolbox-mode tools like vapi
	// that bypass the OnTempToolApproved path) auto-load.
	_, isSeedBacked := seedAgentByID(a.ID)
	isSeed := a.Owner == seedOwner || isSeedBacked
	// User-crafted agents only: an explicit AllowedTools list still gates
	// persistent temp tools. Empty list = default pool = include all. Nil for
	// seed agents (admin approval is enough).
	if !isSeed && !g.noTools && len(a.AllowedTools) > 0 {
		g.allow = make(map[string]bool, len(a.AllowedTools))
		for _, n := range a.AllowedTools {
			g.allow[canonicalToolName(n)] = true
		}
	}
	return g
}

// reaches reports whether a tool's agent scope includes this agent. Builder
// is reached by every scope.
func (g poolToolGate) reaches(p PersistentTempTool) bool {
	return len(p.ScopeAgents) == 0 || g.builder || p.ScopedToAgent(g.id) || g.takesFromParent(p)
}

// takesFromParent reports whether a sub-agent carries a tool scoped to one of
// its parents. The right is the parent's (a sub-agent is listed in no picker
// a tool is scoped in), but sub-agents are narrow specialists, so it carries
// the tool only when it asks for it: its own tool list names it, or it
// inherits its parents' tools.
func (g poolToolGate) takesFromParent(p PersistentTempTool) bool {
	if len(g.parents) == 0 || !(g.inherit || g.allow[p.Tool.Name]) {
		return false
	}
	for _, id := range g.parents {
		if p.ScopedToAgent(id) {
			return true
		}
	}
	return false
}

// admits reports whether the agent loads p.
func (g poolToolGate) admits(p PersistentTempTool) bool {
	if g.noTools {
		return false
	}
	// Governance visibility: a Disabled tool (turned off in Extensions › Tools),
	// a Builder-only tool and a bound-only one are hidden from every agent
	// EXCEPT Builder, which has to be able to load, RUN and fix them: a tool it
	// cannot run is a tool it cannot repair.
	if (p.Tool.Disabled || p.Tool.BuilderOnly || p.Tool.BoundOnly) && !g.builder {
		return false
	}
	if len(p.ScopeAgents) > 0 {
		// Agent-scoped: part of this agent's own kit when scoped to it, with
		// no allow-list gate (it is attached by intent), but an explicit
		// opt-out still wins. Builder still loads it: turning a tool off must
		// not make it unfixable by the surface that fixes tools.
		if g.builder {
			return true
		}
		return (p.ScopedToAgent(g.id) || g.takesFromParent(p)) && !g.disabled[p.Tool.Name]
	}
	if g.disabled[p.Tool.Name] {
		return false
	}
	return g.allow == nil || g.allow[p.Tool.Name]
}

// agentToolsRefusal is the refusal for an authoring call that would give an
// agent tools: inline definitions it attaches, and tool names it grants
// (allowed_tools). Two checks: a run another agent sent is held to that
// agent's limits, and an inline definition that would REPLACE an existing tool
// of the owner's asks them first (change_approval.go). "" lets it go ahead.
func agentToolsRefusal(sess *ToolSession, inline []TempTool, allowed []string) string {
	if sess == nil {
		return ""
	}
	if sess.ApproveChange != nil {
		for _, t := range inline {
			if p, ok := UserToolByName(sess.DB, sess.Username, t.Name); ok && !p.Tool.SameDefinition(t) {
				if msg := sess.ApproveChange("tool", t.Name, "Replace its definition with the one this agent brings.", ChangeEdit); msg != "" {
					return msg
				}
			}
		}
	}
	if sess.ToolEditRefusal == nil {
		return ""
	}
	for _, t := range inline {
		for _, c := range toolCredentialNames(t) {
			if msg := sess.ToolEditRefusal(t.Name, c); msg != "" {
				return msg
			}
		}
	}
	for _, n := range allowed {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if n == "*" {
			// Every tool: only as wide as the sender's own reach.
			if from, ok := builderDelegator(sess, sess.DB); ok {
				if g := newPoolToolGate(sess.DB, from); g.noTools || g.allow != nil {
					who := delegatorName(from)
					return fmt.Sprintf("NOT DONE: %s sent this job, and %s has a list of the tools it may use, so this run cannot give an agent every tool. Name the tools instead, from those %s may use.", who, who, who)
				}
			}
			continue
		}
		if msg := sess.ToolEditRefusal(canonicalToolName(n), ""); msg != "" {
			return msg
		}
	}
	return ""
}

// toolCredentialNames is "" (the tool itself) plus every API a definition
// names: its credential, and a script's fetch_via:/secret: capabilities.
func toolCredentialNames(t TempTool) []string {
	out := []string{""}
	if c := strings.TrimSpace(t.Credential); c != "" {
		out = append(out, c)
	}
	for _, c := range t.HookCapabilities {
		if i := strings.IndexByte(c, ':'); i >= 0 {
			switch strings.ToLower(strings.TrimSpace(c[:i])) {
			case "fetch_via", "secret":
				out = append(out, strings.TrimSpace(c[i+1:]))
			}
		}
	}
	return out
}
