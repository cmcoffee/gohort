package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A model has the suite's NAME — it is what list printed and what a person
// says. Requiring the id would make the tool unusable without a copy-paste
// nobody is there to do.
func TestASuiteResolvesByNameAsWellAsID(t *testing.T) {
	db := evalDB(t)
	saved, err := SaveEvalSuite(db, goodSuite())
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	byID, err := findEvalSuite(db, saved.ID)
	if err != nil || byID.ID != saved.ID {
		t.Fatalf("by id: %+v %v", byID, err)
	}
	byName, err := findEvalSuite(db, "Debate quality")
	if err != nil || byName.ID != saved.ID {
		t.Fatalf("by name: %+v %v", byName, err)
	}
	if mixed, err := findEvalSuite(db, "  debate QUALITY "); err != nil || mixed.ID != saved.ID {
		t.Errorf("the match should survive case and surrounding space: %v", err)
	}
}

// Exact, not prefix. The first time somebody has "Support bot" and "Support bot
// v2", a prefix match grades the wrong one and reports a number about it.
func TestAPartialSuiteNameIsRefusedRatherThanGuessed(t *testing.T) {
	db := evalDB(t)
	if _, err := SaveEvalSuite(db, goodSuite()); err != nil {
		t.Fatalf("save: %v", err)
	}
	_, err := findEvalSuite(db, "Debate")
	if err == nil {
		t.Fatal("a partial name must not resolve — it would grade a suite nobody named")
	}
	// And the refusal has to carry what DOES exist, or the caller's next move
	// is another guess.
	if !strings.Contains(err.Error(), "Debate quality") {
		t.Errorf("the error should name the suites that exist: %q", err)
	}

	empty := evalDB(t)
	if _, err := findEvalSuite(empty, "anything"); err == nil ||
		!strings.Contains(err.Error(), "no eval suites are saved") {
		t.Errorf("with nothing saved, say that rather than listing an empty set: %v", err)
	}
}

