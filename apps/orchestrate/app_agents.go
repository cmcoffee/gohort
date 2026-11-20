// App Agents — orchestrate's adapter for the cross-app agent registry
// (core/app_agents.go). Apps register an AppAgentSpec in their init(); here we
// map each spec to an AgentRecord and fold it into seedAgents(), so a
// registered app agent resolves through loadAgent (framework/app owns the
// prompt, the per-user shadow overlay owns operational state), lists in the
// Agency dashboard, and is dispatchable — all via the SAME machinery as
// orchestrate's own seeds, with no new resolution path.
//
// Next step (not in this slice): a dedicated "App Agents" dashboard section
// grouped by OwningApp. listAgents already surfaces these; AppAgentByID lets
// that future section tell app-owned agents apart from seeds + user records.

package orchestrate

import (
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
)

// isAppAgent reports whether id belongs to a code-registered App Agent. This is
// the AUTHORITATIVE app-agent test — it keys on the registry, not on drift-able
// runtime proxies (Owner=="system", Hidden), which a stale per-user shadow can
// forge. Every safeguard that must hold the app-agent boundary (no LLM-authored
// tools, not a scope target) keys on THIS, so the boundary can't be walked
// across by a mis-saved record. (Phase 0 of the AgentRecord split — until the
// type itself carries the boundary, this is the single source of truth.)
func isAppAgent(id string) bool {
	_, ok := appagents.AppAgentByID(id)
	return ok
}

// appAgentGroup is the group an app agent shows under in every agent picker,
// apart from the person's own agents: "App agents: Scribe". ok is false for
// anything that is not an app agent. One label everywhere, so an app's agent
// reads as the app's wherever it is offered, rather than as one more agent of
// the user's that happens to sit in a group named after an app.
func appAgentGroup(id string) (group string, ok bool) {
	s, ok := appagents.AppAgentByID(id)
	if !ok {
		return "", false
	}
	return "App agents: " + chFirst(s.OwningApp, "other apps"), true
}

// agentAppGroup is appAgentGroup for a record. A custom app's own agent has
// none: it is in no list a person picks from (appOwnedAgent).
func agentAppGroup(a AgentRecord) (string, bool) {
	return appAgentGroup(a.ID)
}

// agentGroupFor is agentAppGroup with the group a picker gives everything
// else.
func agentGroupFor(a AgentRecord, others string) string {
	if g, ok := agentAppGroup(a); ok {
		return g
	}
	return others
}

// agentGroup is appAgentGroup with the group a picker gives everything else.
func agentGroup(id, others string) string {
	if g, ok := appAgentGroup(id); ok {
		return g
	}
	return others
}

// appGroupsLast orders picker groups: everything else first, keeping its
// order, then the app groups by name. For sort.SliceStable.
func appGroupsLast(a, b string) bool {
	ia, ib := strings.HasPrefix(a, "App agents"), strings.HasPrefix(b, "App agents")
	if ia != ib {
		return ib
	}
	return ia && a < b
}

// hiddenAppAgent reports whether id is an app agent whose SPEC says Hidden.
// Keys on the registry for the same reason isAppAgent does: a stale per-user
// shadow can carry Hidden=false long after the spec flipped, and every listing
// that consulted the record instead of the spec has leaked one already.
func hiddenAppAgent(id string) bool {
	s, ok := appagents.AppAgentByID(id)
	return ok && s.Hidden
}

// appAgentForcesPrivate reports whether id is an app agent whose SPEC declares
// ForcePrivate — it handles material that must not reach a third-party model.
//
// Keys on the registry for the same reason hiddenAppAgent does, and the stakes
// are higher: a per-user shadow saved before the spec set the flag carries
// ForcePrivate=false forever, and a record consulted instead of the spec would
// let servitor's investigator — which reads SSH credentials, log contents and
// system facts — escalate to a remote lead model. Hidden leaking an agent into
// a picker is embarrassing; this one sends credentials somewhere they cannot be
// recalled from.
func appAgentForcesPrivate(id string) bool {
	s, ok := appagents.AppAgentByID(id)
	return ok && s.ForcePrivate
}

// appAgentDispatchMode returns the SPEC-declared dispatch policy for an app
// agent, and whether id is one at all.
//
// Consulted for the same reason appAgentForcesPrivate is: a per-user shadow
// saved before the spec declared anything carries a blank DispatchMode forever,
// and blank on a RECORD is read as "all". Without this the fix would hold only
// for app agents nobody had ever opened in the editor.
//
// It is a FALLBACK, never an override — see effectiveDispatchMode. A record
// carrying an explicit mode is a decision, whether the owner made it in the
// editor or the hosting app made it on its per-turn copy, and a spec default
// must not overrule either.
// appAgentExactTools reports whether an app agent's spec limits its turns to
// exactly its own tools (appagents.AppAgentSpec.ExactTools). An app that runs
// its agent under a scoped id ("app-anvil-coder:<project>", so each project
// keeps its own conversations and memory) is that agent: the part before the
// colon names the spec. Looked up by the whole id alone, Anvil's turns never
// matched, and its coding assistant was offered 44 tools, image and video
// among them, about 15K tokens on every call.
func appAgentExactTools(id string) bool {
	s, ok := appagents.AppAgentByID(id)
	if !ok {
		if base, _, scoped := strings.Cut(id, ":"); scoped {
			s, ok = appagents.AppAgentByID(base)
		}
	}
	return ok && s.ExactTools
}

