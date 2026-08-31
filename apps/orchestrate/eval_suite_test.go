package orchestrate

import (
	"context"
	"errors"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func evalDB(t *testing.T) Database {
	t.Helper()
	return &DBase{Store: kvlite.MemStore()}
}

func goodSuite() EvalSuite {
	return EvalSuite{
		Name: "Debate quality", TargetKind: EvalTargetPipeline, TargetID: "p1",
		Cases: []EvalCase{
			{Name: "picks_a_side", Prompt: "Should we adopt a monorepo?", MustInclude: []string{"wins"}},
			{Name: "cites", Prompt: "Is a four-day week worth it?", JudgePrompt: "the verdict names the argument that decided it"},
		},
	}
}

// Stub mode is the whole safety story, so "unset" has to read as ON — a record
// written before anybody thought about the field must not run for real.
func TestStubDefaultsOnEvenWhenUnset(t *testing.T) {
	if !(EvalSuite{}).Stubbed() {
		t.Error("an unset Stub must read as stubbed; an eval that runs for real sends the emails")
	}
	off := false
	if (EvalSuite{Stub: &off}).Stubbed() {
		t.Error("an explicit false must turn stubbing off")
	}
	on := true
	if !(EvalSuite{Stub: &on}).Stubbed() {
		t.Error("an explicit true must stay on")
	}
}

// A case that asserts nothing PASSES unconditionally, which is worse than
// having no case: it raises the score while grading nothing.
func TestASuiteThatGradesNothingIsRefused(t *testing.T) {
	s := goodSuite()
	s.Cases = append(s.Cases, EvalCase{Name: "vacuous", Prompt: "hello"})
	err := s.Validate()
	if err == nil {
		t.Fatal("expected a refusal for a case with no assertions")
	}
	if !strings.Contains(err.Error(), "asserts nothing") {
		t.Errorf("the error should say why: %v", err)
	}
}

func TestSuiteValidationCatchesTheRest(t *testing.T) {
	for name, mut := range map[string]func(*EvalSuite){
		"no name":        func(s *EvalSuite) { s.Name = "" },
		"no target kind": func(s *EvalSuite) { s.TargetKind = "" },
		"bad kind":       func(s *EvalSuite) { s.TargetKind = "wishful" },
		"no target":      func(s *EvalSuite) { s.TargetID = "" },
		"no cases":       func(s *EvalSuite) { s.Cases = nil },
		"unnamed case":   func(s *EvalSuite) { s.Cases[0].Name = "" },
		"duplicate name": func(s *EvalSuite) { s.Cases[1].Name = s.Cases[0].Name },
		"no prompt":      func(s *EvalSuite) { s.Cases[0].Prompt = "" },
	} {
		s := goodSuite()
		mut(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: expected a refusal", name)
		}
	}
	if err := goodSuite().Validate(); err != nil {
		t.Errorf("a good suite was refused: %v", err)
	}
}

func TestSuiteRoundTripAndHistory(t *testing.T) {
	db := evalDB(t)
	saved, err := SaveEvalSuite(db, goodSuite())
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.ID == "" || saved.Created.IsZero() {
		t.Fatal("save should allocate an id and stamp created")
	}
	back, ok := LoadEvalSuite(db, saved.ID)
	if !ok || back.Name != saved.Name || len(back.Cases) != 2 {
		t.Fatalf("round trip lost something: %+v", back)
	}

	// Two runs of the same target share a hash; the history is newest-first.
	hash := EvalTargetFingerprint("prompt v1", "tools:a,b")
	SaveEvalRun(db, EvalRun{SuiteID: saved.ID, Passed: 24, Total: 30, TargetHash: hash, Started: back.Created})
	SaveEvalRun(db, EvalRun{SuiteID: saved.ID, Passed: 29, Total: 30, TargetHash: EvalTargetFingerprint("prompt v2", "tools:a,b"), Started: back.Created.Add(1)})
	runs := ListEvalRuns(db, saved.ID)
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	if runs[0].Rate() != "29/30" {
		t.Errorf("history should be newest first, got %s", runs[0].Rate())
	}
	// The point of the hash: an edit is visible as a different fingerprint.
	if runs[0].TargetHash == runs[1].TargetHash {
		t.Error("editing the target should change its fingerprint, or the history says nothing")
	}
}

// A rename or a new timestamp must NOT read as a behaviour change, or every
// comparison is against a different hash and the score history is noise.
func TestFingerprintCoversBehaviourOnly(t *testing.T) {
	a := EvalTargetFingerprint("you are a judge", "lead")
	b := EvalTargetFingerprint("you are a judge", "lead")
	if a != b {
		t.Error("the same behaviour must fingerprint the same")
	}
	if a == EvalTargetFingerprint("you are a judge", "worker") {
		t.Error("a tier change must fingerprint differently")
	}
}

// A run keyed to a deleted suite has a score and no way back to what was
// scored, so the history goes with the suite.
func TestDeletingASuiteTakesItsHistory(t *testing.T) {
	db := evalDB(t)
	saved, _ := SaveEvalSuite(db, goodSuite())
	other, _ := SaveEvalSuite(db, goodSuite())
	SaveEvalRun(db, EvalRun{SuiteID: saved.ID, Total: 2})
	SaveEvalRun(db, EvalRun{SuiteID: other.ID, Total: 2})

	DeleteEvalSuite(db, saved.ID)
	if _, ok := LoadEvalSuite(db, saved.ID); ok {
		t.Error("suite survived deletion")
	}
	if n := len(ListEvalRuns(db, saved.ID)); n != 0 {
		t.Errorf("%d orphaned runs left behind", n)
	}
	if n := len(ListEvalRuns(db, other.ID)); n != 1 {
		t.Errorf("another suite's history was deleted too: %d runs left", n)
	}
}

// A suite whose target is gone has not scored zero — it has scored nothing.
// Recording a 0/30 would put a fabricated cliff in the score history at the
// moment somebody renamed something.
func TestAMissingTargetIsNotAFailingScore(t *testing.T) {
	db := evalDB(t)
	suite := goodSuite()
	suite.TargetKind = EvalTargetAgent
	suite.TargetID = "no-such-agent"
	suite, _ = SaveEvalSuite(db, suite)

	app := &OrchestrateApp{}
	run, err := app.RunEvalSuite(context.Background(), db, "u", suite, "")
	if err == nil {
		t.Fatal("expected an error for a target that does not exist")
	}
	var missing EvalTargetMissing
	if !errors.As(err, &missing) {
		t.Errorf("want a typed EvalTargetMissing so callers can tell it from a low score, got %T", err)
	}
	if run.Passed != 0 || len(run.Results) != 0 {
		t.Error("a missing target must produce no results at all, not zero passes out of N")
	}
	if run.Err == "" {
		t.Error("the run should record WHY it could not run")
	}
	// Still recorded: a run that vanishes leaves somebody wondering whether
	// they clicked the button.
	if len(ListEvalRuns(db, suite.ID)) != 1 {
		t.Error("the failed run should still be in the history")
	}
}

// A kind that has not been wired up yet is refused with a message, not scored.
func TestAnUnwiredTargetKindIsRefused(t *testing.T) {
	db := evalDB(t)
	suite, _ := SaveEvalSuite(db, goodSuite()) // pipeline kind, not wired yet
	app := &OrchestrateApp{}
	run, err := app.RunEvalSuite(context.Background(), db, "u", suite, "")
	if err == nil {
		t.Fatal("expected a refusal for an unwired target kind")
	}
	if run.Total != len(suite.Cases) || run.Passed != 0 {
		t.Errorf("run = %d/%d; a refusal should not read as a score", run.Passed, run.Total)
	}
}

// The generic half: cancelling returns what was graded rather than nothing.
func TestCancellingKeepsWhatWasAlreadyGraded(t *testing.T) {
	cases := []EvalCase{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	ctx, stop := context.WithCancel(context.Background())
	n := 0
	got := runEvalCases(ctx, cases, 1, func(context.Context, EvalCase) EvalResult {
		n++
		if n == 2 {
			stop() // cancelled partway through the second case
		}
		return EvalResult{Passed: true}
	})
	if len(got) != 2 {
		t.Errorf("graded %d cases, want the 2 that completed before the stop", len(got))
	}
}

// The rate, not a boolean: a single run of a non-deterministic model is an
// anecdote.
func TestEachCaseRunsNTimesAndReportsTheRate(t *testing.T) {
	call := 0
	got := runEvalCases(context.Background(), []EvalCase{{Name: "flaky"}}, 4,
		func(context.Context, EvalCase) EvalResult {
			call++
			return EvalResult{Passed: call%2 == 0} // passes half the time
		})
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1 aggregated row", len(got))
	}
	if got[0].Runs != 4 || got[0].Passes != 2 {
		t.Errorf("rate = %d/%d, want 2/4", got[0].Passes, got[0].Runs)
	}
	if got[0].Passed {
		t.Error("Passed is strict — a case that failed half its runs did not pass")
	}
}

// The streaming half: one block per case as it lands, a meta event carrying the
// score, and an EvalRun written with the per-case detail the transcript does
// not keep.
func TestStreamingASuiteEmitsPerCaseAndRecordsTheRun(t *testing.T) {
	db := evalDB(t)
	suite := goodSuite()
	suite.TargetKind = EvalTargetAgent
	suite.TargetID = "a1"
	suite, _ = SaveEvalSuite(db, suite)

	app := &OrchestrateApp{}
	var events []PipelineEvent
	// Stub the target resolution by grading with a canned executor: this test
	// is about the streaming and recording, not about the agent loop.
	target := evalTarget{Fingerprint: "abc123", Exec: func(context.Context, EvalCase) EvalResult {
		return EvalResult{Passed: true, Output: "fine"}
	}}
	out := app.streamEvalWith(context.Background(), db, suite, "trying a shorter prompt", target,
		func(ev PipelineEvent) { events = append(events, ev) })

	var blocks, metas int
	for _, ev := range events {
		switch ev.Kind {
		case "block":
			blocks++
			if ev.Type != "eval_case" {
				t.Errorf("block type = %q, want eval_case so a renderer can style it", ev.Type)
			}
		case "meta":
			metas++
			if ev.Meta["Score"] != "2/2" {
				t.Errorf("meta Score = %q, want 2/2", ev.Meta["Score"])
			}
			if ev.Meta["Version"] != "abc123" {
				t.Errorf("meta Version = %q — the row should carry which version was graded", ev.Meta["Version"])
			}
		}
	}
	if blocks != 2 {
		t.Errorf("emitted %d case blocks, want one per case", blocks)
	}
	if metas != 1 {
		t.Errorf("emitted %d meta events, want exactly 1", metas)
	}
	if !strings.Contains(out, "2/2") {
		t.Errorf("the run's result text should carry the rate, got %q", out)
	}

	runs := ListEvalRuns(db, suite.ID)
	if len(runs) != 1 {
		t.Fatalf("recorded %d runs, want 1", len(runs))
	}
	rec := runs[0]
	if rec.Passed != 2 || rec.Total != 2 || len(rec.Results) != 2 {
		t.Errorf("record = %d/%d with %d results", rec.Passed, rec.Total, len(rec.Results))
	}
	if rec.TargetHash != "abc123" || rec.Note != "trying a shorter prompt" {
		t.Errorf("the record should carry the fingerprint and the note: %+v", rec)
	}
	if rec.Finished.IsZero() {
		t.Error("a completed run should be marked finished")
	}
}

// A pill is read in a list, so the word is coarse and the number carries the
// detail.
func TestOutcomeWordIsCoarse(t *testing.T) {
	for _, tc := range []struct {
		passed, total int
		want          string
	}{
		{2, 2, "all passed"}, {0, 2, "all failed"}, {1, 2, "mixed"}, {0, 0, "empty"},
	} {
		if got := evalOutcome(EvalRun{Passed: tc.passed, Total: tc.total}); got != tc.want {
			t.Errorf("%d/%d = %q, want %q", tc.passed, tc.total, got, tc.want)
		}
	}
}

// A failing case is opened for its REASON, so the reason comes before the
// output rather than after it.
func TestFailingCaseBodyLeadsWithTheReason(t *testing.T) {
	body := evalCaseBody(EvalResult{
		Name: "cites", Reasons: []string{"missing \"10080\""},
		ToolsCalled: []string{"web_search"}, Output: "a long reply nobody needs first",
	})
	if strings.Index(body, "missing") > strings.Index(body, "a long reply") {
		t.Errorf("the reason should come first:\n%s", body)
	}
}

// Structured assertions are the reason grading a pipeline differs from grading
// an agent: "wins" appearing somewhere in three paragraphs is not the same
// claim as winner == "for", and only one of the two is a test.
func TestFieldAssertionsGradeTheDeclaredShape(t *testing.T) {
	c := EvalCase{MustFields: map[string]string{"winner": "for", "confidence": "high"}}
	fields := map[string]any{"winner": "FOR", "confidence": "high", "verdict": "prose"}

	reasons, pass := gradeEvalFields(c, fields)
	if !pass {
		t.Errorf("case should pass; enum values are compared case-insensitively: %v", reasons)
	}

	fields["winner"] = "against"
	reasons, pass = gradeEvalFields(c, fields)
	if pass {
		t.Error("a changed verdict must fail")
	}
	if !strings.Contains(strings.Join(reasons, " "), `want "for"`) {
		t.Errorf("the reason should say what was expected: %v", reasons)
	}
}

// A field the pipeline no longer declares is a FAILURE, not a skip. A renamed
// field silently passing would read as continued success, which is exactly the
// regression evals exist to catch.
func TestAMissingFieldFailsRatherThanSkipping(t *testing.T) {
	reasons, pass := gradeEvalFields(
		EvalCase{MustFields: map[string]string{"winner": "for"}},
		map[string]any{"victor": "for"}, // renamed out from under the case
	)
	if pass {
		t.Fatal("an assertion on a field that no longer exists must fail")
	}
	joined := strings.Join(reasons, " ")
	if !strings.Contains(joined, "victor") {
		t.Errorf("the reason should name what the stage DOES declare, so the fix is one read away: %v", reasons)
	}
}

// Two runs of an identical failure must read the same way; map order would
// shuffle the reasons between them.
func TestFieldReasonsAreStable(t *testing.T) {
	c := EvalCase{MustFields: map[string]string{"zebra": "1", "alpha": "2", "middle": "3"}}
	first, _ := gradeEvalFields(c, map[string]any{})
	for i := 0; i < 20; i++ {
		again, _ := gradeEvalFields(c, map[string]any{})
		if strings.Join(again, "|") != strings.Join(first, "|") {
			t.Fatal("reason order is not stable across runs")
		}
	}
}

// The fingerprint tracks BEHAVIOUR: a renamed pipeline is the same pipeline,
// an edited prompt is not.
func TestPipelineFingerprintTracksBehaviourNotNames(t *testing.T) {
	base := PipelineDef{Name: "Debate", Stages: []PipelineStage{
		{Name: "judge", Kind: StageWorker, Prompt: "decide", Model: "lead",
			Output: []PipelineField{{Name: "winner", Type: FieldString}}},
	}}
	renamed := base
	renamed.Name = "Debate v2"
	renamed.Description = "now with feeling"
	if pipelineFingerprint(base) != pipelineFingerprint(renamed) {
		t.Error("a rename must not read as a behaviour change, or every comparison is against a fresh hash")
	}

	edited := PipelineDef{Stages: []PipelineStage{
		{Name: "judge", Kind: StageWorker, Prompt: "decide, carefully", Model: "lead",
			Output: []PipelineField{{Name: "winner", Type: FieldString}}},
	}}
	if pipelineFingerprint(base) == pipelineFingerprint(edited) {
		t.Error("an edited prompt must change the fingerprint")
	}

	retiered := PipelineDef{Stages: []PipelineStage{
		{Name: "judge", Kind: StageWorker, Prompt: "decide", Model: "worker",
			Output: []PipelineField{{Name: "winner", Type: FieldString}}},
	}}
	if pipelineFingerprint(base) == pipelineFingerprint(retiered) {
		t.Error("a tier change must change the fingerprint")
	}
}

// Grading a tool runs it directly, so "it called the tool" is a tautology that
// would pass every time while looking like a real assertion.
func TestToolSuiteRefusesToolCallAssertions(t *testing.T) {
	s := goodSuite()
	s.TargetKind = EvalTargetTool
	s.Cases[0].MustCallTools = []string{"whatever"}
	err := s.Validate()
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "no model deciding") {
		t.Errorf("the error should say why it is vacuous: %v", err)
	}
	// The same assertion is legitimate on an agent, where a model chooses.
	s.TargetKind = EvalTargetAgent
	if err := s.Validate(); err != nil {
		t.Errorf("an agent suite may assert on tool calls: %v", err)
	}
}

