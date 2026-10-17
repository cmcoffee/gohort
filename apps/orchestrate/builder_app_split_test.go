package orchestrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func splitTurn(t *testing.T, agent AgentRecord) (*chatTurn, *FakeLLM) {
	t.Helper()
	app, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "Built weather: the page and its forecast script. Verify passed.", Repeat: true}}}
	app.LLM, app.LeadLLM = fake, fake
	return &chatTurn{app: app, agent: agent, user: user, udb: udb, ctx: context.Background()}, fake
}

var designingBuilder = AgentRecord{ID: "seed-builder", Name: "Builder", LeadModel: true, LeadPlans: true}

// On Builder, "lead plans" means the lead designs and reviews apps and a
// worker writes them: app_def keeps its reading actions and refuses every
// change, code_app is added, and the lead is told how an app gets made. The
// rest of the catalog is untouched, and nothing changes for another agent
// or for Builder on "lead for every turn".
func TestBuilderDesignsOnTheLeadAndAWorkerWritesTheApp(t *testing.T) {
	turn, _ := splitTurn(t, designingBuilder)
	pr := &planRun{t: turn, allTools: []AgentToolDef{turn.appDefToolDef(), {Tool: Tool{Name: "tool_def"}}}}
	pr.splitForBuilderApps()
	var names []string
	var appDef AgentToolDef
	for _, td := range pr.allTools {
		names = append(names, td.Tool.Name)
		if td.Tool.Name == "app_def" {
			appDef = td
		}
	}
	if !slices.Contains(names, codeAppTool) || !slices.Contains(names, "tool_def") {
		t.Fatalf("the lead was offered %v", names)
	}
	for _, action := range []string{"create", "update", "update_section", "patch_html", "replace_function", "checkout", "publish", "revert"} {
		_, err := appDef.Handler(context.Background(), map[string]any{"action": action, "id": "weather"})
		if err == nil || !strings.Contains(err.Error(), "code_app") {
			t.Errorf("app_def %s on the lead: %v", action, err)
		}
	}
	if _, err := appDef.Handler(context.Background(), map[string]any{"action": "get", "id": "weather"}); err != nil && strings.Contains(err.Error(), "code_app") {
		t.Errorf("reading an app was refused: %v", err)
	}
	if !strings.Contains(pr.sys, "a worker writes them") || !strings.Contains(pr.sys, "Review the code") {
		t.Error("the lead was not told how an app gets made")
	}

	for name, agent := range map[string]AgentRecord{
		"Builder on lead for every turn": {ID: "seed-builder", LeadModel: true},
		"another agent that plans":       {ID: "a1", LeadModel: true, LeadPlans: true},
	} {
		turn, _ := splitTurn(t, agent)
		pr := &planRun{t: turn, allTools: []AgentToolDef{turn.appDefToolDef()}}
		pr.splitForBuilderApps()
		if len(pr.allTools) != 1 || pr.sys != "" {
			t.Errorf("%s: the catalog changed", name)
		}
	}
	// A Private turn cannot reach the lead, so there is nothing to split.
	turn.privateMode = true
	if turn.builderSplitsApps(false) {
		t.Error("a Private turn split")
	}
}

