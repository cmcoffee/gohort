package orchestrate

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The opening proposal is the first thing a person sees when they ask for an
// agent, so it has to say what the thing IS before asking them anything.
func TestProposalOpensWithTheAgentItWouldBuild(t *testing.T) {
	p, ok := proposeAgent("an agent that answers questions from our handbook")
	if !ok {
		t.Fatal("no proposal for a request that plainly describes a knowledge base")
	}
	if p.Shape != "knowledge_base" || p.Name == "" || p.Summary == "" {
		t.Fatalf("proposal = %+v", p)
	}
	if p.NeedsComposing {
		t.Error("a shape that ships an agent was reported as needing composing")
	}
	if p.Draft.ShapeID != "knowledge_base" {
		t.Errorf("the draft follows %q, so improvements would never reach it", p.Draft.ShapeID)
	}
	// Unsaved, and not the framework's own record: a proposal is something to
	// react to, and creating on sight leaves a fleet full of agents nobody
	// agreed to.
	if p.Draft.ID != "" || p.Draft.Owner != "" {
		t.Errorf("the draft is already somebody's record: id=%q owner=%q", p.Draft.ID, p.Draft.Owner)
	}
	if p.Draft.Hidden || p.Draft.Exposed || p.Draft.Fleet {
		t.Errorf("the draft inherited a seed's posture: hidden=%v exposed=%v fleet=%v",
			p.Draft.Hidden, p.Draft.Exposed, p.Draft.Fleet)
	}

	// The points are what a person checks before agreeing: what it refuses,
	// what it can reach, what it remembers.
	joined := strings.Join(p.Points, " ")
	if !strings.Contains(joined, "Stays offline") {
		t.Errorf("the offline contract, the whole point of this shape, is not in the proposal: %v", p.Points)
	}
	if !strings.Contains(joined, "Answer only from the attached corpus") {
		t.Errorf("the shape's rules are not shown: %v", p.Points)
	}
	if strings.Contains(joined, "ask_user") {
		t.Errorf("conversation mechanics were listed as reach: %v", p.Points)
	}
	if len(p.Asks) == 0 {
		t.Error("nothing to ask, so the dialog would stop at the proposal")
	}
}

// A shape whose subject is not known yet has no draft, and dropping the match
// would throw away the useful half: which kind of agent this is, and what it
// needs to be told.
func TestAShapeWithNoAgentStillOpensTheConversation(t *testing.T) {
	p, ok := proposeAgent("watch our status page every 5 minutes")
	if !ok {
		t.Fatal("a watcher request produced nothing at all")
	}
	if !p.NeedsComposing {
		t.Error("a shape that ships no agent was presented as a draft")
	}
	if p.Name == "" || p.Summary == "" {
		t.Errorf("nothing to show the user: name=%q summary=%q", p.Name, p.Summary)
	}
	if len(p.Points) != 0 {
		t.Errorf("points were invented for an agent that does not exist yet: %v", p.Points)
	}
	if len(p.Asks) == 0 {
		t.Error("no questions, so there is no way to compose one either")
	}
}

// Declining is a real answer, and it has to survive all the way out: opening
// with the wrong agent costs more than opening with a question.
func TestProposalDeclinesWhenNothingFits(t *testing.T) {
	for _, req := range []string{"build me an app for tracking invoices", "an agent", ""} {
		if p, ok := proposeAgent(req); ok {
			t.Errorf("%q proposed %q", req, p.Shape)
		}
	}
}

// describeAgentPlainly is the one description of an agent shown to people, so
// it is derived rather than written per shape: a hand-written blurb is a
// second description of the same agent, and it goes stale the first time a
// tool is added.
func TestDescribeAgentPlainly(t *testing.T) {
	got := describeAgentPlainly(AgentRecord{
		AllowedTools: []string{"web_search", "ask_user", "fetch_url"},
		Rules:        "Never guess a price.\nAlways name the source.",
		GapCheck:     true,
		Cortex:       true,
	})
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "Reaches web_search and fetch_url.") {
		t.Errorf("reach line = %q", joined)
	}
	if !strings.Contains(joined, "Never guess a price.") || !strings.Contains(joined, "Always name the source.") {
		t.Errorf("both rules should appear, one per line: %q", joined)
	}
	if !strings.Contains(joined, "covers the question") || !strings.Contains(joined, "ongoing thread") {
		t.Errorf("gap check and cortex should be stated: %q", joined)
	}

	// The empty allowlist is the default pool, not "no tools". Getting these
	// two backwards would tell somebody their agent is offline when it is not.
	if got := describeAgentPlainly(AgentRecord{}); !strings.Contains(strings.Join(got, " "), "standard set of tools") {
		t.Errorf("an empty allowlist described as %v", got)
	}
	if got := describeAgentPlainly(AgentRecord{AllowedTools: []string{noToolsSentinel}}); !strings.Contains(strings.Join(got, " "), "no tools") {
		t.Errorf("the no-tools sentinel described as %v", got)
	}
	// And an offline agent says so once, not twice.
	priv := describeAgentPlainly(AgentRecord{ForcePrivate: true, AllowedTools: []string{"ask_user"}})
	if n := strings.Count(strings.Join(priv, " "), "Reaches"); n != 0 {
		t.Errorf("an offline agent also reported its reach: %v", priv)
	}
}

