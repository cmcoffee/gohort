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

// A lead that plans keeps the control and conversation tools and loses the
// work tools, which are listed for its steps instead; plan_set is reworded
// for it; and a work tool named anyway does not resolve.
func TestAPlanningLeadHasNoWorkTools(t *testing.T) {
	td := func(name, desc string) AgentToolDef { return AgentToolDef{Tool: Tool{Name: name, Description: desc}} }
	pr := &planRun{t: &chatTurn{agent: AgentRecord{ID: "a1", LeadModel: true, LeadPlans: true}}, leadPlansOn: true, maxSteps: 5}
	planSet := pr.planSetToolDef()
	pr.cat.controlTools = []AgentToolDef{planSet, td("ask_user", "Ask.")}
	pr.cat.knowTools = []AgentToolDef{td("recall", "Recall."), td("load_tool", "Load."), td("find_tools", "Find.")}
	pr.allTools = append(append(append([]AgentToolDef{}, pr.cat.controlTools...), pr.cat.knowTools...),
		td("web_search", "Search the web. Returns results."), td("search_build_logs", "Search the build logs store."))
	pr.splitForLeadPlans()

	var kept []string
	for _, x := range pr.allTools {
		kept = append(kept, x.Tool.Name)
	}
	if strings.Join(kept, ",") != "plan_set,ask_user,recall" {
		t.Errorf("the lead keeps %v", kept)
	}
	if len(pr.delegable) != 2 {
		t.Errorf("listed for steps: %v", pr.delegable)
	}
	for _, want := range []string{"You plan; workers do the work", "- search_build_logs: Search the build logs store.", "- web_search: Search the web."} {
		if !strings.Contains(pr.sys, want) {
			t.Errorf("the prompt is missing %q:\n%s", want, pr.sys)
		}
	}
	if strings.Contains(pr.sys, "Returns results") {
		t.Error("a step's tool is listed past its first sentence")
	}
	ps := pr.allTools[0].Tool
	if strings.Contains(ps.Description, "CALL THE TOOL DIRECTLY") || !strings.Contains(ps.Description, "One step is enough") ||
		!strings.Contains(ps.Parameters["steps"].Items.Properties["tools"].Description, "Tools a step can be given") {
		t.Errorf("plan_set for a planning lead: %q", ps.Description)
	}
	// The original definition is untouched (its params are not shared).
	if !strings.Contains(planSet.Tool.Parameters["steps"].Items.Properties["tools"].Description, "YOUR catalog") {
		t.Error("rewording plan_set changed the shared definition")
	}
	if pr.toolFallback() != nil || pr.dynamicTools() != nil {
		t.Error("a planning lead can still reach a work tool by naming it")
	}

	// A one-step plan is accepted from a planning lead.
	pr.orchCtx, pr.cancelOrch = t.Context(), func() {}
	if _, err := pr.allTools[0].Handler(t.Context(), map[string]any{"steps": []any{
		map[string]any{"title": "Search the logs", "intent": "Find the error", "worker_brief": "Search the build logs for the error.", "tools": []any{"search_build_logs"}},
	}}); err != nil || len(pr.capturedSteps) != 1 {
		t.Errorf("a one-step plan: %v %d", err, len(pr.capturedSteps))
	}
}

// Without LeadPlans, or on a turn that may not reach the lead, nothing changes.
func TestPlanningOnTheLeadNeedsTheLead(t *testing.T) {
	for name, tc := range map[string]struct {
		agent   AgentRecord
		private bool
		want    bool
	}{
		"plans":          {AgentRecord{ID: "a1", LeadModel: true, LeadPlans: true}, false, true},
		"lead only":      {AgentRecord{ID: "a1", LeadModel: true}, false, false},
		"plans, no lead": {AgentRecord{ID: "a1", LeadPlans: true}, false, false},
		"private turn":   {AgentRecord{ID: "a1", LeadModel: true, LeadPlans: true}, true, false},
		"builder":        {AgentRecord{ID: "seed-builder", LeadModel: true, LeadPlans: true}, false, false},
		"work plan":      {AgentRecord{ID: "a1", LeadModel: true, LeadPlans: true, WorkPlan: true}, false, false},
	} {
		pr := &planRun{t: &chatTurn{agent: tc.agent, privateMode: tc.private}}
		pr.decideLeadPlans()
		if pr.leadPlansOn != tc.want {
			t.Errorf("%s: plans=%v, want %v", name, pr.leadPlansOn, tc.want)
		}
		pr.allTools = []AgentToolDef{{Tool: Tool{Name: "web_search"}}}
		pr.splitForLeadPlans()
		if !tc.want && len(pr.allTools) != 1 {
			t.Errorf("%s: the catalog changed", name)
		}
	}
}

