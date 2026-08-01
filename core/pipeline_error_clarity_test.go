package core

import (
	"strings"
	"testing"
)

// Six authoring rounds were spent on three error messages that each named a
// symptom without its fix. A pipeline is authored by someone who cannot see the
// validator, so the message IS the documentation at that moment.

func TestOutputTypeErrorNamesTheValidTypes(t *testing.T) {
	def := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "decompose", Kind: StageWorker, Prompt: "split",
			Output: []PipelineField{{Name: "items", Type: "array"}}},
	}}
	err := def.Validate()
	if err == nil {
		t.Fatal("array is not a field type — it must be refused")
	}
	for _, want := range []string{"string", "number", "bool", "list", "object"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name the valid type %q so the author stops guessing: %v", want, err)
		}
	}
	// The nearest right answer for the thing they were declaring.
	if !strings.Contains(err.Error(), "fan_over") {
		t.Errorf("a rejected list-shaped type should point at list + fan_over: %v", err)
	}
}

func TestFanOverRejectsThePromptTemplateForm(t *testing.T) {
	for _, ref := range []string{"{stage:decompose.items}", "{decompose.items}"} {
		def := PipelineDef{Name: "p", Stages: []PipelineStage{
			{Name: "decompose", Kind: StageWorker, Prompt: "split",
				Output: []PipelineField{{Name: "items", Type: FieldList}}},
			{Name: "dig", Kind: StageFanout, Prompt: "look at {item}", FanOver: ref},
		}}
		err := def.Validate()
		if err == nil {
			t.Fatalf("%s is a prompt template, not a fan_over reference", ref)
		}
		// It must say WHICH form belongs here, and show the corrected one.
		if !strings.Contains(err.Error(), "decompose.items") {
			t.Errorf("the error should show the bare form to use, got %v", err)
		}
		if !strings.Contains(err.Error(), "PROMPT") {
			t.Errorf("the error should say the braced form is for prompts, got %v", err)
		}
	}
	// The bare form still validates — the fix the error describes has to work.
	ok := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "decompose", Kind: StageWorker, Prompt: "split",
			Output: []PipelineField{{Name: "items", Type: FieldList}}},
		{Name: "dig", Kind: StageFanout, Prompt: "look at {item}", FanOver: "decompose.items"},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("the corrected form must validate: %v", err)
	}
}

func TestForwardReferenceExplainsStageOrder(t *testing.T) {
	def := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "dig", Kind: StageFanout, Prompt: "look at {item}", FanOver: "decompose.items"},
		{Name: "decompose", Kind: StageWorker, Prompt: "split",
			Output: []PipelineField{{Name: "items", Type: FieldList}}},
	}}
	err := def.Validate()
	if err == nil {
		t.Fatal("a stage cannot read one that runs after it")
	}
	// The rule, not just the symptom: order in the array is execution order.
	if !strings.Contains(err.Error(), "order") {
		t.Errorf("the error must explain that array order is run order: %v", err)
	}
	if !strings.Contains(err.Error(), "BACKWARD") {
		t.Errorf("the error must say which direction a reference may reach: %v", err)
	}
}

// A pipeline is authored as ONE object, so its mistakes arrive as a set. This
// is the exact shape of a real first submission: a wrong field type, an output
// on a stage that cannot carry one, and a fan_over written as a prompt
// template. Reported one at a time it cost three round-trips, each re-sending
// the whole definition; every message was right, and that was the problem.
func TestValidateReportsEveryIndependentProblemAtOnce(t *testing.T) {
	def := PipelineDef{Name: "Deep Dive Research", Stages: []PipelineStage{
		{Name: "Decompose", Kind: StageWorker, Prompt: "split {input}",
			Output: []PipelineField{{Name: "sub_questions", Type: "array"}}},
		{Name: "Investigate", Kind: StageFanout, Prompt: "search {item}",
			FanOver: "{stage:Decompose.sub_questions}",
			Output:  []PipelineField{{Name: "findings", Type: FieldList}}},
	}}
	err := def.Validate()
	if err == nil {
		t.Fatal("three problems, no error")
	}
	msg := err.Error()
	for _, want := range []string{
		`has unknown type "array"`,           // Decompose
		"output is not valid on kind=fanout", // Investigate
		"is written as a prompt template",    // Investigate, second problem
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("all independent problems must arrive together; missing %q in:\n%s", want, msg)
		}
	}
	if !strings.Contains(msg, "3 problems") {
		t.Errorf("say how many there are, so the author knows the list is the whole list:\n%s", msg)
	}
}

