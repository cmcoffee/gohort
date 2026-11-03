package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// planCheckRun is a worker turn that has just planned, on a hybrid agent, with
// the lead answering as scripted.
func planCheckRun(t *testing.T, lead ...string) (*planRun, *FakeLLM) {
	t.Helper()
	T, _, _ := newTestOrchestrate(t)
	pinRootDB(t)
	var turns []FakeTurn
	for _, l := range lead {
		turns = append(turns, FakeTurn{Content: l})
	}
	fake := &FakeLLM{Turns: turns}
	T.LLM, T.LeadLLM = fake, fake
	turn := &chatTurn{app: T, ctx: context.Background(), agent: AgentRecord{ID: "a1", LeadReview: true}}
	return &planRun{t: turn, maxSteps: 6, msgs: []ChatMessage{{Role: "user", Content: "Plan a weekend in Fresno."}},
		allTools: []AgentToolDef{
			{Tool: Tool{Name: "web_search", Description: "Search the web. Returns titles and links."}},
			{Tool: Tool{Name: "fetch_url", Description: "Read one page. Text only."}},
		}}, fake
}

func threeSteps() []PlanStep {
	return []PlanStep{
		{ID: 1, Title: "Find events", WorkerBrief: "Search for events this weekend in Fresno.", Tools: []string{"web_search"}},
		{ID: 2, Title: "Read details", WorkerBrief: "Read the event pages found.", Tools: []string{"fetch_url"}},
		{ID: 3, Title: "Pick", WorkerBrief: "Pick the best three events."},
	}
}

// The lead reads a big plan with the request and the tools a step can be
// given, and nothing else; on "go" the worker's plan runs as it was.
func TestTheLeadApprovesABigPlan(t *testing.T) {
	pr, fake := planCheckRun(t, `{"verdict":"go"}`)
	got := pr.checkPlanWithLead(threeSteps())
	if len(got) != 3 || got[0].Title != "Find events" {
		t.Fatalf("an approved plan runs as it was: %+v", got)
	}
	if fake.Calls() != 1 {
		t.Fatalf("one check, got %d calls", fake.Calls())
	}
	prompt := fake.Prompt(0)
	for _, want := range []string{"WHAT THEY ASKED\nPlan a weekend in Fresno.", "1. Find events", "brief: Search for events", "- web_search: Search the web."} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the check lacks %q:\n%s", want, prompt)
		}
	}
	if fake.Config(0).RouteKey != leadPlanCheckRouteKey {
		t.Errorf("routed as %q", fake.Config(0).RouteKey)
	}
}

// On "revise" the lead's plan runs, held to the tools a step can be given.
func TestTheLeadRewritesABigPlan(t *testing.T) {
	pr, _ := planCheckRun(t, `{"verdict":"revise","reason":"reading comes before picking, and search was missing","steps":[
		{"title":"Search","worker_brief":"Find this weekend's events in Fresno.","tools":["web_search","made_up_tool"]},
		{"title":"Read and pick","worker_brief":"Read the pages and pick three.","tools":["fetch_url"]}]}`)
	got := pr.checkPlanWithLead(threeSteps())
	if len(got) != 2 || got[0].Title != "Search" {
		t.Fatalf("the lead's plan should run: %+v", got)
	}
	if strings.Join(got[0].Tools, ",") != "web_search" {
		t.Errorf("a tool the step cannot be given was kept: %v", got[0].Tools)
	}
}

// A check that cannot be acted on runs the worker's own plan.
func TestABadPlanCheckRunsTheWorkersPlan(t *testing.T) {
	for _, answer := range []string{
		`not json at all`,
		`{"verdict":"maybe"}`,
		`{"verdict":"revise","reason":"x","steps":[{"title":"Only one","worker_brief":"Do it all."}]}`,
	} {
		pr, _ := planCheckRun(t, answer)
		if got := pr.checkPlanWithLead(threeSteps()); len(got) != 3 || got[2].Title != "Pick" {
			t.Errorf("%s: the worker's plan should run: %+v", answer, got)
		}
	}
}