// A plan step gets the agent's attached sources: a step asked to search a
// store had no search_<store>, and a lead that plans has no other way in.
func TestAPlanStepGetsTheAgentsAttachedSources(t *testing.T) {
	RegisterReferenceSource(fakeRefSource{kind: "testfiles", label: "Test files"})
	app, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "Found the error in the nightly capture.", Repeat: true}}}
	app.LLM, app.LeadLLM = fake, fake
	turn := &chatTurn{app: app, agent: AgentRecord{ID: "a1", Owner: user, Name: "Investigator",
		AttachedSources: []ReferenceSelection{{Kind: "testfiles", ItemID: "prod"}}},
		user: user, udb: udb, ctx: context.Background()}
	out, err := turn.runWorkerStep(nil, PlanStep{ID: 1, Title: "Search the logs", WorkerBrief: "Search the nightly capture for the error.",
		Tools: []string{"search_prod_logs"}}, "What failed last night?", nil)
	if err != nil || !strings.Contains(out, "Found the error") {
		t.Fatalf("the step: %q %v", out, err)
	}
	var offered []string
	for _, tl := range fake.Config(0).Tools {
		offered = append(offered, tl.Name)
	}
	if !slices.Contains(offered, "search_prod_logs") || slices.Contains(offered, "read_prod_file") {
		t.Errorf("the step was offered %v", offered)
	}
}

// A whole planning turn on the chat path: the lead's round carries no work
// tools and commits a one-step plan, the step runs with the store's search
// tool, and the reply is written from what the step found.
func TestAPlanningTurnEndToEnd(t *testing.T) {
	RegisterReferenceSource(fakeRefSource{kind: "testfiles", label: "Test files"})
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	fake := &FakeLLM{Turns: []FakeTurn{
		{ToolCalls: []ToolCall{{ID: "p1", Name: "plan_set", Args: map[string]any{"steps": []any{map[string]any{
			"title": "Search the logs", "intent": "Find what failed", "worker_brief": "Search the nightly capture for the failure.",
			"tools": []any{"search_prod_logs"}}}}}}},
		{Content: "The nightly build failed on a full disk."},
		{Content: "Last night's build failed because the disk filled up.", Repeat: true},
	}}
	T.LLM, T.LeadLLM = fake, fake
	a, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Investigator", OrchestratorPrompt: "Explain failures.",
		LeadModel: true, LeadPlans: true, AttachedSources: []ReferenceSelection{{Kind: "testfiles", ItemID: "prod"}}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"message": "What failed last night?"})
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
	if lead := names(0); !slices.Contains(lead, "plan_set") || slices.Contains(lead, "search_prod_logs") {
		t.Errorf("the lead's round was offered %v", lead)
	}
	if !strings.Contains(fake.Config(0).SystemPrompt, "- search_prod_logs") {
		t.Error("the lead was not told what a step can use")
	}
	if step := names(1); !slices.Contains(step, "search_prod_logs") {
		t.Errorf("the step was offered %v", step)
	}
	if !strings.Contains(fake.Prompt(2), "full disk") || !strings.Contains(w.Body.String(), "disk filled up") {
		t.Errorf("the reply was not written from the step: %s", w.Body.String())
	}
}