// One problem still reads as one sentence — a list of one is a worse message.
func TestValidateKeepsASingleProblemPlain(t *testing.T) {
	def := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "s", Kind: StageAgent, Prompt: "hi"},
	}}
	err := def.Validate()
	if err == nil {
		t.Fatal("an agent stage with no agent must fail")
	}
	if strings.Contains(err.Error(), "problems") || strings.Contains(err.Error(), "\n- ") {
		t.Errorf("a lone problem should not be dressed up as a list: %q", err)
	}
}

// Cascades stay suppressed: a stage whose OUTPUT failed must not also generate
// "declares no output field" for everything that reads it. One mistake, one
// line — otherwise the count is noise and the real fix is buried.
func TestValidateDoesNotCascadeFromABadOutput(t *testing.T) {
	def := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "first", Kind: StageWorker, Prompt: "go",
			Output: []PipelineField{{Name: "items", Type: "array"}}},
		{Name: "second", Kind: StageWorker, Prompt: "use {stage:first.items}"},
		{Name: "third", Kind: StageFanout, Prompt: "{item}", FanOver: "first.items"},
	}}
	err := def.Validate()
	if err == nil {
		t.Fatal("the bad output type must still be reported")
	}
	msg := err.Error()
	if !strings.Contains(msg, `has unknown type "array"`) {
		t.Errorf("the real problem is missing: %s", msg)
	}
	if strings.Contains(msg, "declares no output field") {
		t.Errorf("a reference into a stage whose output failed is that stage's problem, not a second one:\n%s", msg)
	}
	if strings.Contains(msg, "unknown stage") {
		t.Errorf("a stage that failed validation still EXISTS — referencing it is not an unknown-stage error:\n%s", msg)
	}
}

// Seven attempts at one loop, then the loop was abandoned for five hand-copied
// stage pairs that cannot stop early. These are the four messages that failed
// to teach, taken verbatim from that session.

// until is the least guessable thing in the vocabulary: it is not a condition,
// it is the NAME of a bool a body stage promised to return. The author reached
// for a comparison three times.
func TestUntilRefusalTeachesItsShape(t *testing.T) {
	body := []PipelineStage{
		{Name: "critic", Kind: StageWorker, Prompt: "critique {prev}",
			Output: []PipelineField{{Name: "feedback", Type: FieldString}}},
	}
	for _, until := range []string{
		"{stage:critic.feedback} == 'SATISFIED'", // what was written first
		"stage:critic.feedback == 'SATISFIED'",   // and second
		"critic.feedback == 'SATISFIED'",
	} {
		def := PipelineDef{Name: "p", Stages: []PipelineStage{
			{Name: "loop", Kind: StageLoop, Count: 5, Body: body, Until: until},
		}}
		err := def.Validate()
		if err == nil {
			t.Fatalf("until=%q is a condition, not a field reference", until)
		}
		msg := err.Error()
		if !strings.Contains(msg, `until:"check.done"`) {
			t.Errorf("every until refusal must show the shape; until=%q gave:\n%s", until, msg)
		}
		if !strings.Contains(msg, "not an expression") {
			t.Errorf("say that a comparison is not what until takes; until=%q gave:\n%s", until, msg)
		}
	}
	// A field that exists but is prose, not a bool — the same steer applies.
	def := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "loop", Kind: StageLoop, Count: 5, Body: body, Until: "critic.feedback"},
	}}
	if err := def.Validate(); err == nil || !strings.Contains(err.Error(), "not bool") {
		t.Errorf("a string field cannot end a loop: %v", err)
	}
	// And the correct shape validates, so the advice is followable.
	ok := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "loop", Kind: StageLoop, Count: 5, Until: "check.done", Body: []PipelineStage{
			{Name: "check", Kind: StageWorker, Prompt: "done?",
				Output: []PipelineField{{Name: "done", Type: FieldBool}}},
		}},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("the shape the error describes must validate: %v", err)
	}
}

