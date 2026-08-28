package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// categoryFixture stands up a user with two agents and two tools claiming one
// category, plus a third tool claiming nothing.
func categoryFixture(t *testing.T) (root Database, owner string) {
	t.Helper()
	r := &DBase{Store: kvlite.MemStore()}
	prevRoot := RootDB
	RootDB = r
	t.Cleanup(func() { RootDB = prevRoot })

	udb := agentUserDB(r, "u")
	for _, a := range []AgentRecord{
		{ID: "agent-one", Name: "Wren", Owner: "u", OrchestratorPrompt: "p"},
		{ID: "agent-two", Name: "Kestrel", Owner: "u", OrchestratorPrompt: "p"},
	} {
		if _, err := saveAgent(udb, a); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []TempTool{
		{Name: "list_files", Description: "d", CommandTemplate: "ls", Category: "Files"},
		{Name: "read_file", Description: "d", CommandTemplate: "cat", Category: "Files"},
		{Name: "send_mail", Description: "d", CommandTemplate: "mail"},
	} {
		if err := AdminPersistTempTool(udb, "u", tt); err != nil {
			t.Fatal(err)
		}
	}
	return r, "u"
}

// Membership is the tools' own claim, so a category's members are exactly the
// tools naming it — and nothing else gets swept in.
func TestCategoryMembersAreTheToolsThatClaimIt(t *testing.T) {
	root, owner := categoryFixture(t)

	got := categoryMembers(root, owner, "Files")
	if len(got) != 2 || got[0] != "list_files" || got[1] != "read_file" {
		t.Fatalf("members = %v, want the two tools claiming Files", got)
	}
	// Case-insensitive, because a category is a free-form label a person types.
	if len(categoryMembers(root, owner, "files")) != 2 {
		t.Error("category matching should not depend on the case someone typed")
	}
	if len(categoryMembers(root, owner, "Nothing")) != 0 {
		t.Error("a category no tool claims has no members")
	}
}

// Setting access on the category moves every tool in it, which is the whole
// point of the control.
func TestSettingCategoryScopeMovesEveryMember(t *testing.T) {
	root, owner := categoryFixture(t)

	st, ok := categoryScopeState(root, owner, "Files")
	if !ok {
		t.Fatal("category state not found")
	}
	if !st.Global || st.Custom {
		t.Fatalf("a category whose tools are all user-wide should read Global and not Custom: %+v", st)
	}

	// Off Global, on to one agent — the category as a whole.
	if err := setCategoryScope(root, owner, "Files", "agent-one", true); err != nil {
		t.Fatalf("grant to agent: %v", err)
	}
	if err := setCategoryScope(root, owner, "Files", "global", false); err != nil {
		t.Fatalf("revoke global: %v", err)
	}

	for _, name := range []string{"list_files", "read_file"} {
		ts, ok := toolScopeState(root, owner, name)
		if !ok {
			t.Fatalf("%s lost its scope entirely", name)
		}
		if ts.Global {
			t.Errorf("%s is still user-wide after the category left the pool", name)
		}
		on := false
		for _, a := range ts.Agents {
			if a.ID == "agent-one" {
				on = a.On
			}
		}
		if !on {
			t.Errorf("%s did not follow the category onto agent-one", name)
		}
	}

	// A tool that never claimed the category is untouched — the blast radius of
	// a category is its members and nothing else.
	if ts, ok := toolScopeState(root, owner, "send_mail"); !ok || !ts.Global {
		t.Error("a tool outside the category was moved by the category's scope change")
	}
}

// The behavior the request named: change ONE tool inside a category and the
// category stops having a single answer.
func TestOneToolOutOfStepMakesTheCategoryCustom(t *testing.T) {
	root, owner := categoryFixture(t)

	// Both tools start user-wide. Move ONE of them out, by itself.
	if err := setToolScope(root, owner, "read_file", "agent-one", true); err != nil {
		t.Fatalf("grant tool to agent: %v", err)
	}
	if err := setToolScope(root, owner, "read_file", "global", false); err != nil {
		t.Fatalf("revoke tool global: %v", err)
	}

	st, ok := categoryScopeState(root, owner, "Files")
	if !ok {
		t.Fatal("category state not found")
	}
	if !st.Custom {
		t.Error("a category whose tools disagree must read Custom")
	}
	if st.Global {
		t.Error("Global must be false when only some members are user-wide — reporting it on overstates what the category grants")
	}
	if !st.GlobalPartial {
		t.Error("the Global pill should report the disagreement rather than a flat off")
	}

	// Setting the category settles it, and Custom clears.
	if err := setCategoryScope(root, owner, "Files", "global", true); err != nil {
		t.Fatalf("settle the category: %v", err)
	}
	settled, ok := categoryScopeState(root, owner, "Files")
	if !ok {
		t.Fatal("category state not found after settling")
	}
	if settled.Custom || !settled.Global {
		t.Errorf("setting the category should give every member the same access: %+v", settled)
	}
}

// A per-agent disagreement is reported on that agent's pill, not smeared across
// the whole category.
func TestPartialIsReportedPerAgent(t *testing.T) {
	root, owner := categoryFixture(t)

	// Take the category off Global so per-agent grants are the live question.
	// Demoting a global tool scopes it to every agent that could already see it
	// — which is all of them — so after this both tools are on both agents and
	// the category still agrees with itself.
	if err := setCategoryScope(root, owner, "Files", "global", false); err != nil {
		t.Fatal(err)
	}
	// The disagreement: take ONE member off ONE agent, by itself.
	if err := setToolScope(root, owner, "list_files", "agent-two", false); err != nil {
		t.Fatal(err)
	}

	st, ok := categoryScopeState(root, owner, "Files")
	if !ok {
		t.Fatal("category state not found")
	}
	byID := map[string]ToolScopeAgent{}
	for _, a := range st.Agents {
		byID[a.ID] = a
	}
	if one := byID["agent-one"]; !one.On || one.Partial {
		t.Errorf("agent-one holds every member and should read a plain on: %+v", one)
	}
	if two := byID["agent-two"]; two.On || !two.Partial {
		t.Errorf("agent-two holds one member of two and should read partial, not on: %+v", two)
	}
	if !st.Custom {
		t.Error("a per-agent disagreement makes the category Custom")
	}

	// And settling that one pill brings the category back into agreement —
	// which is what clicking a dashed pill does in the UI.
	if err := setCategoryScope(root, owner, "Files", "agent-two", true); err != nil {
		t.Fatalf("settle agent-two: %v", err)
	}
	if settled, _ := categoryScopeState(root, owner, "Files"); settled.Custom {
		t.Error("clicking a partial pill should give every member that agent, clearing Custom")
	}
}

// An empty category is not an error. It exists, it just has nothing in it, and
// saying "not found" would send someone looking for a category they can see.
func TestEmptyCategoryIsNotAnError(t *testing.T) {
	root, owner := categoryFixture(t)

	st, ok := categoryScopeState(root, owner, "Nothing")
	if !ok {
		t.Fatal("an empty category should resolve, not 404")
	}
	if len(st.Agents) != 0 {
		t.Errorf("an empty category has no targets to configure: %+v", st.Agents)
	}
	if st.Global {
		t.Error("an empty category must not read Global — no tools are granted anywhere")
	}
	if err := setCategoryScope(root, owner, "Nothing", "global", true); err == nil {
		t.Error("setting access on a category with no tools should say so rather than silently doing nothing")
	}
}
