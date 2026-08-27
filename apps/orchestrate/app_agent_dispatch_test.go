package orchestrate

import (
	"testing"

	"github.com/cmcoffee/gohort/core/appagents"
)

// TestAppAgentDispatchDefaultsToNone — an app agent is hidden, bound to its
// app's surface, and gets the `agents` grouped tool whether or not its spec's
// AllowedTools names it (it is a framework tool). Left on the ordinary blank
// default it therefore reached every non-hidden agent in the user's fleet, and
// no app author chose that or could see it. Guides' Guide Author was dispatching
// to agents its guide had never attached as Sources.
func TestAppAgentDispatchDefaultsToNone(t *testing.T) {
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-quiet", Name: "Quiet", OwningApp: "Test", Hidden: true,
		Prompt: "x",
	})
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-loud", Name: "Loud", OwningApp: "Test", Hidden: true,
		Prompt: "x", DispatchMode: appagents.DispatchAll,
	})

	// A blank spec means NONE. Asked of a record whose own field is blank too —
	// which is what a per-user shadow saved before the spec grew the field looks
	// like, and the case a spec-only fix would have missed.
	if got := effectiveDispatchMode(AgentRecord{ID: "app-test-quiet"}); got != dispatchNone {
		t.Errorf("app agent with no declared policy = %q, want %q", got, dispatchNone)
	}
	// Reaching the fleet is opt-IN, and the opt-in works.
	if got := effectiveDispatchMode(AgentRecord{ID: "app-test-loud"}); got != dispatchAll {
		t.Errorf("app agent declaring DispatchAll = %q, want %q", got, dispatchAll)
	}
	// The spec is a FALLBACK, never an override: an explicit mode on the record
	// is a decision, whether the owner made it in the editor or the hosting app
	// made it on its per-turn copy (guides binds dispatch to the open guide's
	// attached agent Sources exactly this way).
	rec := AgentRecord{ID: "app-test-quiet", DispatchMode: dispatchOnly, AllowedDispatchTargets: []string{"a1"}}
	if got := effectiveDispatchMode(rec); got != dispatchOnly {
		t.Errorf("host app's per-turn override = %q, want %q — the spec default must not overrule it", got, dispatchOnly)
	}
	// And an ordinary agent is untouched: blank still means the fleet.
	if got := effectiveDispatchMode(AgentRecord{ID: "some-user-agent"}); got != dispatchAll {
		t.Errorf("non-app agent with no policy = %q, want %q — this change must not narrow ordinary agents", got, dispatchAll)
	}
	// The spec's own resolver agrees with what the record path produced.
	if s, ok := appagents.AppAgentByID("app-test-quiet"); !ok || s.EffectiveDispatchMode() != appagents.DispatchNone {
		t.Error("AppAgentSpec.EffectiveDispatchMode disagrees with the record path")
	}
	// The record built from a spec carries the policy, so the editor shows it
	// rather than an empty select that reads as "all".
	if r := appAgentSpecToRecord(appagents.AppAgentSpec{ID: "x", Name: "x"}); r.DispatchMode != dispatchNone {
		t.Errorf("spec-to-record left DispatchMode %q; the editor would render it as the default", r.DispatchMode)
	}
}

// TestUserAgentWinsANameCollision — the app-agent registry is process-global and
// its entries are HIDDEN, so a user naming an agent "Investigator" has no way to
// know one already answers to that. Resolution used to hand back whichever
// record listAgents emitted first, making the answer depend on registration
// order — on which apps are compiled in. A caller asking for the agent the user
// built and named got a hidden framework agent instead, silently.
func TestUserAgentWinsANameCollision(t *testing.T) {
	const name = "Zzcollide Fixture"
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-collide", Name: name, OwningApp: "Test", Hidden: true, Prompt: "x",
	})
	_, udb, user := newTestOrchestrate(t)

	// With no agent of their own by that name, the framework's still resolves —
	// precedence must not become a hard block.
	if a, ok := findAgentByNameOrID(udb, user, name); !ok || a.ID != "app-test-collide" {
		t.Fatalf("framework agent should resolve when nothing else claims the name; got %+v ok=%v", a.ID, ok)
	}

	if _, err := saveAgent(udb, AgentRecord{
		ID: "mine-collide", Name: name, Owner: user, OrchestratorPrompt: "mine",
	}); err != nil {
		t.Fatalf("saving the user's agent: %v", err)
	}

	// Now theirs wins.
	if a, ok := findAgentByNameOrID(udb, user, name); !ok || a.ID != "mine-collide" {
		t.Errorf("user's own agent lost the name they gave it; resolved to %q", a.ID)
	}
	// Case and separator drift resolve to theirs too — precedence applies at
	// every tier, not just the exact one.
	if a, ok := findAgentByNameOrID(udb, user, "zzcollide fixture"); !ok || a.ID != "mine-collide" {
		t.Errorf("case-insensitive lookup resolved to %q, want the user's own", a.ID)
	}
	if a, ok := findAgentByNameOrID(udb, user, "zzcollide-fixture"); !ok || a.ID != "mine-collide" {
		t.Errorf("slug-tolerant lookup resolved to %q, want the user's own", a.ID)
	}
	// An explicit ID still addresses exactly what it names — precedence is a
	// tie-break on NAMES and must never override an id.
	if a, ok := findAgentByNameOrID(udb, user, "app-test-collide"); !ok || a.ID != "app-test-collide" {
		t.Errorf("lookup by id resolved to %q; an id is not ambiguous", a.ID)
	}
}

// TestFrameworkSplitKeysOnIDNotOwner — a customized seed carries a per-user
// shadow whose Owner IS the user. Splitting on Owner would call it theirs and
// hand a shadowed framework agent the same precedence as one they built.
func TestFrameworkSplitKeysOnIDNotOwner(t *testing.T) {
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-shadowed", Name: "Shadowed", OwningApp: "Test", Hidden: true, Prompt: "x",
	})
	own, framework := splitOwnAndFrameworkAgents([]AgentRecord{
		{ID: "app-test-shadowed", Name: "Shadowed", Owner: "alice"}, // shadow: owner looks like the user
		{ID: "user-made", Name: "Mine", Owner: "alice"},
	})
	if len(framework) != 1 || framework[0].ID != "app-test-shadowed" {
		t.Errorf("a shadowed app agent must count as framework, got %+v", framework)
	}
	if len(own) != 1 || own[0].ID != "user-made" {
		t.Errorf("own = %+v, want just the user-made agent", own)
	}
}
