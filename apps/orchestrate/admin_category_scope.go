// Scoping a whole CATEGORY at once.
//
// A category's access is not a stored thing. It is the union of the access of
// every tool that claims it, computed on each read — set it and every member
// moves together; change one member on its own and the category reports
// "Custom", because there is no longer a single answer to what it can reach.
//
// Derived rather than stored on purpose. A stored group-scope would be a second
// copy of a fact the tools already carry, and every path that changes ONE
// tool's scope — the tool pill, an authoring flow, a share — would have to
// remember to invalidate it. The first one that forgot would leave a category
// claiming access it no longer has, which on a permissions surface is the worst
// kind of wrong: confidently stale.
//
// This registers as an ordinary ScopeProvider, so the existing pill control,
// its endpoints and its renderer all work unchanged. The only thing the UI
// needed to learn is the third pill state, which is what Partial/Custom carry.
package orchestrate

import (
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	RegisterScopeProvider("category", ScopeProvider{State: categoryScopeState, Set: setCategoryScope})
}

// categoryMembers returns the owner's tools claiming this category, by name.
//
// Membership is the tool's own claim (Tool.Category) — there is no member list
// on the category — so this is a scan of the user's pool. The flattened
// namespace means one record per (user, name), so the pool IS every tool the
// user has and no second home has to be consulted.
func categoryMembers(db Database, owner, category string) []string {
	category = strings.TrimSpace(category)
	if category == "" {
		return nil
	}
	var names []string
	for _, p := range LoadPersistentTempTools(db, owner) {
		if strings.EqualFold(strings.TrimSpace(p.Tool.Category), category) {
			names = append(names, p.Tool.Name)
		}
	}
	sort.Strings(names)
	return names
}

// categoryScopeState folds every member's scope into one picture.
//
// A target is ON only when EVERY member is on it, OFF when none is, and Partial
// when they disagree. That asymmetry is deliberate: a category's access is what
// you can count on reaching through it, so one member missing from an agent
// means the category is not on that agent — reporting it as on would overstate
// what the agent can do, which is the direction that matters on a permissions
// surface.
//
// A category no tool claims returns an EMPTY state and true, not false. False
// makes the endpoint answer "category not found", which is untrue and unhelpful
// — the category exists, it is simply empty, and the caller can say so. Global
// stays false in that case; the fold below would otherwise read "every one of
// zero tools is global" and report a category with nothing in it as granted
// everywhere.
func categoryScopeState(db Database, owner, category string) (ToolScopeState, bool) {
	members := categoryMembers(db, owner, category)
	st := ToolScopeState{Name: category, Agents: []ToolScopeAgent{}}
	if len(members) == 0 {
		return st, true
	}

	// on counts how many members hold each target; the agent ORDER comes from
	// the first member's state so the tree (parents, then their sub-agents)
	// survives the fold.
	on := map[string]int{}
	globalCount := 0
	var order []ToolScopeAgent
	seen := map[string]bool{}
	counted := 0

	for _, name := range members {
		ms, ok := toolScopeState(db, owner, name)
		if !ok {
			// A member with no scope at all — an orphan, or a draft never kept.
			// Counted as holding nothing, which is what it is: it contributes a
			// disagreement rather than being silently skipped, because a
			// category one of whose tools reaches nowhere is not uniformly
			// scoped.
			counted++
			continue
		}
		counted++
		if ms.Global {
			globalCount++
		}
		for _, a := range ms.Agents {
			if !seen[a.ID] {
				seen[a.ID] = true
				order = append(order, ToolScopeAgent{ID: a.ID, Name: a.Name, ParentID: a.ParentID})
			}
			if a.On {
				on[a.ID]++
			}
		}
	}

	st.Global = globalCount == counted
	st.GlobalPartial = globalCount > 0 && globalCount < counted
	for _, a := range order {
		a.On = on[a.ID] == counted
		a.Partial = on[a.ID] > 0 && on[a.ID] < counted
		st.Agents = append(st.Agents, a)
		if a.Partial {
			st.Custom = true
		}
	}
	if st.GlobalPartial {
		st.Custom = true
	}
	return st, true
}

// setCategoryScope applies one pill toggle to every member of the category.
//
// Best-effort across members rather than all-or-nothing: there is no
// transaction spanning the tool records, so a failure part-way would leave the
// category half-moved either way. Carrying on means the rest still land, and
// the state that comes back is honest about the result — the category reads
// Custom, naming exactly the situation the operator has to look at.
//
// A member that is already in the requested state is a no-op inside
// setToolScope, so re-clicking a Partial pill settles the stragglers without
// disturbing the members that were already right.
func setCategoryScope(db Database, owner, category, target string, on bool) error {
	members := categoryMembers(db, owner, category)
	if len(members) == 0 {
		return fmt.Errorf("no tools claim the category %q", category)
	}
	var failed []string
	for _, name := range members {
		if err := setToolScope(db, owner, name, target, on); err != nil {
			Log("[categories] scope %q on %q (target %s, on=%v) failed: %v", category, name, target, on, err)
			failed = append(failed, name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d of %d tools in %q could not be changed (%s) — the category now reads Custom",
			len(failed), len(members), category, strings.Join(failed, ", "))
	}
	Log("[categories] user %q set %q on category %q (%d tools)", owner, target, category, len(members))
	return nil
}
