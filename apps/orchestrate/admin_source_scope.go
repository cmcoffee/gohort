// Source scoping — "which of my agents is this file store (or system, or
// document service) linked to", answered from the SOURCE's side.
//
// The Sources modal answers the same question from the agent's side: open
// an agent, see its sources. That is the right shape when you are setting
// an agent up and the wrong one when you are holding a store and want to
// know where it reached. Checking four agents one at a time is how you
// end up believing an attachment took when it did not — and a file store
// is the attachment where being wrong matters, because it is a folder of
// somebody's logs.
//
// Registered as a ScopeProvider so it drives the SAME pill UI and the
// same endpoint as tools and credentials, with no new plumbing (see
// core.RegisterScopeProvider). name is the composite ref the picker
// already stores: "files:<slug>", "system:<id>".
//
// Two deliberate differences from the tool and pipeline planes:
//
//   - There is NO global scope. A tool can sensibly live in a user-wide
//     pool; "every agent I own can read this folder" is a grant nobody
//     should be able to make by accident, and the store's admin-side
//     assignment already decides who may reach it at all. Set refuses the
//     global target rather than silently ignoring it.
//   - Reach is re-checked against the live catalog, not the agent record.
//     An admin can un-assign a store (Store.AllowedUsers) after it was
//     attached, and this surface must not become the way to keep managing
//     one you can no longer reach.
package orchestrate

import (
	"fmt"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	RegisterScopeProvider("source", ScopeProvider{State: sourceScopeState, Set: setSourceScope})
	// The read side of the same fact, for callers that are not this app.
	// A path-scoped parameter on a servitor command tool has to know
	// whether the calling agent is linked to the root it names, and
	// servitor cannot ask an agent record anything — only this package
	// can. See core.ResolvePathScope.
	AgentHoldsReference = agentHoldsReference
}

// agentHoldsReference reports whether one of user's agents carries an
// attachment. Reads through RootDB rather than a passed-in store because
// the caller is a tool dispatch several apps away and has no handle on
// this app's data.
//
// Deliberately NO parent walk. A sub-agent is connected to a machine in
// its own right before it gets that machine's command tools at all
// (applianceEnabledForAgent), so requiring its own source link keeps the
// two halves of one grant consistent. Inheriting the parent's reach
// would mean a sub-agent could touch a folder nobody linked it to.
func agentHoldsReference(user, agentID, kind, itemID string) bool {
	if user == "" || agentID == "" || kind == "" || itemID == "" {
		return false
	}
	udb := agentUserDB(RootDB, user)
	if udb == nil {
		return false
	}
	a, ok := loadAgent(udb, agentID)
	if !ok {
		return false
	}
	return agentHasSource(a, ReferenceSelection{Kind: kind, ItemID: itemID})
}

// sourceVisible reports whether owner can currently reach the item, and
// returns its display name. Resolved through ReferenceGroups — the same
// per-user, access-gated listing the picker renders — so a store that has
// been un-assigned disappears from here at the same moment.
func sourceVisible(owner string, sel ReferenceSelection) (string, bool) {
	for _, grp := range ReferenceGroups(owner) {
		if grp.Kind != sel.Kind {
			continue
		}
		for _, it := range grp.Items {
			if it.ID == sel.ItemID {
				return chFirst(it.Name, sel.Ref()), true
			}
		}
	}
	return "", false
}

// agentHasSource reports whether the record carries the selection.
func agentHasSource(a AgentRecord, sel ReferenceSelection) bool {
	for _, s := range a.AttachedSources {
		if s.Kind == sel.Kind && s.ItemID == sel.ItemID {
			return true
		}
	}
	return false
}

// sourceScopeState builds the pill picture for one source across the
// owner's agents.
func sourceScopeState(db Database, owner, name string) (ToolScopeState, bool) {
	st := ToolScopeState{Name: name, Agents: []ToolScopeAgent{}}
	sel, ok := ParseReferenceRef(name)
	if !ok {
		return st, false
	}
	udb := agentUserDB(db, owner)
	if udb == nil {
		return st, false
	}
	label, reachable := sourceVisible(owner, sel)
	if !reachable {
		// Not found rather than an empty grid. An empty grid reads as "not
		// linked anywhere", which is a different and much more comforting
		// statement than "you cannot reach this any more".
		return st, false
	}
	st.Name = label
	for _, a := range listAgents(udb, owner) {
		holds := agentHasSource(a, sel)
		// App agents carry an app-declared kit; framework seeds are off
		// the pill surfaces unless they already hold an explicit grant.
		// Both rules copied from the pipeline plane so the three planes
		// offer the same set of targets.
		// isCloneOnlySeed too, matching the tool plane. seed-kb is a live seed
		// record that listAgents returns, so without this it was offered as a
		// scope target on two planes of three — a grant made, or kept visible,
		// on a template that is never runnable.
		if isAppAgent(a.ID) || isCloneOnlySeed(a.ID) {
			continue
		}
		if (isSeedID(a.ID) || a.Owner == seedOwner) && !holds {
			continue
		}
		st.Agents = append(st.Agents, ToolScopeAgent{ID: a.ID, Name: a.Name, On: holds})
	}
	return st, true
}

// setSourceScope applies one pill toggle: attach or detach the ref on one
// agent.
func setSourceScope(db Database, owner, name, target string, on bool) error {
	sel, ok := ParseReferenceRef(name)
	if !ok {
		return fmt.Errorf("%q is not a source reference", name)
	}
	if target == "global" {
		return fmt.Errorf("a source has no global scope — link it to the agents that need it, one at a time")
	}
	udb := agentUserDB(db, owner)
	if udb == nil {
		return fmt.Errorf("no agent store for user %q", owner)
	}
	if _, reachable := sourceVisible(owner, sel); !reachable {
		return fmt.Errorf("there is no source called %s you can reach", name)
	}
	a, ok := loadAgent(udb, target)
	if !ok {
		return fmt.Errorf("agent %q not found", target)
	}
	if isAppAgent(a.ID) {
		return fmt.Errorf("%s is an app agent — its sources are declared by the app", chFirst(a.Name, a.ID))
	}
	held := agentHasSource(a, sel)
	if held == on {
		return nil
	}
	if on {
		a.AttachedSources = append(a.AttachedSources, sel)
	} else {
		out := a.AttachedSources[:0:0]
		for _, s := range a.AttachedSources {
			if s.Kind == sel.Kind && s.ItemID == sel.ItemID {
				continue
			}
			out = append(out, s)
		}
		a.AttachedSources = out
	}
	_, err := saveAgent(udb, a)
	return err
}