// code_app runs the coder as a worker step that has app_def, with the design
// as its brief and the person's message beside it, and caps a turn at four
// builds. An ordinary Builder step does not get app_def.
func TestCodeAppGivesTheWorkerTheDesignAndAppDef(t *testing.T) {
	turn, fake := splitTurn(t, designingBuilder)
	td := turn.codeAppToolDef(func() string { return "build me a weather app" })
	out, err := td.Handler(context.Background(), map[string]any{
		"title": "Build the weather app", "design": "A page showing today's forecast; data source forecast returns {\"temp\": number}.",
	})
	if err != nil {
		t.Fatal(err)
	}
	var offered []string
	for _, tl := range fake.Config(0).Tools {
		offered = append(offered, tl.Name)
	}
	if !slices.Contains(offered, "app_def") {
		t.Errorf("the coder was not given app_def: %v", offered)
	}
	if slices.Contains(offered, codeAppTool) || slices.Contains(offered, "ask_user") {
		t.Errorf("the coder was given the lead's tools: %v", offered)
	}
	sys, prompt := fake.Config(0).SystemPrompt, fake.Prompt(0)
	if !strings.Contains(sys, "## The design") || !strings.Contains(sys, `{\"temp\": number}`) && !strings.Contains(sys, `{"temp": number}`) {
		t.Errorf("the design is not the coder's brief:\n%.800s", sys)
	}
	if !strings.Contains(prompt, "build me a weather app") {
		t.Error("the coder does not see the person's message")
	}
	if th := fake.Config(0).Think; th == nil || !*th {
		t.Error("the coder does not think")
	}
	if !strings.Contains(out, "Built weather") || !strings.Contains(out, "No app changed") || !strings.Contains(out, "Build 1 of 4") {
		t.Errorf("the lead's result:\n%s", out)
	}
	for i := 2; i <= maxAppBuilds; i++ {
		if _, err := td.Handler(context.Background(), map[string]any{"design": "1. Fix the header."}); err != nil {
			t.Fatalf("build %d: %v", i, err)
		}
	}
	if _, err := td.Handler(context.Background(), map[string]any{"design": "1. One more."}); err == nil || !strings.Contains(err.Error(), "used its 4 builds") {
		t.Errorf("a fifth build ran: %v", err)
	}
	if _, err := td.Handler(context.Background(), map[string]any{"design": "  "}); err == nil {
		t.Error("a build with no design ran")
	}

	plain, fake2 := splitTurn(t, designingBuilder)
	if _, err := plain.runWorkerStep(nil, PlanStep{ID: 1, Title: "Look something up", WorkerBrief: "Find the API's base URL."}, "x", nil); err != nil {
		t.Fatal(err)
	}
	for _, tl := range fake2.Config(0).Tools {
		if tl.Name == "app_def" {
			t.Error("an ordinary Builder step was given app_def")
		}
	}
	if th := fake2.Config(0).Think; th != nil && *th {
		t.Error("an ordinary step started thinking")
	}
}

// What the lead reads back about the store does not depend on the worker's
// report: a new app and an app at a new revision are listed with their
// verify state, an untouched one is not.
func TestCodeAppReportsWhatTheStoreSaysChanged(t *testing.T) {
	pinRootDB(t)
	secs, _ := json.Marshal([]any{map[string]any{"kind": "html", "id": "page", "html": "<p>x</p>"}})
	SaveAppSpec(AppSpec{Owner: "u", Slug: "kept", Name: "Kept", Sections: secs})
	SaveAppSpec(AppSpec{Owner: "u", Slug: "edited", Name: "Edited", Sections: secs})
	before := appRevisions("u")
	spec, _ := LoadAppSpec("u", "edited")
	spec.Desc = "now with a forecast"
	SaveAppSpec(spec)
	SaveAppSpec(AppSpec{Owner: "u", Slug: "fresh", Name: "Fresh", Sections: secs})
	var got []string
	for _, s := range appsChangedSince("u", before) {
		got = append(got, s.Slug)
	}
	if strings.Join(got, " ") != "edited fresh" {
		t.Fatalf("changed: %v", got)
	}
	out := codeAppResult("I built it and it passed.", appsChangedSince("u", before), 2)
	if !strings.Contains(out, "- fresh (\"Fresh\")") || !strings.Contains(out, "never verified") || strings.Contains(out, "kept") {
		t.Errorf("the result:\n%s", out)
	}
}

