package orchestrate

// An agent intake holds create_agent and update_agent until agent_recipe has
// run, and the recipe call reads the request and, on a change, the agent as it
// stands with its sub-agents.

import (
	"context"
	"errors"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// recipeStub answers every shape with reply and keeps what it was sent.
type recipeStub struct {
	reply    string
	err      error
	input    string
	system   string
	routeKey string
}

func (s *recipeStub) chat(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
	var cfg ChatConfig
	for _, o := range opts {
		o(&cfg)
	}
	s.system, s.routeKey = cfg.SystemPrompt, cfg.RouteKey
	if len(msgs) > 0 {
		s.input = msgs[len(msgs)-1].Content
	}
	if s.err != nil {
		return nil, s.err
	}
	return &Response{Content: s.reply}, nil
}

func builderIntakeTurn(session string, vals map[string]string) *chatTurn {
	return &chatTurn{user: "u", agent: AgentRecord{ID: "seed-builder"},
		session: &ChatSession{ID: session, Messages: []ChatMessage{{Role: "user", IntakeValues: vals}}}}
}

func TestAnAgentIntakeHoldsTheBuildUntilItHasARecipe(t *testing.T) {
	t.Cleanup(func() {
		for _, s := range []string{"recipe-create", "recipe-app", "recipe-none"} {
			builderRecipeStates.Delete("u\x00" + s)
		}
	})
	turn := builderIntakeTurn("recipe-create", map[string]string{"action": "Create", "kind": "Agent", "goal": "a morning digest of our team news"})
	turn.noteRecipeRequirement()
	sess := &ToolSession{Username: "u", ChatSessionID: "recipe-create"}
	if msg := recipeGateRefusal(sess, "create_agent"); !strings.Contains(msg, "agent_recipe") {
		t.Fatalf("an agent intake let create_agent run without a recipe: %q", msg)
	}
	// Through the tool itself, before anything is saved.
	if _, err := (createAgentTool{}).RunWithSession(map[string]any{"name": "Digest"}, &ToolSession{Username: "u", ChatSessionID: "recipe-create", DB: scopeStores(t)}); err == nil || !strings.Contains(err.Error(), "agent_recipe") {
		t.Fatalf("create_agent was not held for the recipe: %v", err)
	}

	stub := &recipeStub{reply: "Recipe: one specialist that posts a digest.\nAgents:\n- Digest (specialist): posts. Reaches: the news.\nWiring: a schedule and a board\nAsk the person: which sources"}
	out, err := runAgentRecipe(context.Background(), sess, stub.chat, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stub.input, "a morning digest of our team news") || !strings.Contains(stub.input, "CREATE AN AGENT") {
		t.Errorf("the recipe call was not given the intake's request: %q", stub.input)
	}
	if stub.routeKey != builderRecipeRouteKey || !strings.Contains(stub.system, "Split a part off") {
		t.Errorf("the recipe call carried route %q and brief %.60q", stub.routeKey, stub.system)
	}
	if !strings.HasPrefix(out, "Proposed recipe:") || !strings.Contains(out, "Show it to the person") {
		t.Errorf("the shape came back as %q", out)
	}
	if msg := recipeGateRefusal(sess, "create_agent"); msg != "" {
		t.Errorf("still held after the recipe: %q", msg)
	}

	// A second intake in the same session is a new build, shaped again.
	turn.session.Messages = append(turn.session.Messages, ChatMessage{Role: "user", IntakeValues: map[string]string{"action": "Create", "kind": "Agent", "goal": "something else"}})
	turn.noteRecipeRequirement()
	if recipeGateRefusal(sess, "create_agent") == "" {
		t.Error("a new agent intake got no new recipe")
	}

	// Other intakes, and sessions with none, are not held.
	builderIntakeTurn("recipe-app", map[string]string{"action": "Create", "kind": "App", "goal": "weather"}).noteRecipeRequirement()
	if msg := recipeGateRefusal(&ToolSession{Username: "u", ChatSessionID: "recipe-app"}, "create_agent"); msg != "" {
		t.Errorf("an app intake held an agent create: %q", msg)
	}
	none := &chatTurn{user: "u", agent: AgentRecord{ID: "seed-builder"}, session: &ChatSession{ID: "recipe-none", Messages: []ChatMessage{{Role: "user", Content: "make me an agent"}}}}
	none.noteRecipeRequirement()
	if msg := recipeGateRefusal(&ToolSession{Username: "u", ChatSessionID: "recipe-none"}, "create_agent"); msg != "" {
		t.Errorf("a session with no intake was held: %q", msg)
	}
}

// On a change the recipe call sees the agent with its sub-agents, and "No shape
// change." goes back to Builder as a go-ahead.
func TestTheRecipeSeesTheAgentBeingChanged(t *testing.T) {
	depStores(t, "u")
	t.Cleanup(func() { builderRecipeStates.Delete("u\x00recipe-change") })
	udb := UserDB(orchestrateBaseDB, "u")
	wren, err := saveAgent(udb, AgentRecord{Name: "Wren", Owner: "u", OrchestratorPrompt: "p", MemoryMode: "chatbot", AllowedTools: []string{"not_a_tool_here"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := saveAgent(udb, AgentRecord{Name: "Comedian", Owner: "u", OrchestratorPrompt: "p", OwnedBy: wren.ID, Hidden: true, AllowedTools: []string{"generate_image"}}); err != nil {
		t.Fatal(err)
	}
	turn := builderIntakeTurn("recipe-change", map[string]string{"action": "Change", "kind": "Agent", "target": wren.ID, "change": "make its jokes funnier"})
	turn.noteRecipeRequirement()
	sess := &ToolSession{Username: "u", ChatSessionID: "recipe-change", DB: udb}
	if msg := recipeGateRefusal(sess, "update_agent"); !strings.Contains(msg, "being changed") {
		t.Fatalf("a change intake did not hold update_agent: %q", msg)
	}

	stub := &recipeStub{reply: "No shape change. A prompt change fits what it already does."}
	out, err := runAgentRecipe(context.Background(), sess, stub.chat, "", "")
	if err != nil {
		t.Fatal(err)
	}
	// Wren's one tool is not registered here, so it was healed to none.
	for _, want := range []string{"- Wren (assistant)", "tools: none", "- sub-agent Comedian (specialist)", "tools: generate_image", "make its jokes funnier"} {
		if !strings.Contains(stub.input, want) {
			t.Errorf("the recipe call's view of the agent lacks %q:\n%s", want, stub.input)
		}
	}
	if !strings.HasPrefix(out, "No shape change.") || !strings.Contains(out, "Go ahead") {
		t.Errorf("no-change came back as %q", out)
	}
	if msg := recipeGateRefusal(sess, "update_agent"); msg != "" {
		t.Errorf("still held after the recipe: %q", msg)
	}
}

// A recipe call that cannot answer does not hold the build up: it is advice.
func TestARecipeThatCannotBeWrittenDoesNotHoldTheBuild(t *testing.T) {
	t.Cleanup(func() { builderRecipeStates.Delete("u\x00recipe-down") })
	builderIntakeTurn("recipe-down", map[string]string{"action": "Create", "kind": "Agent", "goal": "an analyst"}).noteRecipeRequirement()
	sess := &ToolSession{Username: "u", ChatSessionID: "recipe-down"}
	out, err := runAgentRecipe(context.Background(), sess, (&recipeStub{err: errors.New("lead unreachable")}).chat, "", "")
	if err != nil || !strings.Contains(out, "Decide the shape yourself") {
		t.Fatalf("a failed recipe returned %q, %v", out, err)
	}
	if msg := recipeGateRefusal(sess, "create_agent"); msg != "" {
		t.Errorf("a failed recipe still held the build: %q", msg)
	}
}

// The intake's packed text is read when no values were stored, every field
// whole, including a textarea's several lines.
func TestLatestIntakeReadsThePackedText(t *testing.T) {
	form := IntakeFormSpec{{Name: "action", Label: "What do you want to do?"}, {Name: "kind", Label: "What kind of thing?"}, {Name: "goal", Label: "What should it do?"}}
	turn := &chatTurn{agent: AgentRecord{ID: "seed-builder", IntakeForm: form}, session: &ChatSession{Messages: []ChatMessage{
		{Role: "user", Content: "**What do you want to do?:** Create\n\n**What kind of thing?:** Agent\n\n**What should it do?:** read the exports\nand say what changed"},
	}}}
	vals, from := turn.latestIntake()
	if from != "text" || vals["action"] != "Create" || vals["kind"] != "Agent" || vals["goal"] != "read the exports\nand say what changed" {
		t.Fatalf("read %v from %q", vals, from)
	}
}