// Every plan, on a worker-driven turn that may reach the lead, on the
// consultant and hybrid levels, is checked: a two-step plan is as likely to
// miss part of the job as a long one.
func TestWhichPlansTheLeadChecks(t *testing.T) {
	pr, fake := planCheckRun(t, `{"verdict":"go"}`)
	if pr.checkPlanWithLead(threeSteps()[:2]); fake.Calls() != 1 {
		t.Error("a two-step plan was not checked")
	}
	for name, change := range map[string]func(*planRun){
		"Worker only":    func(pr *planRun) { pr.t.agent.LeadReview = false },
		"Lead model":     func(pr *planRun) { pr.t.agent.LeadModel = true },
		"Lead plans":     func(pr *planRun) { pr.leadPlansOn = true },
		"a Private turn": func(pr *planRun) { pr.t.privateMode = true },
	} {
		pr, fake := planCheckRun(t, `{"verdict":"go"}`)
		change(pr)
		if pr.checkPlanWithLead(threeSteps()); fake.Calls() != 0 {
			t.Errorf("%s: the plan was checked", name)
		}
	}
}

// The consultant's plans go past the lead too: asking and planning come
// together on every level the worker drives with the lead beside it.
func TestTheConsultantsPlansGoToTheLead(t *testing.T) {
	pr, fake := planCheckRun(t, `{"verdict":"go"}`)
	pr.t.agent.LeadReview, pr.t.agent.ConsultLead = false, settingOn
	if leadUseOf(RootDB, pr.t.agent) != leadUseConsult {
		t.Fatalf("precondition: the agent is on the consultant level, reads %q", leadUseOf(RootDB, pr.t.agent))
	}
	if pr.checkPlanWithLead(threeSteps()); fake.Calls() != 1 {
		t.Error("a consultant's plan was not checked")
	}
}

// Hybrid is the field "Lead reviews" was stored in, so an agent saved on it
// is a hybrid, and the editor's old value still lands on it.
func TestHybridIsWhereLeadReviewsLanded(t *testing.T) {
	pinRootDB(t)
	if got := leadUseOf(RootDB, AgentRecord{LeadReview: true}); got != leadUseHybrid {
		t.Errorf("an agent saved on Lead reviews reads %q", got)
	}
	var a AgentRecord
	applyLeadUse(RootDB, &a, "review")
	if !a.LeadReview || a.LeadModel || leadUseOf(RootDB, a) != leadUseHybrid {
		t.Errorf("the old value should choose hybrid: %+v", a)
	}
}

// The deployment's Off for consulting takes the lead away from beside the
// worker everywhere: neither a consultant nor a hybrid asks it or sends it a
// plan, and the editor stops offering the consultant.
func TestTheDeploymentsOffKeepsTheWorkerAlone(t *testing.T) {
	pinRootDB(t)
	setDeploymentSetting(RootDB, deploymentMaximum, defaultConsultLead, settingOff)
	if consultAllowed(RootDB) {
		t.Fatal("consulting is still allowed")
	}
	if leadAsks(RootDB, AgentRecord{ID: "a1", LeadReview: true}) {
		t.Error("a hybrid still works with the lead beside it")
	}
	for _, o := range leadUseField(true, AgentRecord{ID: "a1"}).Options {
		if o.Value == leadUseConsult {
			t.Error("the editor still offers the consultant")
		}
	}
}

// The consult tool is offered by level: the consultant and the hybrid have
// it; checked on the source because the catalog is built in one long function.
func TestTheConsultToolFollowsTheLevel(t *testing.T) {
	if src := readFile(t, "runner_tool_catalog.go"); !strings.Contains(src, "if forOrchestrator && leadAsks(RootDB, t.agent) {") {
		t.Error("the consult tool is no longer offered by the agent's level")
	}
}

// A consult carries the conversation to the lead with the question.
func TestAConsultCarriesTheConversation(t *testing.T) {
	T, _, _ := newTestOrchestrate(t)
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "Take the train."}}}
	T.LLM, T.LeadLLM = fake, fake
	turn := &chatTurn{app: T, ctx: context.Background(), session: &ChatSession{Messages: []ChatMessage{
		{Role: "user", Content: "Get me to Fresno cheaply."}}}}
	if _, err := turn.consult("Train or bus?", ""); err != nil {
		t.Fatal(err)
	}
	if p := fake.Prompt(0); !strings.Contains(p, "Person: Get me to Fresno cheaply.") || !strings.Contains(p, "QUESTION\nTrain or bus?") {
		t.Errorf("the consultant did not get the conversation with the question:\n%s", p)
	}
}