// A dispatched run of an agent that plans on the lead: the lead has
// work_step and none of the work tools, a step runs on the worker with the
// tool the lead named, and the lead writes the reply from what it returned.
func TestADispatchedRunPlansOnTheLead(t *testing.T) {
	RegisterReferenceSource(fakeRefSource{kind: "testfiles", label: "Test files"})
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	fake := &FakeLLM{Turns: []FakeTurn{
		{ToolCalls: []ToolCall{{ID: "w1", Name: "work_step", Args: map[string]any{
			"title": "Search the logs", "brief": "Search the nightly capture for the failure.", "tools": []any{"search_prod_logs"}}}}},
		{Content: "The nightly build failed on a full disk."},
		{Content: "Last night's build failed because the disk filled up.", Repeat: true},
	}}
	T.LLM, T.LeadLLM = fake, fake
	a, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Investigator", OrchestratorPrompt: "Explain failures.",
		LeadModel: true, LeadPlans: true, AttachedSources: []ReferenceSelection{{Kind: "testfiles", ItemID: "prod"}}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := T.RunAgentSync(context.Background(), user, user, a.ID, "What failed last night?")
	if err != nil {
		t.Fatal(err)
	}
	names := func(i int) (n []string) {
		for _, tl := range fake.Config(i).Tools {
			n = append(n, tl.Name)
		}
		return
	}
	if lead := names(0); !slices.Contains(lead, "work_step") || slices.Contains(lead, "search_prod_logs") {
		t.Errorf("the lead was offered %v", lead)
	}
	if !strings.Contains(fake.Config(0).SystemPrompt, "hand it to work_step") || !strings.Contains(fake.Config(0).SystemPrompt, "- search_prod_logs") {
		t.Error("the lead was not told how this run works")
	}
	if step := names(1); len(step) != 1 || step[0] != "search_prod_logs" {
		t.Errorf("the step was offered %v", step)
	}
	if rk := fake.Config(1).RouteKey; rk != "app.orchestrate.worker" {
		t.Errorf("the step ran on route %q, not the worker's", rk)
	}
	if rk := fake.Config(0).RouteKey; !strings.Contains(rk, "lead") {
		t.Errorf("the planning round ran on route %q, not the lead's", rk)
	}
	// The step's answer reached the lead as work_step's result.
	sawStep := false
	for _, m := range fake.Sent(2) {
		for _, tr := range m.ToolResults {
			if strings.Contains(tr.Content, "full disk") {
				sawStep = true
			}
		}
	}
	if !sawStep || !strings.Contains(out, "disk filled up") {
		t.Errorf("the reply was not written from the step: %q (step result reached the lead: %v)", out, sawStep)
	}

	// The same agent with only the lead keeps its tools on the lead.
	a.LeadPlans = false
	saveAgent(udb, a)
	fake2 := &FakeLLM{Turns: []FakeTurn{{Content: "Done.", Repeat: true}}}
	T.LLM, T.LeadLLM = fake2, fake2
	if _, err := T.RunAgentSync(context.Background(), user, user, a.ID, "What failed last night?"); err != nil {
		t.Fatal(err)
	}
	var lead []string
	for _, tl := range fake2.Config(0).Tools {
		lead = append(lead, tl.Name)
	}
	if slices.Contains(lead, "work_step") || !slices.Contains(lead, "search_prod_logs") {
		t.Errorf("lead for every turn was offered %v", lead)
	}
}

// work_step refuses a tool it was not given (without spending a step), and
// stops at the step limit.
func TestWorkStepKeepsToItsTools(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "found it", Repeat: true}}}
	T.LLM, T.LeadLLM = fake, fake
	turn := &chatTurn{app: T, agent: AgentRecord{ID: "a1", Owner: user, MaxPlanSteps: 1}, user: user, udb: udb, ctx: context.Background()}
	ws := turn.workStepToolDef("q", []AgentToolDef{{Tool: Tool{Name: "web_search"}}}, nil)
	call := func(tool string) (string, error) {
		return ws.Handler(context.Background(), map[string]any{"title": "x", "brief": "y", "tools": []any{tool}})
	}
	if _, err := call("drop_table"); err == nil || !strings.Contains(err.Error(), "web_search") {
		t.Errorf("an ungiven tool: %v", err)
	}
	if out, err := call("web_search"); err != nil || out != "found it" {
		t.Errorf("the one step: %q %v", out, err)
	}
	if _, err := call("web_search"); err == nil || !strings.Contains(err.Error(), "used its 1 steps") {
		t.Errorf("past the step limit: %v", err)
	}
}