// Reaching into a loop for the body stage that produced the result — four times
// in one session. "declares no output field writer.text" is true and teaches
// nothing; the loop answers under its own name.
func TestLoopResultIsReadByTheLoopsOwnName(t *testing.T) {
	def := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "polish", Kind: StageLoop, Count: 3, Body: []PipelineStage{
			{Name: "writer", Kind: StageWorker, Prompt: "rewrite {prev}"},
		}},
		{Name: "final", Kind: StageWorker, Prompt: "ship {stage:polish.writer.text}"},
	}}
	err := def.Validate()
	if err == nil {
		t.Fatal("a loop's body stage is not addressable from outside it")
	}
	msg := err.Error()
	if !strings.Contains(msg, "{stage:polish}") {
		t.Errorf("point at the loop's own name, got:\n%s", msg)
	}
	if !strings.Contains(msg, "each pass") {
		t.Errorf("say WHY a body name cannot be read from outside, got:\n%s", msg)
	}
}

// {stage:prev} — the right idea through the wrong door. Reporting "no stage
// named prev" is true and useless.
func TestBuiltinTokenWrittenAsAStageReference(t *testing.T) {
	def := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "one", Kind: StageWorker, Prompt: "hi"},
		{Name: "two", Kind: StageWorker, Prompt: "use {stage:prev}"},
	}}
	err := def.Validate()
	if err == nil {
		t.Fatal("prev is not a stage")
	}
	if !strings.Contains(err.Error(), "BUILT-IN") || !strings.Contains(err.Error(), "{prev}") {
		t.Errorf("name the built-in and its correct spelling, got: %v", err)
	}
}

// A loop body's problems belong to the pipeline's problem list, not wrapped as
// one entry inside it: nested, the count was wrong and the header printed
// twice — "this pipeline has 2 problems" followed by a bullet repeating it.
func TestLoopBodyProblemsFlattenIntoOneList(t *testing.T) {
	def := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "loop", Kind: StageLoop, Count: 3, Body: []PipelineStage{
			{Name: "a", Kind: StageWorker, Prompt: "x", Output: []PipelineField{{Name: "f", Type: "array"}}},
			{Name: "b", Kind: StageAgent, Prompt: "y"},
		}},
	}}
	err := def.Validate()
	if err == nil {
		t.Fatal("two body problems, no error")
	}
	msg := err.Error()
	if strings.Count(msg, "problems — fix them all") != 1 {
		t.Errorf("the header must appear exactly once, got:\n%s", msg)
	}
	if !strings.Contains(msg, "2 problems") {
		t.Errorf("the count must match the bullets listed, got:\n%s", msg)
	}
}

// when:"stage:critic.polished" is not a forward reference and not a typo — the
// stage it names is directly above it. But the whole "stage:critic" read as the
// NAME, nothing matched, and the answer was "has not run at that point … move
// it earlier": false, and unfollowable. The author reordered stages that were
// already in order, six times in one session.
func TestBarePrefixIsNamedRatherThanReportedAsOrdering(t *testing.T) {
	def := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "loop", Kind: StageLoop, Count: 5, Body: []PipelineStage{
			{Name: "critic", Kind: StageWorker, Prompt: "critique",
				Output: []PipelineField{{Name: "polished", Type: FieldBool}}},
			{Name: "check", Kind: StageBranch, When: "stage:critic.polished"},
		}},
	}}
	err := def.Validate()
	if err == nil {
		t.Fatal("a bare reference cannot carry the stage: prefix")
	}
	msg := err.Error()
	if !strings.Contains(msg, "stage: PREFIX") {
		t.Errorf("name the prefix as the problem, got:\n%s", msg)
	}
	if !strings.Contains(msg, `"critic.polished"`) {
		t.Errorf("show the corrected bare form, got:\n%s", msg)
	}
	if strings.Contains(msg, "move it earlier") {
		t.Errorf("this is NOT an ordering problem and must not be reported as one:\n%s", msg)
	}
	// The corrected form validates, so the advice can be followed. (Top level:
	// a branch inside a loop body may only skip within the pass, which is a
	// separate rule and not what this test is about.)
	ok := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "critic", Kind: StageWorker, Prompt: "critique",
			Output: []PipelineField{{Name: "polished", Type: FieldBool}}},
		{Name: "check", Kind: StageBranch, When: "critic.polished"},
		{Name: "ship", Kind: StageWorker, Prompt: "ship it"},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("the bare form must validate: %v", err)
	}
	// until shares the checker.
	u := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "loop", Kind: StageLoop, Count: 5, Until: "stage:check.done", Body: []PipelineStage{
			{Name: "check", Kind: StageWorker, Prompt: "done?",
				Output: []PipelineField{{Name: "done", Type: FieldBool}}},
		}},
	}}
	if err := u.Validate(); err == nil || !strings.Contains(err.Error(), "stage: PREFIX") {
		t.Errorf("until takes a bare reference too: %v", err)
	}
}