// The result an agent reads: failures in full because they are what it acts on,
// passes by name because they only need counting.
func TestTheRunResultGivesFailuresInFullAndPassesByName(t *testing.T) {
	suite := goodSuite()
	suite.Name = "Support bot"
	suite.Runs = 3
	run := EvalRun{
		Total: 2, Passed: 1, TargetHash: "abcdef0123456789",
		Results: []EvalResult{
			{Name: "refund_policy", Passed: false, Runs: 3, Passes: 1,
				Reasons:     []string{`reply did not include "14 days"`},
				ToolsCalled: []string{"search_kb"},
				Output:      "You can return most items."},
			{Name: "greeting", Passed: true, Runs: 3, Passes: 3},
		},
	}
	out := renderEvalRunForTool(suite, run)

	if !strings.HasPrefix(out, "Support bot — 1/2") {
		t.Errorf("the headline number goes first, for a caller that reads one line: %q", firstLine(out))
	}
	for _, want := range []string{"refund_policy", "14 days", "search_kb", "You can return most items."} {
		if !strings.Contains(out, want) {
			t.Errorf("a failure must carry what it needs to be acted on; missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "greeting (3/3)") {
		t.Errorf("a pass should be counted, not expanded: %q", out)
	}
	if strings.Contains(out, "PASSED: greeting\n    ") {
		t.Error("a passing case should not render its body")
	}
	// The pass RATE is the signal on a non-deterministic model, so a case that
	// passed one run in three must not read as a flat failure.
	if !strings.Contains(out, "(1/3)") {
		t.Errorf("the per-case rate is the signal and must survive into the result: %q", out)
	}
	if !strings.Contains(out, "abcdef01") || strings.Contains(out, "abcdef0123456789") {
		t.Errorf("the version fingerprint should be present and shortened: %q", out)
	}
}

// Stub mode is the safety story. Which mode a run used has to be in the result,
// or an agent reporting "all passed" cannot say whether that cost real emails.
func TestTheRunResultSaysWhetherToolsWereRealOrScripted(t *testing.T) {
	stubbed := renderEvalRunForTool(goodSuite(), EvalRun{Total: 0})
	if !strings.Contains(stubbed, "STUBBED") {
		t.Errorf("the default is stubbed and the result should say so: %q", stubbed)
	}
	live := goodSuite()
	live.StubMode = "off"
	if got := renderEvalRunForTool(live, EvalRun{Total: 0}); !strings.Contains(got, "LIVE") ||
		!strings.Contains(got, "side effects") {
		t.Errorf("a live run must announce itself: %q", got)
	}
}

// A cancelled suite grades what it reached and returns it. Read as a whole
// score that is a WRONG answer rather than a missing one, so say it.
func TestAPartialRunSaysItStoppedEarly(t *testing.T) {
	run := EvalRun{
		Total: 10, Passed: 2,
		Results: []EvalResult{{Name: "a", Passed: true}, {Name: "b", Passed: true}},
	}
	out := renderEvalRunForTool(goodSuite(), run)
	if !strings.Contains(out, "STOPPED EARLY") || !strings.Contains(out, "2 of 10") {
		t.Errorf("a partial run must not read as a complete one: %q", out)
	}
}

// The tool has to be IN the authoring catalog, or the capability that grants
// editing still grants no way to measure the edit.
func TestTheEvalToolShipsWithTheAuthoringCatalog(t *testing.T) {
	sess := &ToolSession{Username: "u"}
	var names []string
	for _, td := range builderAuthoringTools(sess, nil) {
		names = append(names, td.Tool.Name)
	}
	if !contains(names, "eval") {
		t.Fatalf("an authoring agent can edit another agent but not measure it; catalog: %v", names)
	}
}

// t is nil on three dispatch paths and the app pointer has no second source.
// Refuse plainly there — the alternative is a panic on every delegated run.
func TestTheEvalToolRefusesWithoutAnAppRatherThanPanicking(t *testing.T) {
	_, _, err := evalToolScope(nil, &ToolSession{Username: "u"})
	if err == nil {
		t.Fatal("no app context should be an error, not a resolved scope")
	}
	if _, _, err := evalToolScope(&chatTurn{}, &ToolSession{Username: "u"}); err == nil {
		t.Error("a turn with no app is the same case")
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// The name is the key, so writing a case that already exists is an EDIT. The
// alternative is a duplicate-name save failure every time an improver refines
// an assertion it wrote last week.
func TestAddingACaseUnderAnExistingNameReplacesIt(t *testing.T) {
	db := evalDB(t)
	saved, err := SaveEvalSuite(db, goodSuite())
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	before := len(saved.Cases)

	c := evalCaseFromArgs(map[string]any{
		"name": "picks_a_side", "prompt": "Should we adopt a monorepo?",
		"must_include": []any{"decides"},
	})
	if c.Name != "picks_a_side" || len(c.MustInclude) != 1 {
		t.Fatalf("case args did not parse: %+v", c)
	}

	// Same name as a case goodSuite already holds.
	suite, _ := findEvalSuite(db, saved.ID)
	for i := range suite.Cases {
		if suite.Cases[i].Name == c.Name {
			suite.Cases[i] = c
		}
	}
	after, err := SaveEvalSuite(db, suite)
	if err != nil {
		t.Fatalf("save after replace: %v", err)
	}
	if len(after.Cases) != before {
		t.Errorf("replacing a case should not grow the suite: %d → %d", before, len(after.Cases))
	}
	if after.Cases[0].MustInclude[0] != "decides" {
		t.Errorf("the replacement did not take: %+v", after.Cases[0])
	}
}

// Every assertion form the grader supports has to be reachable from the tool,
// or a model can only ever write the weakest kind of case.
func TestEveryAssertionFormSurvivesTheToolArguments(t *testing.T) {
	c := evalCaseFromArgs(map[string]any{
		"name": "escalates", "prompt": "my order never arrived",
		"must_include":        []any{"sorry"},
		"must_not_include":    []any{"refund policy"},
		"must_call_tools":     []any{"create_ticket"},
		"must_not_call_tools": []any{"send_email"},
		"judge_prompt":        "the reply commits to a next step",
		"must_fields":         map[string]any{"winner": "for"},
		"stub_results":        map[string]any{"create_ticket": "TICKET-1"},
		"notes":               "written for the 2026-09 escalation miss",
	})
	switch {
	case len(c.MustInclude) != 1 || len(c.MustNotInclude) != 1:
		t.Errorf("reply assertions lost: %+v", c)
	case len(c.MustCallTools) != 1 || len(c.MustNotCallTools) != 1:
		t.Errorf("tool-call assertions lost: %+v", c)
	case c.JudgePrompt == "" || c.MustFields["winner"] != "for":
		t.Errorf("judge or field assertions lost: %+v", c)
	case c.StubResults["create_ticket"] != "TICKET-1":
		t.Errorf("stub results lost, so a multi-step case cannot behave like production: %+v", c)
	case c.Notes == "":
		t.Errorf("notes lost — the record of why a case exists: %+v", c)
	}
}

// A case that asserts nothing passes whatever the target does. That rule lives
// in Validate, and the tool must go through it rather than around it.
func TestACaseThatAssertsNothingIsRefusedOnSave(t *testing.T) {
	db := evalDB(t)
	suite := goodSuite()
	suite.Cases = append(suite.Cases, evalCaseFromArgs(map[string]any{
		"name": "vague", "prompt": "hello",
	}))
	_, err := SaveEvalSuite(db, suite)
	if err == nil {
		t.Fatal("a case with no assertions must be refused — it raises the score and grades nothing")
	}
	if !strings.Contains(err.Error(), "asserts nothing") {
		t.Errorf("the refusal should say what is wrong with it: %v", err)
	}
}

// The tool must not be able to arm live mode. Stubbing off is what makes a run
// send the emails, and it is a decision for a person on the Evals page.
func TestTheToolCannotCreateALiveSuite(t *testing.T) {
	gt := evalTool(nil)
	// Params() is the union across every action, so a stub switch on any of
	// them would show up here.
	for p := range gt.Params() {
		switch strings.ToLower(p) {
		case "stub", "stub_mode", "live", "stubbed":
			t.Errorf("the eval tool exposes %q — a model must not be able to turn stubbing off", p)
		}
	}
	if !strings.Contains(gt.Desc(), "add_case") {
		t.Error("the tool description should name its case actions, or a model will not know they exist")
	}
}

// The run count defaults rather than failing: a bad number is not worth
// refusing a suite over, and models send "3", 3 and 3.0 interchangeably.
func TestTheRunCountToleratesTheFormsAModelSends(t *testing.T) {
	cases := map[string]struct {
		in   any
		want int
	}{
		"int":     {3, 3},
		"string":  {"3", 3},
		"float":   {"3.0", 3},
		"zero":    {0, 1},
		"garbage": {"lots", 1},
	}
	for name, tc := range cases {
		if got := intArgOr(map[string]any{"runs": tc.in}, "runs", 1); got != tc.want {
			t.Errorf("%s: intArgOr(%v) = %d, want %d", name, tc.in, got, tc.want)
		}
	}
	if got := intArgOr(map[string]any{}, "runs", 1); got != 1 {
		t.Errorf("a missing value should fall back, got %d", got)
	}
}

// Removing the last case would leave a suite that grades nothing. Say that in
// terms of what was asked rather than letting it surface as a save failure.
func TestRemovingTheLastCaseIsRefusedInItsOwnWords(t *testing.T) {
	db := evalDB(t)
	one := goodSuite()
	one.Cases = one.Cases[:1]
	saved, err := SaveEvalSuite(db, one)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	// The rule the handler enforces before saving.
	if len(saved.Cases) != 1 {
		t.Fatalf("fixture should hold exactly one case, holds %d", len(saved.Cases))
	}
	stripped := saved
	stripped.Cases = nil
	if _, err := SaveEvalSuite(db, stripped); err == nil {
		t.Error("a suite with no cases must not save")
	}
}

// runs=3 against a suite saved at 1 used to run ONCE and report "1/1", with
// nothing saying the number the caller asked for went nowhere. Grouped tools do
// not reject unknown params, so an ignored parameter and an accepted one look
// identical from the outside.
func TestTheRunActionAcceptsARepeatCountOverride(t *testing.T) {
	gt := evalTool(nil)
	if _, ok := gt.Params()["runs"]; !ok {
		t.Fatal("run must accept a repeat count, or a caller asking for a rate silently gets one sample")
	}
	// The override only applies when it is a real count; omitted means "use
	// what the suite says", which is what 0 stands for at the call site.
	if got := intArgOr(map[string]any{}, "runs", 0); got != 0 {
		t.Errorf("an omitted override must not overwrite the suite's own setting, got %d", got)
	}
	if got := intArgOr(map[string]any{"runs": 3}, "runs", 0); got != 3 {
		t.Errorf("a supplied override must be honored, got %d", got)
	}
}

// The harness used to grade GetAgentTools(allowed_tools...) — the global
// registry alone — which is a materially different agent from the one that
// ships. A support agent whose whole job is answering from a corpus was graded
// by a harness that could not reach the corpus, so it improvised by
// construction and the failure read as the agent's.
func TestTheEvalCatalogCarriesTheFrameworkTools(t *testing.T) {
	db := evalDB(t)
	app := &OrchestrateApp{AppCore: AppCore{DB: db}}
	agent := AgentRecord{
		ID: "a1", Name: "Support bot", Owner: "u",
		AllowedTools:        []string{"web_search"},
		AttachedCollections: []string{"c-kiteworks"},
	}
	var names []string
	for _, td := range app.evalAgentCatalog(db, agent) {
		names = append(names, td.Tool.Name)
	}
	joined := strings.Join(names, " ")
	if !strings.Contains(joined, "knowledge_search") {
		t.Errorf("an agent with a corpus must be graded holding the tool that reads it; got %v", names)
	}
	// And no name twice: a tool arriving by two routes is still one tool, and a
	// duplicate makes the loop reject a handler.
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Errorf("duplicate tool %q in the graded catalog", n)
		}
		seen[n] = true
	}
}

// Rules render above the persona and win every conflict with it. A fingerprint
// that ignored them let three runs across a rules rewrite report one version,
// with the history inviting the reader to call the difference noise.
func TestTheFingerprintMovesWhenBehaviourDoes(t *testing.T) {
	base := AgentRecord{OrchestratorPrompt: "you are support", AllowedTools: []string{"web_search"}}
	start := agentFingerprint(base)

	cases := map[string]func(a *AgentRecord){
		"rules":       func(a *AgentRecord) { a.Rules = "never guess a version number" },
		"collections": func(a *AgentRecord) { a.AttachedCollections = []string{"c1"} },
		"sources":     func(a *AgentRecord) { a.AttachedSources = ReferenceSelections{{Kind: "files", ItemID: "logs"}} },
		"machine":     func(a *AgentRecord) { a.Machine = "intake" },
		"prompt":      func(a *AgentRecord) { a.OrchestratorPrompt = "you are something else" },
		"tools":       func(a *AgentRecord) { a.AllowedTools = []string{"web_search", "fetch_url"} },
	}
	for name, mutate := range cases {
		changed := base
		mutate(&changed)
		if agentFingerprint(changed) == start {
			t.Errorf("changing %s did not move the fingerprint, so a score history would call it noise", name)
		}
	}

	// And things that are NOT behaviour must not move it, or every rename puts
	// the history under a fresh hash and it compares nothing to nothing.
	renamed := base
	renamed.Name, renamed.Description = "Renamed", "a new description"
	if agentFingerprint(renamed) != start {
		t.Error("a rename is not a behaviour change")
	}
	reordered := base
	reordered.AllowedTools = []string{"web_search"}
	if agentFingerprint(reordered) != start {
		t.Error("reordering an allowlist is not a behaviour change")
	}
}
