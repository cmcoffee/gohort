package orchestrate

import (
	"context"
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/toolscript"
)

// A Builder another agent (or MCP, or a schedule) sends has the app,
// pipeline and machine managers its own chat has. The dispatch paths added
// only the session-built authoring tools, and a sent Builder told the person
// app_def "isn't actually available in this deployment". Machine is one of
// the less-used tools its own chat keeps behind load_tool, so a sent Builder
// is offered it the same way: indexed in its prompt, not inline.
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
	// The first call with tools is Builder's own: before it, the question of
	// whether the sent job is an app job (builder_app_mode.go).
	turn := 0
	for i := 0; i < fake.Calls(); i++ {
		if len(fake.Config(i).Tools) > 0 {
			turn = i
			break
		}
	}
	var offered []string
	for _, tl := range fake.Config(turn).Tools {
		offered = append(offered, tl.Name)
	}
	for _, want := range []string{"app_def", "pipeline", "load_tool"} {
		if !slices.Contains(offered, want) {
			t.Errorf("a dispatched Builder was not offered %s: %v", want, offered)
		}
	}
	if slices.Contains(offered, "machine") {
		t.Error("machine rode inline on a sent Builder: its own chat keeps it behind load_tool")
	}
	if p := fake.Config(turn).SystemPrompt + fake.Prompt(turn); !strings.Contains(p, "load before use") || !strings.Contains(p, "- `machine` ") {
		t.Error("a sent Builder's prompt does not index machine to load")
	}
}

// A Builder run another agent sent cannot save an app whose scripts use an
// API or a tool that agent may not use: the scripts run with the owner's
// credentials. The owner's own run is not held to it.
func TestASentBuilderCannotBuildAnAppAroundItsSender(t *testing.T) {
	udb := delegationFixture(t)
	sess := builderSession(udb, "a-other")
	applyBuilderDelegation(sess, udb, ownerJob{})
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

// Builder's tool_script reaches app_def for its reading actions only: each
// runs and changes nothing, a write is refused, and the script is offered to
// an author alone, and not on a measure run without it.
func TestBuildersScriptReadsOnlyReadingActions(t *testing.T) {
	turn, _ := folderTestTurn(t)
	turn.agent.ID = "seed-builder"
	SaveAppSpec(AppSpec{Owner: "u", Slug: "books", Name: "Books", RecordKey: "id"})
	before, _ := LoadAppSpec("u", "books")

	def := turn.appDefToolDef()
	view := toolscript.Reads(def, builderScriptReads...)
	for _, args := range []map[string]any{
		{"action": "get", "id": "books"}, {"action": "list"}, {"action": "stores"}, {"action": "revisions", "id": "books"},
	} {
		if _, err := view.Handler(context.Background(), args); err != nil {
			t.Errorf("%v: %v", args["action"], err)
		}
	}
	if after, _ := LoadAppSpec("u", "books"); after.Updated != before.Updated {
		t.Errorf("a reading action changed the app: %v -> %v", before.Updated, after.Updated)
	}
	if _, err := view.Handler(context.Background(), map[string]any{"action": "update", "id": "books", "name": "Renamed"}); err == nil || !strings.Contains(err.Error(), "reading actions only") {
		t.Errorf("a write through the view: %v", err)
	}
	if after, _ := LoadAppSpec("u", "books"); after.Name != "Books" {
		t.Errorf("the refused write landed: %q", after.Name)
	}

	has := func(tools []AgentToolDef) bool {
		return slices.ContainsFunc(tools, func(td AgentToolDef) bool { return td.Tool.Name == toolscript.Name })
	}
	if !has(turn.withToolScript([]AgentToolDef{def})) {
		t.Error("Builder was not offered tool_script")
	}
	turn.agent.ID = "someone-else"
	if has(turn.withToolScript([]AgentToolDef{def})) {
		t.Error("an agent that does not author was offered tool_script")
	}
	turn.agent.ID = "seed-builder"
	if has(turn.withToolScript(nil)) {
		t.Error("tool_script offered with nothing for it to call")
	}
	turn.session = &ChatSession{ID: "measure-noscript"}
	measureWithoutScript.Store("measure-noscript", true)
	t.Cleanup(func() { measureWithoutScript.Delete("measure-noscript") })
	if has(turn.withToolScript([]AgentToolDef{def})) {
		t.Error("a measure run without tool_script was offered it")
	}
}
