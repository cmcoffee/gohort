package orchestrate

import (
	"context"
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A Builder another agent (or MCP, or a schedule) sends has the app,
// pipeline and machine managers its own chat has. The dispatch paths added
// only the session-built authoring tools, and a sent Builder told the person
// app_def "isn't actually available in this deployment".
func TestADispatchedBuilderCanBuildApps(t *testing.T) {
	T, _, user := newTestOrchestrate(t)
	pinRootDB(t)
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "Done.", Repeat: true}}}
	T.LLM, T.LeadLLM = fake, fake
	if _, err := T.RunAgentSync(context.Background(), user, user, "seed-builder", "Build me a light party app."); err != nil {
		t.Fatal(err)
	}
	if fake.Calls() == 0 {
		t.Fatal("the Builder never ran")
	}
	var offered []string
	for _, tl := range fake.Config(0).Tools {
		offered = append(offered, tl.Name)
	}
	for _, want := range []string{"app_def", "pipeline", "machine"} {
		if !slices.Contains(offered, want) {
			t.Errorf("a dispatched Builder was not offered %s: %v", want, offered)
		}
	}
}

// A Builder run another agent sent cannot save an app whose scripts use an
// API or a tool that agent may not use: the scripts run with the owner's
// credentials. The owner's own run is not held to it.
func TestASentBuilderCannotBuildAnAppAroundItsSender(t *testing.T) {
	udb := delegationFixture(t)
	sess := builderSession(udb, "a-other")
	applyBuilderDelegation(sess, udb)
	sent := &chatTurn{user: "alice", udb: udb, editRefusal: sess.ToolEditRefusal}
	withCaps := func(caps ...string) AppSpec {
		return AppSpec{DataSources: []AppDataSource{{Name: "d", Script: "print(1)", Capabilities: caps}}}
	}
	for _, c := range []string{"fetch_via:weather", "secret:weather", "tool:allowed_kit"} {
		if msg := sent.appDelegationRefusal(withCaps("fetch", c)); !strings.Contains(msg, "NOT DONE") {
			t.Errorf("%s: a sent run may build it into an app: %q", c, msg)
		}
	}
	if msg := sent.appDelegationRefusal(AppSpec{Actions: []AppAction{{Name: "a", Script: "x", Capabilities: []string{"tool:allowed_kit"}}}}); msg == "" {
		t.Error("an action's capabilities were not checked")
	}
	if msg := sent.appDelegationRefusal(withCaps("fetch", "log", "tool:other_kit")); msg != "" {
		t.Errorf("refused what the sender can use: %q", msg)
	}
	owner := &chatTurn{user: "alice", udb: udb}
	if msg := owner.appDelegationRefusal(withCaps("fetch_via:weather", "tool:allowed_kit")); msg != "" {
		t.Errorf("the owner's own run was held to a sender: %q", msg)
	}
}