func TestShapeTitle(t *testing.T) {
	if got := shapeTitle("scheduled_watcher"); got != "Scheduled watcher" {
		t.Errorf("shapeTitle = %q", got)
	}
}

// --- creating from a proposal ----------------------------------------------

// Taking the shape as described creates a complete agent that FOLLOWS it. This
// is the difference between starting from a shape and starting from a blank
// page: the unanswered case is not a stub.
func TestCreatingFromAShapeWithNoAnswers(t *testing.T) {
	db := overlayTestDB(t)
	app := &OrchestrateApp{}
	rec, err := app.createFromShape(httptest.NewRequest("POST", "/", nil), db, "craig@example.com", "knowledge_base",
		wizardRequest{Name: "Handbook"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if rec.Name != "Handbook" {
		t.Errorf("name = %q", rec.Name)
	}
	if rec.ShapeID != "knowledge_base" {
		t.Errorf("the new agent follows %q", rec.ShapeID)
	}
	if rec.Owner != "craig@example.com" || rec.ID == "seed-kb" {
		t.Errorf("not the user's own record: owner=%q id=%q", rec.Owner, rec.ID)
	}
	// It arrives complete: the shape's persona, rules and posture, not a stub
	// waiting for a draft that never came.
	shape, _ := shapeBaseRecord("knowledge_base")
	if rec.OrchestratorPrompt != shape.OrchestratorPrompt {
		t.Error("the shape's persona did not come with it")
	}
	if rec.Rules != shape.Rules || !rec.ForcePrivate {
		t.Errorf("the shape's contract did not come with it: rules=%q private=%v", rec.Rules, rec.ForcePrivate)
	}
	if rec.Hidden {
		t.Error("the user's own agent inherited a seed's hidden posture")
	}
}

// A shape whose subject is not known yet has nothing to copy, and quietly
// building something else would be worse than saying so.
func TestCreatingFromAShapeThatShipsNothing(t *testing.T) {
	db := overlayTestDB(t)
	app := &OrchestrateApp{}
	_, err := app.createFromShape(httptest.NewRequest("POST", "/", nil), db, "craig@example.com", "scheduled_watcher",
		wizardRequest{Name: "Watcher"})
	if err == nil {
		t.Fatal("a watcher was created from a shape that ships no agent")
	}
	if !strings.Contains(err.Error(), "Builder") {
		t.Errorf("the error does not say what to do instead: %v", err)
	}
	if _, err := app.createFromShape(httptest.NewRequest("POST", "/", nil), db, "craig@example.com", "nonsense", wizardRequest{}); err == nil {
		t.Error("an unknown shape was accepted")
	}
}

// The brief drafts from the QUESTIONS as well as the answers: "our runbooks,
// and say you do not know" means nothing without the two questions it answers,
// and a brief that reads as fragments drafts like one.
func TestShapeAnswerBrief(t *testing.T) {
	doc, ok := archetypeBySlug("knowledge_base")
	if !ok {
		t.Fatal("no shape")
	}
	got := shapeAnswerBrief(doc, wizardRequest{
		Purpose: "answers from our handbook",
		Answers: []string{"Our runbooks and the onboarding guide", ""},
	})
	if !strings.Contains(got, doc.Asks[0]) {
		t.Errorf("the question is missing from the brief: %q", got)
	}
	if !strings.Contains(got, "Our runbooks") || !strings.Contains(got, "answers from our handbook") {
		t.Errorf("the answer or the request is missing: %q", got)
	}
	if strings.Contains(got, doc.Asks[1]) {
		t.Errorf("an unanswered question was briefed anyway: %q", got)
	}

	// Nothing answered is the signal to keep the shape's own persona, and it
	// has to be distinguishable from an answer of whitespace.
	if b := shapeAnswerBrief(doc, wizardRequest{Answers: []string{"", "  "}}); b != "" {
		t.Errorf("blank answers produced a brief: %q", b)
	}
	if b := shapeAnswerBrief(doc, wizardRequest{}); b != "" {
		t.Errorf("no answers produced a brief: %q", b)
	}
}

// The dialog is injected HTML, so the checks a compiler cannot make live here.
func TestWizardDescribeHTML(t *testing.T) {
	got := wizardDescribeHTML()
	for _, want := range []string{
		"../api/agents/propose",
		"../api/agents/wizard",
		"window.uiOpenModal",
		"needs_composing", // the shape that ships nothing reads differently
		"shape:d.shape",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the dialog does not contain %q", want)
		}
	}
	if strings.Contains(got, "%!") {
		t.Error("a format verb went unfilled, which ships a script that cannot parse")
	}
}
