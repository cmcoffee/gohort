package orchestrate

import (
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
)

// noToolsSentinel is the reserved AllowedTools[0] marker meaning
// "admin explicitly disabled all optional tools." The framework
// distinguishes this from a bare empty list (which means "use the
// default pool") so the user's intent survives a save → reload cycle
// in the Tools modal.
//
// It IS core.NoToolsMarker, not a second marker that happens to match: a
// phase's tool list and an agent's make the same statement, and a machine
// exported from one deployment and imported into another has to keep
// meaning it. Two literals a package apart would drift the first time one
// of them was reconsidered.
const noToolsSentinel = NoToolsMarker

// isNoToolsSentinel reports whether AllowedTools is the explicit
// no-optional-tools marker. Exported via the package so runner.go's
// resolveWorkerTools can short-circuit before the default-pool
// expansion.
func isNoToolsSentinel(allowed []string) bool {
	return len(allowed) == 1 && allowed[0] == noToolsSentinel
}

// Dispatch policy modes — how an agent's AllowedDispatchTargets list is read.
// See AgentRecord.DispatchMode. Resolve with effectiveDispatchMode, never the
// raw field, so back-compat inference + the deleted-target self-heal apply.
// Aliased to appagents' spellings rather than restated, so a spec and a record
// cannot come to disagree about what "none" is spelled like.
const (
	dispatchAll    = appagents.DispatchAll    // any non-hidden agent (default)
	dispatchOnly   = appagents.DispatchOnly   // allowlist: only the listed agents
	dispatchExcept = appagents.DispatchExcept // denylist: any non-hidden agent EXCEPT the listed
	dispatchNone   = appagents.DispatchNone   // no dispatch at all
)

// Exported spellings of the modes above, for an APP that hosts an agent turn
// and needs to set the policy on its per-turn record copy. The `agents` grouped
// tool is a framework tool: it rides every turn regardless of AllowedTools, so
// an app whose agent should not reach the user's whole fleet has to say so here
// rather than by leaving it off an allowlist.
const (
	DispatchAll    = dispatchAll
	DispatchOnly   = dispatchOnly
	DispatchExcept = dispatchExcept
	DispatchNone   = dispatchNone
)

// AgentReferenceKind is the ReferenceSource kind an AGENT is attached under, so
// a consumer can tell an attached agent apart from an attached system or file
// store without hardcoding the string.
const AgentReferenceKind = "agent"

// effectiveDispatchMode resolves an agent's dispatch policy, applying back-
// compat: a blank DispatchMode with a non-empty AllowedDispatchTargets is the
// legacy allowlist ("only"); blank with an empty list is "all". An unrecognized
// value degrades to "all" (fail-open to the default, never a silent hard block).
func effectiveDispatchMode(a AgentRecord) string {
	switch a.DispatchMode {
	case dispatchAll, dispatchOnly, dispatchExcept, dispatchNone:
		return a.DispatchMode
	default:
		if len(a.AllowedDispatchTargets) > 0 {
			return dispatchOnly
		}
		// An APP agent's blank means none, not all. It is hidden, bound to its
		// app's surface, and the `agents` tool rides its turns regardless of
		// AllowedTools — so the ordinary fail-open default handed every app
		// agent the user's whole fleet without its author choosing that. Read
		// from the spec, because a per-user shadow saved before the spec grew
		// the field carries a blank one forever. See appAgentDispatchMode.
		if mode, ok := appAgentDispatchMode(a.ID); ok {
			return mode
		}
		return dispatchAll
	}
}

// dispatchModeAfterSelfHeal is effectiveDispatchMode with the deleted-target
// rescue applied: an "only" list whose every named agent has since been deleted,
// and which names no runnable pipeline or machine either, is read as "all"
// rather than as "nothing is reachable".
//
// It exists because that rescue lived in the fleet-catalog renderer alone, and
// the dispatch gate resolved the mode raw. So the rescued agent was shown the
// whole fleet under "**If a question lands in one of these agents' domains,
// DELEGATE FIRST**" and then had every dispatch it made refused with "not on
// this agent's dispatch allow list". The advertisement healed and the door did
// not, which is a worse state than either alone: the model is steered into a
// call and punished for making it, round after round.
//
// Both surfaces call this now. A catalog that offers what the gate refuses is
// the drift worth preventing, not the mode calculation itself.
func (t *chatTurn) dispatchModeAfterSelfHeal(fleetDB Database, fleetUser string) string {
	mode := effectiveDispatchMode(t.agent)
	if mode != dispatchOnly {
		return mode
	}
	for _, a := range listAgents(fleetDB, fleetUser) {
		if dispatchListContains(t.agent, a.ID) {
			return mode // at least one named target still exists
		}
	}
	if t.dispatchListNamesARunnable() {
		return mode // names a pipeline or machine instead of an agent
	}
	return dispatchAll
}