// exactToolsOnly keeps the tools an ExactTools agent is offered: its
// AllowedTools, the host app's tools for this turn, and the two ask tools. It
// returns the kept tools and the names it dropped.
func exactToolsOnly(tools, appTools []AgentToolDef, allowed []string) ([]AgentToolDef, []string) {
	keep := map[string]bool{"ask_user": true, "ask_user_form": true}
	for _, n := range allowed {
		keep[n] = true
	}
	for _, td := range appTools {
		keep[td.Tool.Name] = true
	}
	var out []AgentToolDef
	var dropped []string
	for _, td := range tools {
		if keep[td.Tool.Name] {
			out = append(out, td)
		} else {
			dropped = append(dropped, td.Tool.Name)
		}
	}
	return out, dropped
}

func appAgentDispatchMode(id string) (string, bool) {
	s, ok := appagents.AppAgentByID(id)
	if !ok {
		return "", false
	}
	return s.EffectiveDispatchMode(), true
}

// agentForcesPrivate is THE question "must this agent stay private" — the
// record's own flag OR the spec's, for every caller that enforces it.
//
// The comment above explains why the spec has to be consulted; what it did not
// do was reach the places that enforce. Only the model-routing gate ORed the
// two. The two paths that actually strip network access — the dispatch
// tool-filter and the web turn's connector — read the record alone, so a stale
// shadow was correctly denied a remote lead model while keeping fetch_url,
// web_search and browse_page in its catalog and an open connector behind them.
// For servitor's investigator, which holds SSH credentials and log contents,
// that is the wrong half to enforce.
//
// One function so the answer cannot differ by call site, which is how it came
// to differ in the first place.
func agentForcesPrivate(a AgentRecord) bool {
	return a.ForcePrivate || appAgentForcesPrivate(a.ID) || privateParentOf(a) != ""
}

// privateParentOf names the nearest parent of a sub-agent that is forced
// Private, which makes the sub-agent Private too: a sub-agent may be stricter
// than its parent, never wider, and it runs on its own schedules as well as
// when its parent calls it. "" for a top-level agent or none.
func privateParentOf(a AgentRecord) string {
	if strings.TrimSpace(a.OwnedBy) == "" {
		return ""
	}
	db := agentStore(a.Owner)
	for _, id := range agentAncestors(db, a.ID) {
		if p, ok := loadAgent(db, id); ok && p.ForcePrivate {
			return chFirst(p.Name, p.ID)
		}
	}
	return ""
}

// appAgentVisibilityWarnOnce fires the visible-app-agent lint a single time,
// the first time app agents are folded into resolution (startup / first agent
// list). Guarded so the per-request registeredAppAgents() call doesn't spam.
var appAgentVisibilityWarnOnce sync.Once

// warnVisibleAppAgents logs a one-time WARNING for every app-agent registered
// NON-Hidden. A visible app-agent shows in the picker AND becomes a tool-scope
// target, so a stray tool can be mis-scoped onto it (the Casefile "Case
// Analyzer" incident). The removal + Hidden-from-spec fixes make that
// recoverable, but the trap is easy to walk into — RegisterAppAgent can't lint
// (it's a stdlib-only leaf), so the nudge lives here where nfo logging exists.
func warnVisibleAppAgents() {
	for _, s := range appagents.AppAgents() {
		if !s.Hidden {
			Warn("[app-agents] %q (%s) is registered VISIBLE (Hidden:false), it appears in the agent picker and becomes a tool-scope target, so a stray tool can be mis-scoped onto it. Register it Hidden:true unless users are meant to chat with it directly.", s.Name, s.ID)
		}
	}
}