// A field assertion is enough on its own — it was not counted as an assertion
// when MustFields was added, which would have refused every pipeline suite that
// graded only its declared shape.
func TestFieldAssertionAloneIsEnough(t *testing.T) {
	s := goodSuite()
	s.Cases = []EvalCase{{Name: "verdict", Prompt: "go", MustFields: map[string]string{"winner": "for"}}}
	if err := s.Validate(); err != nil {
		t.Errorf("a case asserting only on declared fields grades something: %v", err)
	}
}

// A tool case's prompt is ARGUMENTS, not a sentence: the point of grading a
// tool this way is asking whether it works without a model in the middle to be
// blamed for the answer.
func TestToolCasePromptIsJSONArguments(t *testing.T) {
	called := map[string]any(nil)
	tool := AgentToolDef{
		Tool:    Tool{Name: "adder"},
		Handler: func(args map[string]any) (string, error) { called = args; return "42", nil },
	}
	exec := toolEvalExecutor(tool)

	row := exec(context.Background(), EvalCase{Name: "adds", Prompt: `{"a":1,"b":2}`, MustInclude: []string{"42"}})
	if !row.Passed {
		t.Errorf("case should pass: %v", row.Reasons)
	}
	if called["a"] != float64(1) {
		t.Errorf("the prompt should reach the tool as arguments, got %v", called)
	}

	bad := exec(context.Background(), EvalCase{Name: "prose", Prompt: "please add one and two", MustInclude: []string{"42"}})
	if bad.Passed {
		t.Error("a prose prompt is not arguments and must fail rather than call the tool with nothing")
	}
	if !strings.Contains(bad.ErrText, "JSON arguments") {
		t.Errorf("the error should say what was expected: %q", bad.ErrText)
	}
}