// The choice is offered on Builder under its own name, and it survives the
// boot that rebuilds Builder from code over its saved settings.
func TestBuilderOffersAndKeepsTheDesignSplit(t *testing.T) {
	pinRootDB(t)
	label := func(rec AgentRecord) string {
		for _, o := range leadUseField(true, rec).Options {
			if o.Value == leadUsePlan {
				return o.Label
			}
		}
		return ""
	}
	if l := label(AgentRecord{ID: "seed-builder"}); !strings.Contains(l, "Lead designs apps") {
		t.Errorf("Builder offers %q", l)
	}
	if l := label(AgentRecord{ID: "a1"}); !strings.Contains(l, "Lead plans") {
		t.Errorf("another agent offers %q", l)
	}
	seed := AgentRecord{ID: "seed-builder"}
	applyBuilderDeploymentState(&seed, AgentRecord{ID: "seed-builder", LeadModel: true, LeadPlans: true})
	if !seed.LeadModel || !seed.LeadPlans {
		t.Errorf("rebuilt from code, Builder lost the choice: lead=%v plans=%v", seed.LeadModel, seed.LeadPlans)
	}
}

// A whole Builder chat turn in the mode: the lead's round has code_app and an
// app_def that only reads, it hands the design to code_app, the coder's round
// has app_def and not code_app, and the lead writes the reply after.
func TestABuilderTurnDesignsThenHandsTheCodeToAWorker(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	fake := &FakeLLM{Turns: []FakeTurn{
		{ToolCalls: []ToolCall{{ID: "c1", Name: codeAppTool, Args: map[string]any{
			"title": "Build the weather app", "design": "A page with today's forecast from data source forecast, which returns {\"temp\": number}."}}}},
		{Content: "Built weather; verify passed."},
		{Content: "Your weather app is ready.", Repeat: true},
	}}
	T.LLM, T.LeadLLM = fake, fake
	a, err := saveAgent(udb, AgentRecord{ID: "seed-builder", Owner: user, Name: "Builder", OrchestratorPrompt: "Build what is asked.",
		LeadModel: true, LeadPlans: true})
	if err != nil {
		t.Fatal(err)
	}
	if !isBuilderAgent(a.ID) {
		t.Skipf("saveAgent gave Builder the id %q", a.ID)
	}
	body, _ := json.Marshal(map[string]any{"message": "build me a weather app"})
	w := httptest.NewRecorder()
	T.handleSend(w, httptest.NewRequest(http.MethodPost, "/api/send?agent_id="+a.ID, strings.NewReader(string(body))), udb, user, a)
	if fake.Calls() < 3 {
		t.Fatalf("calls=%d: %s", fake.Calls(), w.Body.String())
	}
	names := func(i int) (out []string) {
		for _, tl := range fake.Config(i).Tools {
			out = append(out, tl.Name)
		}
		return
	}
	if lead := names(0); !slices.Contains(lead, codeAppTool) || !slices.Contains(lead, "app_def") {
		t.Errorf("the lead's round was offered %v", lead)
	}
	if coder := names(1); !slices.Contains(coder, "app_def") || slices.Contains(coder, codeAppTool) {
		t.Errorf("the coder's round was offered %v", coder)
	}
	if !strings.Contains(fake.Config(1).SystemPrompt, "## The design") || !strings.Contains(fake.Prompt(1), "build me a weather app") {
		t.Error("the coder did not get the design and the person's message")
	}
	// The lead's next round: after the coder (and the reply check on the
	// coder's answer), the call that carries the lead's catalog again.
	next := -1
	for i := 2; i < fake.Calls(); i++ {
		if slices.Contains(names(i), codeAppTool) {
			next = i
			break
		}
	}
	if next < 0 {
		t.Fatal("the lead never had another round")
	}
	var result string
	for _, m := range fake.Sent(next) {
		for _, r := range m.ToolResults {
			result += r.Content
		}
	}
	if !strings.Contains(result, "Built weather; verify passed.") || !strings.Contains(result, "No app changed") {
		t.Errorf("the lead did not get the worker's report and the store's word back: %q", result)
	}
}