// dispatchListNames reports whether a dispatch target list names a target by
// any of the identities that target answers to.
//
// An agent answers to its id, which is what the picker writes. A PIPELINE also
// answers to its NAME: the picker writes ids for it too, but a list edited
// through the agent tool (or by hand) carries the name the author knows it by,
// and a grant that silently means nothing is worse than no grant at all.
func dispatchListNames(list []string, ids ...string) bool {
	for _, x := range list {
		if x = strings.TrimSpace(x); x == "" {
			continue
		}
		for _, id := range ids {
			if id != "" && strings.EqualFold(x, id) {
				return true
			}
		}
	}
	return false
}

// dispatchListContains reports whether id is in the agent's dispatch target list.
func dispatchListContains(a AgentRecord, id string) bool {
	for _, x := range a.AllowedDispatchTargets {
		if x == id {
			return true
		}
	}
	return false
}

// selfHealAllowedTools strips entries from AllowedTools that no
// longer resolve — either because the registered tool was removed
// (post-blocklist update / migration) or because a persistent temp
// tool referenced by name has been deleted. Cleaned record is
// persisted back so the orphan is gone for good on the next read.
// No-op when AllowedTools is empty (default-pool agents) or when
// nothing is stale. Also no-op when the no-tools sentinel is set —
// the marker isn't a registered tool name and would otherwise get
// stripped, silently reverting the agent to the default pool.
func selfHealAllowedTools(db Database, a AgentRecord) AgentRecord {
	if len(a.AllowedTools) == 0 || isNoToolsSentinel(a.AllowedTools) {
		return a
	}
	cleaned := a.AllowedTools[:0]
	dropped := false
	for _, name := range a.AllowedTools {
		if isResolvableToolName(db, a.Owner, name) {
			cleaned = append(cleaned, name)
			continue
		}
		Log("[orchestrate.agents] dropping stale tool %q from agent %q AllowedTools (not registered, not in owner's temp-tool pool)", name, a.ID)
		dropped = true
	}
	if !dropped {
		return a
	}
	// Healing away the LAST entry must not empty the list. An empty
	// AllowedTools reads as "sees the whole default pool" (see the guard at the
	// top of this function, and agentSeesGlobalTool), so an agent pinned to a
	// restricted list whose entries all went stale — e.g. its only temp tool was
	// deleted — would silently WIDEN from a few tools to every tool. Collapse to
	// the explicit no-tools sentinel instead: the restriction was deliberate, so
	// losing its last member means "nothing", never "everything".
	if len(cleaned) == 0 {
		Log("[orchestrate.agents] agent %q AllowedTools healed to empty; pinning no-tools sentinel rather than widening to the default pool", a.ID)
		a.AllowedTools = []string{noToolsSentinel}
	} else {
		a.AllowedTools = cleaned
	}
	a.Updated = time.Now()
	db.Set(agentsTable, a.ID, a)
	return a
}

// isResolvableToolName reports whether the given name maps to either
// a registered ChatTool, a connected gohort-desktop local tool, or
// one of the agent owner's persistent temp tools. Used to detect
// orphan entries left in AllowedTools after a tool gets unregistered
// or a temp tool gets deleted.
//
// Client-bridge tools (ClientToolPrefix) are treated as
// ALWAYS resolvable: they're framework-runtime tools injected
// per-turn from the desktop bridge regardless of whether the
// agent's AllowedTools lists them, and the desktop may be
// disconnected at AllowedTools-load time even when it's connected
// later at chat-turn time. Stripping them at load would create a
// thrash where the user toggles them on, the load self-heals them
// off, and the runtime keeps adding them via the per-turn hook
// anyway.
func isResolvableToolName(db Database, owner, name string) bool {
	if name == "" {
		return false
	}
	if IsClientToolName(name) {
		return true
	}
	// Legacy call_<credential> aliases resolve to fetch_url_<credential>.
	// Treat them as resolvable so AllowedTools entries from before the
	// 0.3.1 rename don't get stripped by self-heal. The agent loop's
	// lookup path applies the same translation. call_no_auth has no
	// counterpart — fetch_url covers it directly — so that legacy name
	// fails through and is healed away on first save.
	if strings.HasPrefix(name, "call_") && name != "call_no_auth" {
		return true
	}
	if _, ok := FindChatTool(name); ok {
		return true
	}
	if owner == "" || db == nil {
		return false
	}
	// Persistent temp tools live in RootDB keyed by username; the
	// LoadPersistentTempTools helper handles the lookup with the
	// canonical store regardless of which db we pass in.
	for _, p := range LoadPersistentTempTools(db, owner) {
		if p.Tool.Name == name {
			return true
		}
	}
	return false
}