// A description edit changes what a MODEL does with a tool even when the code
// behind it is untouched, and it is the most common change a tool ever gets.
func TestToolFingerprintCoversDescriptionAndParams(t *testing.T) {
	a := Tool{Name: "t", Description: "does a thing", Parameters: map[string]ToolParam{"x": {Type: "string"}}}
	b := a
	b.Description = "does a thing, carefully"
	c := Tool{Name: "t", Description: a.Description, Parameters: map[string]ToolParam{"x": {Type: "number"}}}

	fa := EvalTargetFingerprint("tool", a.Name, a.Description, toolParamSignature(a))
	fb := EvalTargetFingerprint("tool", b.Name, b.Description, toolParamSignature(b))
	fc := EvalTargetFingerprint("tool", c.Name, c.Description, toolParamSignature(c))
	if fa == fb {
		t.Error("a description edit must change the fingerprint")
	}
	if fa == fc {
		t.Error("a retyped parameter must change the fingerprint")
	}
	if toolParamSignature(a) != toolParamSignature(a) {
		t.Error("the signature must be stable across calls")
	}
}

// The migration is a COPY. Deleting somebody's saved cases to prove a point
// about primitives is not a migration, it is data loss with a rationale.
func TestLiftingAnAgentsCasesLeavesThemAlone(t *testing.T) {
	agent := AgentRecord{ID: "a1", Name: "Scribe", Evals: []EvalCase{
		{Name: "asks", Prompt: "compare these", JudgePrompt: "asks a clarifying question"},
	}}
	suite, err := EvalSuiteFromAgent(agent)
	if err != nil {
		t.Fatalf("lift: %v", err)
	}
	if suite.TargetKind != EvalTargetAgent || suite.TargetID != "a1" {
		t.Errorf("the suite should point back at the agent: %+v", suite)
	}
	if len(agent.Evals) != 1 {
		t.Error("the agent's own cases must be untouched")
	}
	// Copied, not aliased: editing one must not silently rewrite the other.
	suite.Cases[0].Name = "renamed in the suite"
	if agent.Evals[0].Name != "asks" {
		t.Error("editing the suite rewrote the agent's field — they share a backing array")
	}
	// A field that only ever ran once starts asking for enough runs to have a
	// rate; three is the smallest number that shows a flake as a flake.
	if suite.RunCount() < 3 {
		t.Errorf("lifted suite runs %d times, want enough to distinguish a flake", suite.RunCount())
	}
	if err := suite.Validate(); err != nil {
		t.Errorf("a lifted suite should be valid as-is: %v", err)
	}
}

func TestLiftingAnAgentWithNoCasesSaysSo(t *testing.T) {
	_, err := EvalSuiteFromAgent(AgentRecord{ID: "a1", Name: "Empty"})
	if err == nil {
		t.Fatal("expected an error rather than an empty suite that grades nothing")
	}
	if !strings.Contains(err.Error(), "no eval cases") {
		t.Errorf("the error should say what is missing: %v", err)
	}
}