// appAgentSpecToRecord maps a portable appagents.AppAgentSpec onto orchestrate's
// AgentRecord. Owner=seedOwner marks it framework/app-owned (read-mostly, with
// a per-user shadow for customization), matching how seeds are tagged.
func appAgentSpecToRecord(s appagents.AppAgentSpec) AgentRecord {
	return AgentRecord{
		ID:                 s.ID,
		Owner:              seedOwner,
		Name:               s.Name,
		Description:        s.Description,
		OrchestratorPrompt: s.Prompt,
		AllowedTools:       s.AllowedTools,
		Hidden:             s.Hidden,
		Cortex:             s.Cortex,
		MemoryMode:         s.MemoryMode,
		DisableExplicit:    s.DisableExplicit,
		MemoryPerDocument:  s.MemoryPerDocument,
		ForcePrivate:       s.ForcePrivate,
		// Carried onto the record so the agent editor SHOWS the policy rather
		// than an empty select that reads as "all". effectiveDispatchMode still
		// falls back to the spec, for the shadow that predates this field.
		DispatchMode: s.EffectiveDispatchMode(),
	}
}

// registeredAppAgents converts every cross-app registered agent to a record.
func registeredAppAgents() []AgentRecord {
	appAgentVisibilityWarnOnce.Do(warnVisibleAppAgents)
	specs := appagents.AppAgents()
	out := make([]AgentRecord, 0, len(specs))
	for _, s := range specs {
		out = append(out, appAgentSpecToRecord(s))
	}
	return out
}

// seedAgents returns orchestrate's own in-code seeds plus every app-registered
// agent. seedAgentByID / isSeedID / loadAgent's miss path / listAgents pass 2
// all walk this, so app agents inherit seed resolution + shadow overlay for
// free. coreSeedAgents() returns a fresh slice each call, so the append never
// aliases the in-code literal.
func seedAgents() []AgentRecord {
	return append(coreSeedAgents(), registeredAppAgents()...)
}

// appOwnedAgent reports whether a is one of an app's own agents (a custom
// app's, docs/design/app-history.md). Those live inside their app, as a
// built-in app's hidden agents do (the guide writer, the publisher): its
// chat, scripts and pipelines reach them, Builder and Anvil change them, and
// no list a person picks from or reads shows them. What they ran and cost
// shows under the app's name (appAgentLabels); an administrator still sees
// them, marked as the app's.
func appOwnedAgent(a AgentRecord) bool { return strings.TrimSpace(a.OwningApp) != "" }

// sameAppAgent reports whether caller and target are agents of the same app:
// the only agents an app's agent is reachable from, listed to, or handed work
// by.
func sameAppAgent(caller, target AgentRecord) bool {
	app := strings.TrimSpace(target.OwningApp)
	return app != "" && app == strings.TrimSpace(caller.OwningApp) && caller.Owner == target.Owner
}

// appAgentName is the name of a's app, as a person knows it.
func appAgentName(a AgentRecord) string {
	slug := strings.TrimSpace(a.OwningApp)
	if spec, ok := LoadAppSpec(a.Owner, slug); ok && strings.TrimSpace(spec.Name) != "" {
		return spec.Name
	}
	return slug
}

// runAgentLabel is how a run of a shows where running work is listed (the
// Monitor, the live pill): an app's agent by its app's name ("Recipes
// (app)"), as Fleet's records show it, since the agent is the app's own
// business.
func runAgentLabel(a AgentRecord) string {
	if appOwnedAgent(a) {
		return appAgentName(a) + " (app)"
	}
	return a.Name
}

// appRunLabel is owner's app slug as running work done for it is listed:
// "Recipes (app)".
func appRunLabel(owner, slug string) string {
	return appAgentName(AgentRecord{Owner: owner, OwningApp: slug}) + " (app)"
}

// appAgentLabels are user's app agents, each named by its app ("Recipes
// (app)"): how Fleet's records of runs, spend and blocks show what an app's
// agent did, since the agent itself is the app's own business.
func appAgentLabels(udb Database, user string) map[string]string {
	out := map[string]string{}
	if udb == nil {
		return out
	}
	for _, a := range listAgents(udb, user) {
		if appOwnedAgent(a) {
			out[a.ID] = appAgentName(a) + " (app)"
		}
	}
	return out
}

// inAppOf reports whether an item of owner's app slug is reachable from
// caller: an item no app owns always is; an app's own pipeline or machine
// only from an agent of the same app, as an app's agent is (sameAppAgent).
func inAppOf(caller AgentRecord, slug, owner string) bool {
	slug = strings.TrimSpace(slug)
	return slug == "" || (slug == strings.TrimSpace(caller.OwningApp) && owner == caller.Owner)
}

// appItemName is the name of owner's app slug, as a person knows it.
func appItemName(owner, slug string) string {
	if spec, ok := LoadAppSpec(owner, slug); ok && strings.TrimSpace(spec.Name) != "" {
		return spec.Name
	}
	return slug
}

// adminMarked is name marked as part of owner's app slug, for the
// administrator's lists, which still show an app's own items; name as it is
// for an item no app owns.
func adminMarked(name, owner, slug string) string {
	if strings.TrimSpace(slug) == "" {
		return name
	}
	return name + " (part of " + appItemName(owner, slug) + ")"
}