// {{handlebars}} resolved to nothing, stayed in the prompt verbatim, and the
// model was handed the braces — no error anywhere. An entire session was
// authored that way and validated clean.
func TestDoubleBracesAreRefusedRatherThanIgnored(t *testing.T) {
	def := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "one", Kind: StageWorker, Prompt: "polish this: {{input}}"},
	}}
	err := def.Validate()
	if err == nil {
		t.Fatal("{{input}} resolves to nothing — silently, which is the problem")
	}
	if !strings.Contains(err.Error(), "SINGLE") {
		t.Errorf("name the correct form, got: %v", err)
	}
	// Single braces are untouched.
	ok := PipelineDef{Name: "p", Stages: []PipelineStage{{Name: "one", Kind: StageWorker, Prompt: "polish: {input}"}}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("single braces are the correct form: %v", err)
	}
	// A tool stage's args are templated too, so they get the same check.
	args := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "t", Kind: StageTool, Tool: "calc", Args: map[string]string{"expr": "{{input}} + 1"}},
	}}
	if err := args.Validate(); err == nil || !strings.Contains(err.Error(), "SINGLE") {
		t.Errorf("args are templated and need the same guard: %v", err)
	}
}

// until detected a condition; when did not. So the same author, in the same
// build, wrote when:"{stage:critic}.lower().strip() == \"ok\"" and got the
// brace-stripper echoing garbage back, then wrote
// when:"critic.satisfied == true" and got "declares no output field
// satisfied == true" — true, unhelpful, and two of that build's six refusals.
func TestWhenDetectsAConditionLikeUntilDoes(t *testing.T) {
	for _, when := range []string{
		`{stage:critic}.lower().strip() == "satisfied"`,
		"critic.satisfied == true",
		"critic.satisfied != false",
	} {
		def := PipelineDef{Name: "p", Stages: []PipelineStage{
			{Name: "critic", Kind: StageWorker, Prompt: "review",
				Output: []PipelineField{{Name: "satisfied", Type: FieldBool}}},
			{Name: "check", Kind: StageBranch, When: when},
			{Name: "ship", Kind: StageWorker, Prompt: "ship"},
		}}
		err := def.Validate()
		if err == nil {
			t.Fatalf("when=%q is a condition, not a bool field name", when)
		}
		msg := err.Error()
		if !strings.Contains(msg, "written as a condition") {
			t.Errorf("when=%q must be named as a condition, got:\n%s", when, msg)
		}
		if !strings.Contains(msg, "NOT an expression") {
			t.Errorf("when=%q needs the shape spelled out, got:\n%s", when, msg)
		}
		if strings.Contains(msg, "declares no output field satisfied ==") {
			t.Errorf("when=%q must not report the expression as a field name:\n%s", when, msg)
		}
	}
	// The corrected form validates, so the advice is followable.
	ok := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "critic", Kind: StageWorker, Prompt: "review",
			Output: []PipelineField{{Name: "satisfied", Type: FieldBool}}},
		{Name: "check", Kind: StageBranch, When: "critic.satisfied"},
		{Name: "ship", Kind: StageWorker, Prompt: "ship"},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("the bare bool reference must validate: %v", err)
	}
}

// Wanting to name a loop's result is reasonable — the old refusal said only
// that the slot was wrong, and the author's next move was another rejected
// create. Downstream, {stage:LOOPNAME} already IS that result.
func TestLoopOutputRefusalNamesTheReplacement(t *testing.T) {
	def := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "refine", Kind: StageLoop, Count: 5,
			Output: []PipelineField{{Name: "final_draft", Type: FieldString}},
			Body: []PipelineStage{
				{Name: "writer", Kind: StageWorker, Prompt: "rewrite {prev}"},
			}},
	}}
	err := def.Validate()
	if err == nil {
		t.Fatal("output on a loop must still refuse")
	}
	if !strings.Contains(err.Error(), "{stage:refine}") {
		t.Errorf("name the reference that replaces it, got:\n%s", err)
	}
	if !strings.Contains(err.Error(), "BODY stage") {
		t.Errorf("say where output does belong, got:\n%s", err)
	}
}
