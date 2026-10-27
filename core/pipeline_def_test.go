package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cmcoffee/gohort/core/sources"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The branch stage: read a bool an earlier stage declared, then either
// end the pipeline or skip forward past stages that no longer apply.
// The two shapes it exists for are "input rejected, stop" and "this was
// supplied already, skip the stage that derives it".

func branchDef(skipTo string) PipelineDef {
	return PipelineDef{Stages: []PipelineStage{
		{Name: "frame", Kind: StageAgent, Agent: "framer", Prompt: "frame {input}",
			Output: []PipelineField{{Name: "rejected", Type: FieldBool, Required: true}}},
		{Name: "gate", Kind: StageBranch, When: "frame.rejected", SkipTo: skipTo},
		{Name: "work", Kind: StageAgent, Agent: "worker", Prompt: "do the work"},
		{Name: "report", Kind: StageAgent, Agent: "reporter", Prompt: "report on {prev}"},
	}}
}

// --- validation -------------------------------------------------------

func TestValidate_BranchShape(t *testing.T) {
	if err := branchDef("").Validate(); err != nil {
		t.Fatalf("stop-form branch rejected: %v", err)
	}
	if err := branchDef("report").Validate(); err != nil {
		t.Fatalf("skip-form branch rejected: %v", err)
	}

	bool1 := PipelineStage{Name: "frame", Prompt: "x",
		Output: []PipelineField{{Name: "rejected", Type: FieldBool}}}
	bad := map[string]PipelineDef{
		"no when": {Stages: []PipelineStage{
			bool1, {Name: "gate", Kind: StageBranch},
		}},
		"when names a stage, not a field": {Stages: []PipelineStage{
			bool1, {Name: "gate", Kind: StageBranch, When: "frame"},
		}},
		"when is not bool": {Stages: []PipelineStage{
			{Name: "frame", Prompt: "x", Output: []PipelineField{{Name: "note", Type: FieldString}}},
			{Name: "gate", Kind: StageBranch, When: "frame.note"},
		}},
		"when references a later stage": {Stages: []PipelineStage{
			{Name: "gate", Kind: StageBranch, When: "frame.rejected"}, bool1,
		}},
		"skip_to is unknown": {Stages: []PipelineStage{
			bool1, {Name: "gate", Kind: StageBranch, When: "frame.rejected", SkipTo: "nowhere"},
		}},
		// Backward jumps are iteration, and iteration is kind=loop where
		// count bounds it — allowing one here would be unbounded looping
		// through the back door.
		"skip_to points backwards": {Stages: []PipelineStage{
			bool1,
			{Name: "earlier", Prompt: "x"},
			{Name: "gate", Kind: StageBranch, When: "frame.rejected", SkipTo: "earlier"},
		}},
		"skip_to points at itself": {Stages: []PipelineStage{
			bool1, {Name: "gate", Kind: StageBranch, When: "frame.rejected", SkipTo: "gate"},
		}},
		"branch declares output": {Stages: []PipelineStage{
			bool1, {Name: "gate", Kind: StageBranch, When: "frame.rejected",
				Output: []PipelineField{{Name: "q"}}},
		}},
		"when on a non-branch stage": {Stages: []PipelineStage{
			bool1, {Name: "other", Prompt: "x", When: "frame.rejected"},
		}},
	}
	for name, def := range bad {
		if err := def.Validate(); err == nil {
			t.Errorf("%s: expected a validation error, got nil", name)
		}
	}
}

func TestValidate_BranchInLoopBody(t *testing.T) {
	body := []PipelineStage{
		{Name: "check", Prompt: "x", Output: []PipelineField{{Name: "bad", Type: FieldBool}}},
		{Name: "gate", Kind: StageBranch, When: "check.bad", SkipTo: "tail"},
		{Name: "mid", Prompt: "x"},
		{Name: "tail", Prompt: "x"},
	}
	ok := PipelineDef{Stages: []PipelineStage{
		{Name: "rounds", Kind: StageLoop, Count: 2, Body: body},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("skip-within-body should be allowed: %v", err)
	}
	// Ending the PIPELINE from inside a pass is ambiguous — the loop
	// already has until for stopping early.
	stopInBody := PipelineDef{Stages: []PipelineStage{
		{Name: "rounds", Kind: StageLoop, Count: 2, Body: []PipelineStage{
			{Name: "check", Prompt: "x", Output: []PipelineField{{Name: "bad", Type: FieldBool}}},
			{Name: "gate", Kind: StageBranch, When: "check.bad"},
		}},
	}}
	err := stopInBody.Validate()
	if err == nil {
		t.Fatal("a pipeline-ending branch inside a loop body should be rejected")
	}
	if !strings.Contains(err.Error(), "until") {
		t.Errorf("the error should point at the loop's until: %v", err)
	}
}

// --- execution --------------------------------------------------------

func TestBranch_StopsThePipeline(t *testing.T) {
	rec := &recorder{reply: func(agent, _ string, _ int) string {
		if agent == "framer" {
			return `{"rejected": true}`
		}
		return "should not run"
	}}
	app := &AppCore{}
	out, err := app.executePipelineDef(context.Background(), branchDef(""), "topic", rec.fn, nil, nil)
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if len(rec.prompts) != 1 {
		t.Errorf("only the framing stage should have run, got %d calls: %v", len(rec.prompts), rec.prompts)
	}
	// Stopping returns whatever the last stage produced, so the rejection
	// itself is the pipeline's answer.
	if !strings.Contains(out, "rejected") {
		t.Errorf("stop should return the last stage's output, got %q", out)
	}
}

func TestBranch_FallsThroughWhenFalse(t *testing.T) {
	rec := &recorder{reply: func(agent, _ string, _ int) string {
		if agent == "framer" {
			return `{"rejected": false}`
		}
		return "did " + agent
	}}
	app := &AppCore{}
	out, err := app.executePipelineDef(context.Background(), branchDef(""), "topic", rec.fn, nil, nil)
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if len(rec.prompts) != 3 {
		t.Errorf("all three stages should have run, got %d", len(rec.prompts))
	}
	if out != "did reporter" {
		t.Errorf("output = %q", out)
	}
}

func TestBranch_SkipsForward(t *testing.T) {
	rec := &recorder{reply: func(agent, _ string, _ int) string {
		if agent == "framer" {
			return `{"rejected": true}`
		}
		return "did " + agent
	}}
	app := &AppCore{}
	out, err := app.executePipelineDef(context.Background(), branchDef("report"), "topic", rec.fn, nil, nil)
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	// framer + reporter — "work" is jumped over.
	if len(rec.prompts) != 2 {
		t.Fatalf("expected framer + reporter, got %d: %v", len(rec.prompts), rec.prompts)
	}
	if !strings.Contains(rec.prompts[1], "report on") {
		t.Errorf("second call should be the reporter: %q", rec.prompts[1])
	}
	if out != "did reporter" {
		t.Errorf("output = %q", out)
	}
}

func TestBranch_MissingSourceReadsAsFalse(t *testing.T) {
	// A chain of branches where the first skips past the stage the second
	// reads. Falling through (running the work) is the safe direction;
	// treating an absent value as true would skip real work.
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "a", Kind: StageAgent, Agent: "x", Prompt: "p",
			Output: []PipelineField{{Name: "skip", Type: FieldBool}}},
		{Name: "g1", Kind: StageBranch, When: "a.skip", SkipTo: "g2"},
		{Name: "b", Kind: StageAgent, Agent: "y", Prompt: "p",
			Output: []PipelineField{{Name: "stop", Type: FieldBool}}},
		{Name: "g2", Kind: StageBranch, When: "b.stop"},
		{Name: "tail", Kind: StageAgent, Agent: "z", Prompt: "p"},
	}}
	if err := def.Validate(); err != nil {
		t.Fatalf("def should validate: %v", err)
	}
	rec := &recorder{reply: func(agent, _ string, _ int) string {
		if agent == "x" {
			return `{"skip": true}`
		}
		return "ran " + agent
	}}
	app := &AppCore{}
	out, err := app.executePipelineDef(context.Background(), def, "in", rec.fn, nil, nil)
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if out != "ran z" {
		t.Errorf("g2 should have read false and fallen through, got %q", out)
	}
}

// A stage can be drawn as a card of its values rather than as text: what a
// debate's verdict or a scored item looks like, declared as data so an app
// Builder writes can have it without any code.

// A card stage's block says how it is drawn, and its fields follow as the
// block closes, so the surface draws values instead of the text rendering.
func TestACardStageSendsItsLayoutAndFields(t *testing.T) {
	def := PipelineDef{Name: "verdict", Stages: []PipelineStage{{
		Name: "judge", Kind: StageAgent, Agent: "judge", Prompt: "Judge {input}",
		Output: []PipelineField{{Name: "verdict"}, {Name: "confidence"}},
		Render: "card", Card: map[string]string{"title": "verdict", "badges": "confidence"},
	}}}
	events := collectEvents(t, def, func(ctx context.Context, agent, prompt string) (string, error) {
		return `{"verdict":"Ship it","confidence":"high"}`, nil
	})
	var block, fields *PipelineEvent
	for i := range events {
		switch events[i].Kind {
		case "block":
			block = &events[i]
		case "fields":
			fields = &events[i]
		}
	}
	if block == nil || block.Type != "card" || block.Card["title"] != "verdict" {
		t.Fatalf("the block must name its renderer and carry its layout: %+v", block)
	}
	if fields == nil || fields.ID != block.ID || fields.Fields["verdict"] != "Ship it" || fields.Fields["confidence"] != "high" {
		t.Fatalf("the stage's fields must follow on the same block: %+v", fields)
	}
}

// A stage that does not ask for a card is drawn exactly as before.
func TestAStageWithoutRenderKeepsItsKind(t *testing.T) {
	def := PipelineDef{Name: "plain", Stages: []PipelineStage{{Name: "ask", Kind: StageAgent, Agent: "a", Prompt: "{input}"}}}
	for _, ev := range collectEvents(t, def, func(context.Context, string, string) (string, error) { return "an answer", nil }) {
		if ev.Kind == "block" && (ev.Type != string(StageAgent) || ev.Card != nil) {
			t.Errorf("a plain stage's block changed: %+v", ev)
		}
		if ev.Kind == "fields" {
			t.Errorf("a stage with no declared output has no fields to send: %+v", ev)
		}
	}
}

// A panel drawn as cards shows each voice in each round as its own card, the
// way a debate shows its arguments; the stage's own block is the heading.
func TestAPanelDrawnAsCardsHasOneCardPerVoicePerRound(t *testing.T) {
	def := PipelineDef{Name: "argue", Stages: []PipelineStage{{
		Name: "debate", Kind: StagePanel, Count: 2, Panel: []string{"For", "Against"},
		Prompt: "You are {voice}. {input}",
		Render: "card", Card: map[string]string{"accent": "voice", "body": "text"},
	}}}
	events := collectEvents(t, def, func(ctx context.Context, voice, prompt string) (string, error) {
		return voice + " argues its case", nil
	})
	var heading *PipelineEvent
	cards := map[string]map[string]any{}
	for i, ev := range events {
		if ev.Kind == "block" && ev.Title == "debate" {
			heading = &events[i]
		}
		if ev.Kind == "fields" && ev.Fields["voice"] != nil {
			cards[ev.ID] = ev.Fields
		}
	}
	if heading == nil || heading.Type != "text" {
		t.Fatalf("the panel's own block becomes a heading: %+v", heading)
	}
	for _, ev := range events {
		if ev.Kind == "chunk" && ev.ID == heading.ID {
			t.Errorf("the heading must not repeat what the cards show: %q", ev.Text)
		}
	}
	if len(cards) != 4 {
		t.Fatalf("two voices over two rounds is four cards, got %d", len(cards))
	}
	seen := map[string]bool{}
	for _, f := range cards {
		seen[fmt.Sprintf("%v/%v", f["voice"], f["round"])] = true
		if !strings.HasSuffix(f["text"].(string), "argues its case") {
			t.Errorf("a card carries what its voice said: %v", f)
		}
	}
	if len(seen) != 4 {
		t.Errorf("each voice once per round, got %v", seen)
	}
}

// A card that names a field the stage never produces would draw an empty space
// where its author expects a value; refused when the pipeline is saved.
func TestCardLayoutsAreCheckedOnSave(t *testing.T) {
	stage := func(s PipelineStage) PipelineDef {
		if s.Name == "" {
			s.Name = "s"
		}
		if s.Kind == "" {
			s.Kind = StageWorker
		}
		if s.Prompt == "" {
			s.Prompt = "x"
		}
		return PipelineDef{Name: "d", Stages: []PipelineStage{s}}
	}
	out := []PipelineField{{Name: "verdict"}, {Name: "why"}}
	for _, bad := range []PipelineDef{
		stage(PipelineStage{Output: out, Card: map[string]string{"title": "verdict"}}),                  // card without render=card
		stage(PipelineStage{Output: out, Render: "card", Card: map[string]string{"title": "missing"}}),  // unknown field
		stage(PipelineStage{Output: out, Render: "card", Card: map[string]string{"footer": "verdict"}}), // unknown part
		stage(PipelineStage{Output: out, Render: "card", Card: map[string]string{"title": "verdict, why"}}),
		stage(PipelineStage{Render: "Card <b>"}),
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("should be refused: %+v", bad.Stages[0])
		}
	}
	for _, good := range []PipelineDef{
		stage(PipelineStage{Output: out, Render: "card", Card: map[string]string{"title": "verdict", "badges": "why, verdict", "body": "why", "accent": "verdict"}}),
		stage(PipelineStage{Kind: StagePanel, Panel: []string{"a", "b"}, Count: 1, Render: "card", Card: map[string]string{"accent": "voice", "body": "text"}}),
		stage(PipelineStage{Render: "my_block"}),
	} {
		if err := good.Validate(); err != nil {
			t.Errorf("should be accepted: %v", err)
		}
	}
}

// A stored run keeps each block's card and fields, so opening it later draws
// the same card it streamed; live, the fields go out as block_meta.
func TestARunKeepsItsCards(t *testing.T) {
	s := runSurface(t, func(ctx context.Context, input string, _ map[string]string, sink PipelineSink) (string, error) {
		sink(PipelineEvent{Kind: "block", ID: "b1", Type: "card", Title: "judge", Card: map[string]string{"title": "verdict"}})
		sink(PipelineEvent{Kind: "fields", ID: "b1", Fields: map[string]any{"verdict": "Ship it"}})
		sink(PipelineEvent{Kind: "block_done", ID: "b1"})
		return "done", nil
	})
	id, rec := startRun(t, s)
	if !strings.Contains(rec.Body.String(), "event: block_meta") || !strings.Contains(rec.Body.String(), `"verdict":"Ship it"`) {
		t.Errorf("the fields must stream as block_meta:\n%s", rec.Body.String())
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if run, _ := LoadPipelineRun(s.DB, s.User, s.OwnerID, id); !run.Running {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := httptest.NewRecorder()
	(&AppCore{}).ServeRuns(got, httptest.NewRequest(http.MethodGet, "/sessions/"+id, nil), s, "sessions/"+id)
	var one struct {
		Blocks []PipelineRunBlock
	}
	_ = json.Unmarshal(got.Body.Bytes(), &one)
	if len(one.Blocks) != 1 || one.Blocks[0].Card["title"] != "verdict" || one.Blocks[0].Fields["verdict"] != "Ship it" {
		t.Errorf("the stored block must keep its card and fields: %+v", one.Blocks)
	}
}

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
	if strings.Count(msg, "problems, fix them all") != 1 {
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

// A fanout that runs SEVERAL stages per item.
//
// The single-prompt fan does breadth with one call per branch. A body
// makes each branch a small pipeline, which is what "investigate each of
// these properly, then compare what came back" needs. The tests that
// matter are the ones about SCOPE: branches run in parallel, so two
// branches running a stage of the same name must not see each other.

// --- validation -------------------------------------------------------

func TestValidate_FanoutBodyShape(t *testing.T) {
	body := []PipelineStage{{Name: "look", Prompt: "look at {item}"}}
	ok := PipelineDef{Stages: []PipelineStage{
		{Name: "split", Prompt: "split", Output: []PipelineField{{Name: "parts", Type: FieldList}}},
		{Name: "dig", Kind: StageFanout, FanOver: "split.parts", Body: body},
		{Name: "wrap", Prompt: "read {stage:dig}"},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a valid fanout body was rejected: %v", err)
	}

	bad := map[string]PipelineDef{
		"a body and an agent both": {Stages: []PipelineStage{
			{Name: "split", Prompt: "s", Output: []PipelineField{{Name: "parts", Type: FieldList}}},
			{Name: "dig", Kind: StageFanout, FanOver: "split.parts", Agent: "w", Body: body},
		}},
		"a body inside a body": {Stages: []PipelineStage{
			{Name: "split", Prompt: "s", Output: []PipelineField{{Name: "parts", Type: FieldList}}},
			{Name: "dig", Kind: StageFanout, FanOver: "split.parts", Body: []PipelineStage{
				{Name: "inner", Kind: StageFanout, FanOver: "split.parts", Body: body},
			}},
		}},
		"a loop inside a fanout body": {Stages: []PipelineStage{
			{Name: "split", Prompt: "s", Output: []PipelineField{{Name: "parts", Type: FieldList}}},
			{Name: "dig", Kind: StageFanout, FanOver: "split.parts", Body: []PipelineStage{
				{Name: "rounds", Kind: StageLoop, Count: 2, Body: body},
			}},
		}},
		// A branch name means "whichever branch won the race" from
		// outside, so the reference is refused at save time.
		"an outer stage references a body stage": {Stages: []PipelineStage{
			{Name: "split", Prompt: "s", Output: []PipelineField{{Name: "parts", Type: FieldList}}},
			{Name: "dig", Kind: StageFanout, FanOver: "split.parts", Body: body},
			{Name: "after", Prompt: "read {stage:look}"},
		}},
		"a body on a plain stage": {Stages: []PipelineStage{
			{Name: "plain", Prompt: "x", Body: body},
		}},
	}
	for name, def := range bad {
		if err := def.Validate(); err == nil {
			t.Errorf("%s: expected a validation error, got nil", name)
		}
	}
}

// --- execution --------------------------------------------------------

// twoItemFan is the fixture the scope tests share: a stage that produces
// two items, and a fan whose body looks then judges what it looked at.
func twoItemFan(body []PipelineStage) PipelineDef {
	return PipelineDef{Stages: []PipelineStage{
		{Name: "split", Kind: StageAgent, Agent: "splitter", Prompt: "split",
			Output: []PipelineField{{Name: "parts", Type: FieldList}}},
		{Name: "dig", Kind: StageFanout, FanOver: "split.parts", Body: body},
	}}
}

// THE test. Both branches run a stage called "look", and the stage that
// reads {stage:look} must see ITS OWN branch's value. A shared outputs map
// would let branch 2 overwrite branch 1 mid-flight.
func TestFanoutBody_EachBranchReadsItsOwnStages(t *testing.T) {
	def := twoItemFan([]PipelineStage{
		{Name: "look", Kind: StageAgent, Agent: "w", Prompt: "look at {item}"},
		{Name: "judge", Kind: StageAgent, Agent: "w", Prompt: "judging: {stage:look}"},
	})
	rec := &recorder{reply: func(agent, prompt string, _ int) string {
		switch {
		case agent == "splitter":
			return `{"parts": ["apples", "pears"]}`
		case strings.HasPrefix(prompt, "look at "):
			return "SAW-" + strings.TrimPrefix(prompt, "look at ")
		default:
			return prompt
		}
	}}
	out, err := new(AppCore).executePipelineDef(context.Background(), def, "seed", rec.fn, nil, nil)
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}

	var judged []string
	for _, p := range rec.prompts {
		if strings.HasPrefix(p, "judging: ") {
			judged = append(judged, p)
		}
	}
	sort.Strings(judged)
	if len(judged) != 2 {
		t.Fatalf("expected one judge per branch, got %v", judged)
	}
	if judged[0] != "judging: SAW-apples" || judged[1] != "judging: SAW-pears" {
		t.Errorf("a branch read another branch's stage: %v", judged)
	}
	// The joined block still labels each branch by its item.
	if !strings.Contains(out, "## Item 1: apples") || !strings.Contains(out, "## Item 2: pears") {
		t.Errorf("the joined block should name each item:\n%s", out)
	}
}

// A branch reads what was established BEFORE the fan, which is what makes
// a copied scope different from an empty one.
func TestFanoutBody_ReadsStagesFromBeforeTheFan(t *testing.T) {
	def := twoItemFan([]PipelineStage{
		{Name: "look", Kind: StageAgent, Agent: "w", Prompt: "for {item}, the plan said {stage:split.parts}"},
	})
	rec := &recorder{reply: func(agent, _ string, _ int) string {
		if agent == "splitter" {
			return `{"parts": ["apples", "pears"]}`
		}
		return "ok"
	}}
	if _, err := new(AppCore).executePipelineDef(context.Background(), def, "seed", rec.fn, nil, nil); err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	for _, p := range rec.prompts {
		if strings.HasPrefix(p, "for ") && !strings.Contains(p, "apples") {
			t.Errorf("a branch could not see the stage that ran before the fan: %q", p)
		}
	}
}

// The structured half: one object per branch, carrying its branch number
// and the item it ran on, so a following stage can rank or re-fan.
func TestFanoutBody_CollectsDeclaredShapesPerBranch(t *testing.T) {
	def := twoItemFan([]PipelineStage{
		{Name: "look", Kind: StageAgent, Agent: "w", Prompt: "look at {item}"},
		{Name: "score", Kind: StageAgent, Agent: "scorer", Prompt: "score {item}",
			Output: []PipelineField{{Name: "verdict", Type: FieldString}}},
	})
	rec := &recorder{reply: func(agent, prompt string, _ int) string {
		switch {
		case agent == "splitter":
			return `{"parts": ["apples", "pears"]}`
		case agent == "scorer" && strings.Contains(prompt, "apples"):
			return `{"verdict": "sweet"}`
		case agent == "scorer":
			return `{"verdict": "gritty"}`
		default:
			return "looked"
		}
	}}
	out, r, err := new(AppCore).executePipelineDefRun(context.Background(), def, "seed", nil, rec.fn, nil, nil)
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	_ = out
	items, _ := r.outputs["dig"].Fields["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("want one entry per branch, got %#v", r.outputs["dig"].Fields)
	}
	byItem := map[string]map[string]any{}
	for _, it := range items {
		m, _ := it.(map[string]any)
		byItem[strings.TrimSpace(renderFieldValue(m["item"]))] = m
	}
	if byItem["apples"]["verdict"] != "sweet" || byItem["pears"]["verdict"] != "gritty" {
		t.Errorf("each branch's own shape should survive: %#v", items)
	}
	if byItem["apples"]["branch"] != 1 || byItem["pears"]["branch"] != 2 {
		t.Errorf("entries should carry their branch number: %#v", items)
	}
}

// A body that ends in prose is a normal fan: text, and nothing to merge.
func TestFanoutBody_NoDeclaredShapeMeansNoItems(t *testing.T) {
	def := twoItemFan([]PipelineStage{
		{Name: "look", Kind: StageAgent, Agent: "w", Prompt: "look at {item}"},
	})
	rec := &recorder{reply: func(agent, _ string, _ int) string {
		if agent == "splitter" {
			return `{"parts": ["apples", "pears"]}`
		}
		return "prose"
	}}
	_, r, err := new(AppCore).executePipelineDefRun(context.Background(), def, "seed", nil, rec.fn, nil, nil)
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if fields := r.outputs["dig"].Fields; fields != nil {
		t.Errorf("a prose fan has no shape to expose, got %#v", fields)
	}
}

// One bad branch does not sink the fan: the failure is recorded in its own
// slot and the others complete.
func TestFanoutBody_OneBranchFailingLeavesTheRest(t *testing.T) {
	def := twoItemFan([]PipelineStage{
		{Name: "look", Kind: StageAgent, Agent: "w", Prompt: "look at {item}"},
	})
	rec := &recorder{reply: func(agent, prompt string, _ int) string {
		if agent == "splitter" {
			return `{"parts": ["apples", "pears"]}`
		}
		return "fine: " + prompt
	}}
	failing := func(ctx context.Context, agent, prompt string) (string, error) {
		if strings.Contains(prompt, "pears") {
			return "", Error("the pear shop was closed")
		}
		return rec.fn(ctx, agent, prompt)
	}
	out, err := new(AppCore).executePipelineDef(context.Background(), def, "seed", failing, nil, nil)
	if err != nil {
		t.Fatalf("one bad branch should not fail the run: %v", err)
	}
	if !strings.Contains(out, "branch error") || !strings.Contains(out, "pear shop was closed") {
		t.Errorf("the failing branch should say so in its own slot:\n%s", out)
	}
	if !strings.Contains(out, "fine: look at apples") {
		t.Errorf("the other branch should have completed:\n%s", out)
	}
}

// The payoff for the collection: a later fan runs over what the first one
// scored, which is "investigate, rank, then go deeper on the survivors".
func TestFanoutBody_CanFanOverAPreviousFansItems(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "split", Kind: StageAgent, Agent: "splitter", Prompt: "split",
			Output: []PipelineField{{Name: "parts", Type: FieldList}}},
		{Name: "dig", Kind: StageFanout, FanOver: "split.parts", Body: []PipelineStage{
			{Name: "score", Kind: StageAgent, Agent: "scorer", Prompt: "score {item}",
				Output: []PipelineField{{Name: "verdict", Type: FieldString}}},
		}},
		// An agent branch, because a worker branch would want a live model.
		{Name: "deepen", Kind: StageFanout, FanOver: "dig.items", Agent: "w", Prompt: "deepen {item}"},
	}}
	if err := def.Validate(); err != nil {
		t.Fatalf("fanning over a previous fan should validate: %v", err)
	}
	rec := &recorder{reply: func(agent, _ string, _ int) string {
		switch agent {
		case "splitter":
			return `{"parts": ["apples", "pears"]}`
		case "scorer":
			return `{"verdict": "ok"}`
		}
		return "done"
	}}
	if _, err := new(AppCore).executePipelineDef(context.Background(), def, "seed", rec.fn, nil, nil); err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
}

// Back-compat: a fanout with no body is the fan that already shipped.
func TestFanoutWithoutABodyIsUnchanged(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "split", Kind: StageAgent, Agent: "splitter", Prompt: "split",
			Output: []PipelineField{{Name: "parts", Type: FieldList}}},
		{Name: "dig", Kind: StageFanout, FanOver: "split.parts", Agent: "w", Prompt: "dig into {item}"},
	}}
	rec := &recorder{reply: func(agent, prompt string, _ int) string {
		if agent == "splitter" {
			return `{"parts": ["apples", "pears"]}`
		}
		return "got: " + prompt
	}}
	out, r, err := new(AppCore).executePipelineDefRun(context.Background(), def, "seed", nil, rec.fn, nil, nil)
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if !strings.Contains(out, "got: dig into apples") || !strings.Contains(out, "got: dig into pears") {
		t.Errorf("the single-prompt fan should be untouched:\n%s", out)
	}
	if r.outputs["dig"].Fields != nil {
		t.Error("a fan with no body has no shapes to collect")
	}
}

// A stage field taken from a variable rather than asked for. The shared
// PipelineField grew From for machines; the pipeline tool learned to
// decode it before the pipeline runtime honoured it, which is a
// documented feature that does nothing.

func TestAStageFieldCanBeFilledRatherThanAsked(t *testing.T) {
	stage := PipelineStage{Name: "triage", Kind: StageAgent, Agent: "w", Prompt: "sort it",
		Output: []PipelineField{
			{Name: "asked", Type: FieldString, From: "{input}"},
			{Name: "lane", Type: FieldString, Desc: "which lane"},
		}}

	// The model is asked for exactly what it must answer.
	model := stage.ModelOutput()
	if len(model) != 1 || model[0].Name != "lane" {
		t.Fatalf("a filled field must not be asked for: %+v", model)
	}
	if static := stage.StaticFields(); len(static) != 1 || static[0].Name != "asked" {
		t.Fatalf("and it must still be part of what the stage establishes: %+v", static)
	}

	// End to end: the filled field lands in the stage's result, where
	// {stage:triage.asked} reads it like any other.
	def := PipelineDef{Name: "p", Stages: []PipelineStage{
		stage,
		{Name: "reply", Kind: StageAgent, Agent: "w",
			Prompt: "the question was {stage:triage.asked}, lane {stage:triage.lane}"},
	}}
	rec := &recorder{reply: func(_, _ string, n int) string {
		if n == 1 {
			return `{"lane":"deep"}`
		}
		return "done"
	}}
	if _, err := (&AppCore{}).RunPipelineDefSync(context.Background(), def,
		"why is the export failing?", rec.fn, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(rec.prompts) != 2 {
		t.Fatalf("expected two stages to run, got %d", len(rec.prompts))
	}
	if !strings.Contains(rec.prompts[1], "the question was why is the export failing?") {
		t.Errorf("the filled field should reach a later stage:\n%s", rec.prompts[1])
	}
	if !strings.Contains(rec.prompts[1], "lane deep") {
		t.Errorf("alongside the ones the model answered:\n%s", rec.prompts[1])
	}
	// And the model was never shown the field it did not have to answer.
	if strings.Contains(rec.prompts[0], `"asked"`) {
		t.Errorf("a filled field must not appear in the contract:\n%s", rec.prompts[0])
	}
}

// The loop stage: repeat a body, thread each pass into the next, stop on
// count or on an Until field going true.
//
// Where fanout does breadth (N independent branches in parallel), loop
// does depth — the carry between passes is the whole point, so these
// tests assert the carry as much as the iteration count.

// --- validation -------------------------------------------------------

func TestValidate_LoopShape(t *testing.T) {
	body := []PipelineStage{{Name: "step", Prompt: "do {iteration}"}}
	ok := PipelineDef{Stages: []PipelineStage{
		{Name: "rounds", Kind: StageLoop, Count: 3, Body: body},
		{Name: "wrap", Prompt: "summarize {stage:rounds}"},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid loop rejected: %v", err)
	}

	bad := map[string]PipelineDef{
		"no body": {Stages: []PipelineStage{
			{Name: "rounds", Kind: StageLoop, Count: 2},
		}},
		"no count": {Stages: []PipelineStage{
			{Name: "rounds", Kind: StageLoop, Body: body},
		}},
		"count over the ceiling": {Stages: []PipelineStage{
			{Name: "rounds", Kind: StageLoop, Count: loopMaxIterations + 1, Body: body},
		}},
		"body on a non-loop": {Stages: []PipelineStage{
			{Name: "plain", Body: body},
		}},
		"output on a loop": {Stages: []PipelineStage{
			{Name: "rounds", Kind: StageLoop, Count: 2, Body: body,
				Output: []PipelineField{{Name: "q"}}},
		}},
		"bad collect": {Stages: []PipelineStage{
			{Name: "rounds", Kind: StageLoop, Count: 2, Body: body, Collect: "some"},
		}},
		"loops do not nest": {Stages: []PipelineStage{
			{Name: "outer", Kind: StageLoop, Count: 2, Body: []PipelineStage{
				{Name: "inner", Kind: StageLoop, Count: 2, Body: []PipelineStage{{Name: "deep", Prompt: "x"}}},
			}},
		}},
		// A body name is per-pass, so a reference from outside would mean
		// "whatever the last pass left" — caught at save time instead.
		"outer stage references a body stage": {Stages: []PipelineStage{
			{Name: "rounds", Kind: StageLoop, Count: 2, Body: body},
			{Name: "after", Prompt: "look at {stage:step}"},
		}},
	}
	for name, def := range bad {
		if err := def.Validate(); err == nil {
			t.Errorf("%s: expected a validation error, got nil", name)
		}
	}
}

func TestValidate_LoopUntil(t *testing.T) {
	checker := PipelineStage{Name: "check", Prompt: "done?",
		Output: []PipelineField{{Name: "done", Type: FieldBool}}}

	ok := PipelineDef{Stages: []PipelineStage{
		{Name: "rounds", Kind: StageLoop, Count: 5, Until: "check.done",
			Body: []PipelineStage{{Name: "work", Prompt: "x"}, checker}},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid until rejected: %v", err)
	}

	bad := map[string]PipelineDef{
		"until names a stage, not a field": {Stages: []PipelineStage{
			{Name: "rounds", Kind: StageLoop, Count: 5, Until: "check",
				Body: []PipelineStage{checker}},
		}},
		"until field is not bool": {Stages: []PipelineStage{
			{Name: "rounds", Kind: StageLoop, Count: 5, Until: "check.note",
				Body: []PipelineStage{{Name: "check", Prompt: "x",
					Output: []PipelineField{{Name: "note", Type: FieldString}}}}},
		}},
		"until field is not declared": {Stages: []PipelineStage{
			{Name: "rounds", Kind: StageLoop, Count: 5, Until: "work.done",
				Body: []PipelineStage{{Name: "work", Prompt: "x"}}},
		}},
		// An outer field can't change between passes, so the loop would
		// run once or all N times — never what the author meant.
		"until points outside the loop": {Stages: []PipelineStage{
			{Name: "gate", Prompt: "x", Output: []PipelineField{{Name: "done", Type: FieldBool}}},
			{Name: "rounds", Kind: StageLoop, Count: 5, Until: "gate.done",
				Body: []PipelineStage{{Name: "work", Prompt: "x"}}},
		}},
	}
	for name, def := range bad {
		if err := def.Validate(); err == nil {
			t.Errorf("%s: expected a validation error, got nil", name)
		}
	}
}

// --- execution --------------------------------------------------------

// recorder is a dispatch hook that echoes a scripted reply per agent and
// records the prompt it was handed, so a test can assert the carry.
type recorder struct {
	mu      sync.Mutex
	prompts []string
	reply   func(agent, prompt string, n int) string
	n       int
}

func (rec *recorder) fn(_ context.Context, agent, prompt string) (string, error) {
	rec.mu.Lock()
	rec.n++
	n := rec.n
	rec.prompts = append(rec.prompts, prompt)
	rec.mu.Unlock()
	return rec.reply(agent, prompt, n), nil
}

func TestLoop_CarriesEachPassIntoTheNext(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "rounds", Kind: StageLoop, Count: 3, Body: []PipelineStage{
			{Name: "step", Kind: StageAgent, Agent: "w",
				Prompt: "pass {iteration} of {iterations}, previous was: {prev}"},
		}},
	}}
	if err := def.Validate(); err != nil {
		t.Fatalf("def should validate: %v", err)
	}
	rec := &recorder{reply: func(_, _ string, n int) string { return "result-" + strconv.Itoa(n) }}
	app := &AppCore{}
	out, err := app.executePipelineDef(context.Background(), def, "seed", rec.fn, nil, nil)
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if len(rec.prompts) != 3 {
		t.Fatalf("expected 3 passes, got %d", len(rec.prompts))
	}
	// {iteration} substitutes per pass...
	if !strings.Contains(rec.prompts[0], "pass 1 of 3") || !strings.Contains(rec.prompts[2], "pass 3 of 3") {
		t.Errorf("{iteration}/{iterations} not substituted: %v", rec.prompts)
	}
	// ...and each pass sees the PREVIOUS pass's output, which is the
	// difference between a loop and a fanout.
	if !strings.Contains(rec.prompts[0], "previous was: seed") {
		t.Errorf("pass 1 should carry the pipeline input: %q", rec.prompts[0])
	}
	if !strings.Contains(rec.prompts[1], "previous was: result-1") {
		t.Errorf("pass 2 should carry pass 1's output: %q", rec.prompts[1])
	}
	if !strings.Contains(rec.prompts[2], "previous was: result-2") {
		t.Errorf("pass 3 should carry pass 2's output: %q", rec.prompts[2])
	}
	if out != "result-3" {
		t.Errorf("collect defaults to last, got %q", out)
	}
}

func TestLoop_CollectAll(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "rounds", Kind: StageLoop, Count: 2, Collect: "all", Body: []PipelineStage{
			{Name: "step", Kind: StageAgent, Agent: "w", Prompt: "x"},
		}},
	}}
	rec := &recorder{reply: func(_, _ string, n int) string { return "said-" + strconv.Itoa(n) }}
	app := &AppCore{}
	out, err := app.executePipelineDef(context.Background(), def, "seed", rec.fn, nil, nil)
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	for _, want := range []string{"## Pass 1", "said-1", "## Pass 2", "said-2"} {
		if !strings.Contains(out, want) {
			t.Errorf("collect=all output missing %q:\n%s", want, out)
		}
	}
}

func TestLoop_UntilStopsEarly(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "rounds", Kind: StageLoop, Count: 10, Until: "check.done", Body: []PipelineStage{
			{Name: "check", Kind: StageAgent, Agent: "w", Prompt: "done?",
				Output: []PipelineField{{Name: "done", Type: FieldBool, Required: true}}},
		}},
	}}
	if err := def.Validate(); err != nil {
		t.Fatalf("def should validate: %v", err)
	}
	// False, false, then true — should stop after the third pass, not run
	// all ten.
	rec := &recorder{reply: func(_, _ string, n int) string {
		if n >= 3 {
			return `{"done": true}`
		}
		return `{"done": false}`
	}}
	app := &AppCore{}
	if _, err := app.executePipelineDef(context.Background(), def, "seed", rec.fn, nil, nil); err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if len(rec.prompts) != 3 {
		t.Errorf("until should have stopped the loop after 3 passes, got %d", len(rec.prompts))
	}
}

func TestLoop_UntilNeverTrueStillTerminates(t *testing.T) {
	// The ceiling is the guarantee — a model that never says "done" must
	// not be able to run a pipeline forever.
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "rounds", Kind: StageLoop, Count: 4, Until: "check.done", Body: []PipelineStage{
			{Name: "check", Kind: StageAgent, Agent: "w", Prompt: "done?",
				Output: []PipelineField{{Name: "done", Type: FieldBool}}},
		}},
	}}
	rec := &recorder{reply: func(_, _ string, _ int) string { return `{"done": false}` }}
	app := &AppCore{}
	if _, err := app.executePipelineDef(context.Background(), def, "seed", rec.fn, nil, nil); err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if len(rec.prompts) != 4 {
		t.Errorf("expected exactly count passes, got %d", len(rec.prompts))
	}
}

// The kind=machine stage: a whole run as one stage.
//
// The counterpart of the machine's pipeline phase. Together they let the
// two primitives compose in both directions, and the shape that wants it
// is a fanout body whose branch is a child run.

func machineStagePipeline(out []PipelineField) PipelineDef {
	return PipelineDef{Stages: []PipelineStage{
		{Name: "fill", Kind: StageMachine, Machine: "Gap filler",
			Prompt: "fill the gap in {input}", Output: out},
	}}
}

func TestValidate_MachineStageShape(t *testing.T) {
	if err := machineStagePipeline(nil).Validate(); err != nil {
		t.Fatalf("a valid machine stage was rejected: %v", err)
	}
	bad := map[string]PipelineDef{
		"names no machine": {Stages: []PipelineStage{
			{Name: "fill", Kind: StageMachine, Prompt: "x"},
		}},
		"names an agent as well": {Stages: []PipelineStage{
			{Name: "fill", Kind: StageMachine, Machine: "Gap filler", Agent: "w", Prompt: "x"},
		}},
		"machine on another kind": {Stages: []PipelineStage{
			{Name: "plain", Prompt: "x", Machine: "Gap filler"},
		}},
	}
	for name, def := range bad {
		if err := def.Validate(); err == nil {
			t.Errorf("%s: expected a validation error, got nil", name)
		}
	}
}

// Without a host to run machines, the stage says so rather than failing
// somewhere less useful. Same posture as an agent stage with no dispatch.
func TestMachineStageWithoutARunnerSaysSo(t *testing.T) {
	_, _, err := new(AppCore).RunPipelineDefHooks(context.Background(),
		machineStagePipeline(nil), "the gap", PipelineHooks{})
	if err == nil {
		t.Fatal("a machine stage with no runner should refuse")
	}
	if !strings.Contains(err.Error(), "machine runner") {
		t.Errorf("the refusal should name what is missing: %v", err)
	}
}

func TestMachineStageRunsAndCarriesItsText(t *testing.T) {
	var gotRef, gotInput string
	out, _, err := new(AppCore).RunPipelineDefHooks(context.Background(),
		machineStagePipeline(nil), "the export failure", PipelineHooks{
			Machine: func(_ context.Context, ref, input string) (string, map[string]any, error) {
				gotRef, gotInput = ref, input
				return "the child run's report", nil, nil
			},
		})
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if gotRef != "Gap filler" {
		t.Errorf("the stage should ask for the machine it names, got %q", gotRef)
	}
	if !strings.Contains(gotInput, "the export failure") {
		t.Errorf("the resolved prompt should reach the run: %q", gotInput)
	}
	if out != "the child run's report" {
		t.Errorf("the run's own result should be the stage's, got %q", out)
	}
}

// The one-call path: the child's last step already declared these fields,
// so nothing has to re-read them out of its prose.
func TestMachineStageTakesTheRunsOwnShape(t *testing.T) {
	declared := []PipelineField{{Name: "found", Type: FieldString}}
	_, fields, err := new(AppCore).RunPipelineDefHooks(context.Background(),
		machineStagePipeline(declared), "x", PipelineHooks{
			Machine: func(context.Context, string, string) (string, map[string]any, error) {
				return "prose the stage does not have to parse",
					map[string]any{"found": "a missing index", "extra": "not asked for"}, nil
			},
		})
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if fields["found"] != "a missing index" {
		t.Errorf("the declared field should come straight across: %#v", fields)
	}
	if _, leaked := fields["extra"]; leaked {
		t.Error("a field the stage never declared should not land on it")
	}
}

// Shapes that do not line up fall back to decoding the text, and when
// that fails the message says which side to fix rather than re-running
// the machine to reshape its answer.
func TestMachineStageMismatchExplainsWhichSideToFix(t *testing.T) {
	declared := []PipelineField{{Name: "found", Type: FieldString}}
	// JSON text still satisfies the contract even when the run reported no
	// fields of its own.
	if _, fields, err := new(AppCore).RunPipelineDefHooks(context.Background(),
		machineStagePipeline(declared), "x", PipelineHooks{
			Machine: func(context.Context, string, string) (string, map[string]any, error) {
				return `{"found": "a missing index"}`, nil, nil
			},
		}); err != nil || fields["found"] != "a missing index" {
		t.Errorf("a JSON result should still decode: %v / %#v", err, fields)
	}
	_, _, err := new(AppCore).RunPipelineDefHooks(context.Background(),
		machineStagePipeline(declared), "x", PipelineHooks{
			Machine: func(context.Context, string, string) (string, map[string]any, error) {
				return "just prose", map[string]any{"something_else": 1}, nil
			},
		})
	if err == nil {
		t.Fatal("a result that carries neither the fields nor decodable JSON should fail")
	}
	if !strings.Contains(err.Error(), "LAST step") {
		t.Errorf("the failure should say where to declare them: %v", err)
	}
}

// The shape this whole stage kind exists for: a child run per item,
// several at once, instead of one after another.
func TestAFanoutBodyCanRunAChildRunPerItem(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "gaps", Kind: StageAgent, Agent: "finder", Prompt: "find gaps",
			Output: []PipelineField{{Name: "list", Type: FieldList}}},
		{Name: "fill", Kind: StageFanout, FanOver: "gaps.list", Body: []PipelineStage{
			{Name: "run", Kind: StageMachine, Machine: "Gap filler", Prompt: "fill {item}"},
		}},
	}}
	if err := def.Validate(); err != nil {
		t.Fatalf("a fanout body of child runs should validate: %v", err)
	}
	rec := &recorder{reply: func(string, string, int) string { return `{"list": ["one", "two"]}` }}
	// Fanout branches call the hook at the same time, so the record of
	// what ran is guarded.
	var (
		mu  sync.Mutex
		ran []string
	)
	out, _, err := new(AppCore).RunPipelineDefHooks(context.Background(), def, "seed", PipelineHooks{
		Dispatch: rec.fn,
		Machine: func(_ context.Context, _, input string) (string, map[string]any, error) {
			mu.Lock()
			ran = append(ran, input)
			mu.Unlock()
			return "filled: " + input, nil, nil
		},
	})
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if len(ran) != 2 {
		t.Fatalf("want one child run per item, got %v", ran)
	}
	if !strings.Contains(out, "filled: fill one") || !strings.Contains(out, "filled: fill two") {
		t.Errorf("each branch's own run should show in the joined block:\n%s", out)
	}
}

// Per-stage model tier. The validation half matters more than the
// execution half here: a tier silently dropped on a kind it can't apply
// to reads as "I asked for lead and got worker", which is
// indistinguishable from a routing bug — so those are rejected at save
// time rather than ignored.

func TestValidate_StageModel(t *testing.T) {
	ok := PipelineDef{Stages: []PipelineStage{
		{Name: "plan", Prompt: "decompose {input}", Model: "lead"},
		{Name: "fetch", Kind: StageFanout, FanOver: "plan", Prompt: "{item}", Model: "worker"},
		{Name: "synth", Kind: StageSynthesize, Prompt: "combine {prev}", Model: "LEAD"},
		{Name: "plain", Prompt: "no tier at all"},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid model tiers rejected: %v", err)
	}

	bad := map[string]PipelineDef{
		"unknown tier": {Stages: []PipelineStage{
			{Name: "a", Prompt: "x", Model: "gpt-5"},
		}},
		"on an agent stage": {Stages: []PipelineStage{
			{Name: "a", Kind: StageAgent, Agent: "helper", Prompt: "x", Model: "lead"},
		}},
		"on a branch": {Stages: []PipelineStage{
			{Name: "f", Prompt: "x", Output: []PipelineField{{Name: "b", Type: FieldBool}}},
			{Name: "g", Kind: StageBranch, When: "f.b", Model: "lead"},
		}},
		"on the loop itself": {Stages: []PipelineStage{
			{Name: "rounds", Kind: StageLoop, Count: 2, Model: "lead",
				Body: []PipelineStage{{Name: "step", Prompt: "x"}}},
		}},
		"on an agent-dispatching fanout": {Stages: []PipelineStage{
			{Name: "plan", Prompt: "x"},
			{Name: "fan", Kind: StageFanout, FanOver: "plan", Agent: "helper", Prompt: "{item}", Model: "lead"},
		}},
	}
	for name, def := range bad {
		if err := def.Validate(); err == nil {
			t.Errorf("%s: expected a validation error, got nil", name)
		}
	}

	// A loop's BODY stages carry the tier — that's the supported form.
	inBody := PipelineDef{Stages: []PipelineStage{
		{Name: "rounds", Kind: StageLoop, Count: 2, Body: []PipelineStage{
			{Name: "step", Prompt: "x", Model: "lead"},
		}},
	}}
	if err := inBody.Validate(); err != nil {
		t.Errorf("model on a loop body stage should be allowed: %v", err)
	}
}

func TestStageTier(t *testing.T) {
	cases := map[string]LLMTier{
		"":       WORKER,
		"worker": WORKER,
		"lead":   LEAD,
		"LEAD":   LEAD,
		" lead ": LEAD,
	}
	for in, want := range cases {
		if got := stageTier(PipelineStage{Model: in}); got != want {
			t.Errorf("stageTier(%q) = %v, want %v", in, got, want)
		}
	}
}

// A run numbers what it reads so its stages can cite it, a stage's citations
// can be tidied against it, and a verify stage can check the writing against
// the text that was read. What two apps each built by hand, as data.

// sourceTool is a stand-in reader: a tool stage calls it, and its extractor
// (registered like a real tool's) turns the result into one page read.
const sourceToolName = "test_read_page"

var registerSourceTool sync.Once

func readerTool() AgentToolDef {
	registerSourceTool.Do(func() {
		sources.RegisterExtractor(sourceToolName, func(_ map[string]any, result string) []sources.Fetched {
			return []sources.Fetched{{Title: "Field Study", URL: "https://example.org/study", Text: result}}
		})
	})
	page := "The field study measured adoption across forty clinics over two years. " +
		strings.Repeat("Adoption rose steadily as clinics trained their staff and shared results with each other. ", 6) +
		"By the end, adoption reached 62 percent of eligible patients."
	return AgentToolDef{
		Tool:    Tool{Name: sourceToolName, Description: "read a page"},
		Handler: func(_ context.Context, _ map[string]any) (string, error) { return page, nil },
	}
}

// runWithSources runs def with the stand-in reader available and a fake model
// for the verify stage's claim checks, capturing every agent stage's prompt.
func runWithSources(t *testing.T, def PipelineDef, agentReply string, verdict string) (string, map[string]any, []string) {
	t.Helper()
	var mu sync.Mutex
	var prompts []string
	app := &AppCore{LLM: &FakeLLM{Turns: []FakeTurn{{Content: verdict, Repeat: true}}}}
	out, fields, err := app.RunPipelineDefHooks(context.Background(), def, "is it working?", PipelineHooks{
		Tools: []AgentToolDef{readerTool()},
		Dispatch: func(ctx context.Context, agent, prompt string) (string, error) {
			mu.Lock()
			prompts = append(prompts, prompt)
			mu.Unlock()
			return agentReply, nil
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return out, fields, prompts
}

// What a stage read is numbered, and {sources} hands the list to a later
// stage's prompt to cite from.
func TestALaterStageCitesWhatAnEarlierOneRead(t *testing.T) {
	def := PipelineDef{Name: "cite", Stages: []PipelineStage{
		{Name: "read", Kind: StageTool, Tool: sourceToolName},
		{Name: "write", Kind: StageAgent, Agent: "writer", Prompt: "Write it up. Cite as [N] from:\n{sources}"},
	}}
	_, _, prompts := runWithSources(t, def, "Adoption reached 62 percent [1].", "")
	if len(prompts) != 1 || !strings.Contains(prompts[0], "[1] Field Study - https://example.org/study") {
		t.Fatalf("the writer must be handed the numbered sources, got %q", prompts)
	}
}

// cite tidies a prose stage's citations: a Sources section of exactly what it
// cited, and a citation naming nothing read is noted, not passed off as real.
func TestCiteListsWhatWasCitedAndNotesWhatWasNot(t *testing.T) {
	def := PipelineDef{Name: "cite", Stages: []PipelineStage{
		{Name: "read", Kind: StageTool, Tool: sourceToolName},
		{Name: "write", Kind: StageAgent, Agent: "writer", Prompt: "{sources}", Cite: true},
	}}
	out, _, _ := runWithSources(t, def, "Adoption reached 62 percent [1]. Costs fell by half [4].", "")
	if !strings.Contains(out, "## Sources") || !strings.Contains(out, "[1] [Field Study](https://example.org/study)") {
		t.Errorf("the cited source must be listed as a link:\n%s", out)
	}
	if !strings.Contains(out, "[4]") || !strings.Contains(out, "name no source this run read") {
		t.Errorf("a citation to nothing read must be noted:\n%s", out)
	}
}

// verify checks the writing: citations naming nothing, figures found in none
// of the text read, and each cited claim put to the model against its source.
func TestVerifyChecksTheWritingAgainstWhatWasRead(t *testing.T) {
	def := PipelineDef{Name: "verify", Stages: []PipelineStage{
		{Name: "read", Kind: StageTool, Tool: sourceToolName},
		{Name: "write", Kind: StageAgent, Agent: "writer", Prompt: "{sources}"},
		{Name: "check", Kind: StageVerify, Check: "write"},
	}}
	reply := "Adoption reached 62 percent of eligible patients [1]. It saved 900 million dollars [1]. Staff liked it [7]."
	_, fields, _ := runWithSources(t, def, reply, `{"verdict":"supported","why":"the study says so"}`)
	if fields["checked"] != 2 || fields["supported"] != 2 {
		t.Errorf("the two claims citing [1] must be checked and supported: %v", fields)
	}
	if u, _ := fields["unresolved_citations"].([]any); len(u) != 1 || u[0] != "[7]" {
		t.Errorf("[7] names nothing read: %v", fields["unresolved_citations"])
	}
	if f, _ := fields["unverified_figures"].([]any); len(f) == 0 || !strings.Contains(fmtAny(f), "900") {
		t.Errorf("900 million appears in no source: %v", fields["unverified_figures"])
	}
	if fields["passed"] != false {
		t.Errorf("a draft with an unresolved citation and an unverified figure has not passed: %v", fields["passed"])
	}
}

// A claim the source does not carry comes back unsupported, with why.
func TestVerifyReportsAnUnsupportedClaim(t *testing.T) {
	def := PipelineDef{Name: "verify", Stages: []PipelineStage{
		{Name: "read", Kind: StageTool, Tool: sourceToolName},
		{Name: "write", Kind: StageAgent, Agent: "writer", Prompt: "{sources}"},
		{Name: "check", Kind: StageVerify, Check: "write"},
	}}
	_, fields, _ := runWithSources(t, def, "Every clinic adopted it within a month [1].",
		`{"verdict":"unsupported","why":"the study says adoption rose over two years"}`)
	u, _ := fields["unsupported"].([]any)
	if len(u) != 1 || !strings.Contains(fmtAny(u), "two years") {
		t.Errorf("the unsupported claim must be reported with why: %v", fields)
	}
}

// verify and cite are checked when the pipeline is saved.
func TestSourcesStagesAreCheckedOnSave(t *testing.T) {
	stages := func(s ...PipelineStage) PipelineDef { return PipelineDef{Name: "d", Stages: s} }
	write := PipelineStage{Name: "write", Kind: StageWorker, Prompt: "x"}
	for name, bad := range map[string]PipelineDef{
		"verify with no check":  stages(write, PipelineStage{Name: "v", Kind: StageVerify}),
		"verify a later stage":  stages(PipelineStage{Name: "v", Kind: StageVerify, Check: "write"}, write),
		"verify with an output": stages(write, PipelineStage{Name: "v", Kind: StageVerify, Check: "write", Output: []PipelineField{{Name: "x"}}}),
		"check off verify":      stages(PipelineStage{Name: "w", Kind: StageWorker, Prompt: "x", Check: "w"}),
		"cite on JSON":          stages(PipelineStage{Name: "w", Kind: StageWorker, Prompt: "x", Cite: true, Output: []PipelineField{{Name: "a"}}}),
		"cite on a branch":      stages(PipelineStage{Name: "b", Kind: StageWorker, Prompt: "x", Output: []PipelineField{{Name: "ok", Type: FieldBool}}}, PipelineStage{Name: "br", Kind: StageBranch, When: "b.ok", Cite: true}),
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%s should be refused", name)
		}
	}
	// Its fields are real to what follows: a card on them, a branch on passed.
	good := stages(write,
		PipelineStage{Name: "v", Kind: StageVerify, Check: "write", Render: "card", Card: map[string]string{"title": "summary", "accent": "passed"}},
		PipelineStage{Name: "redo", Kind: StageBranch, When: "v.passed"},
		PipelineStage{Name: "after", Kind: StageWorker, Prompt: "{stage:v.summary}"})
	if err := good.Validate(); err != nil {
		t.Errorf("a verify stage's fields must be usable: %v", err)
	}
}

// The source list on its own.
func TestRunSourcesNumberCiteAndKeepText(t *testing.T) {
	rs := sources.NewRunSources()
	if n := rs.Add(sources.Fetched{Title: "A", URL: "https://a.example/x", Text: "snippet"}); n != 1 {
		t.Fatalf("first source is [1], got %d", n)
	}
	rs.Add(sources.Fetched{Title: "B", URL: "https://b.example/y"})
	if n := rs.Add(sources.Fetched{Title: "A again", URL: "https://a.example/x/", Text: "the whole page, much longer than the snippet"}); n != 1 {
		t.Errorf("a page read twice keeps its number, got %d", n)
	}
	if _, text, _ := rs.Source(1); !strings.HasPrefix(text, "the whole page") {
		t.Errorf("the longer read replaces the snippet, got %q", text)
	}
	if got := sources.Citations("see [2] and [1, 3], again [2]"); len(got) != 3 || got[0] != 2 || got[1] != 1 || got[2] != 3 {
		t.Errorf("citations in order of first mention, got %v", got)
	}
	out, unknown := rs.Cite("A [2]. B [9].")
	if !strings.Contains(out, "[2] [B](https://b.example/y)") || strings.Contains(out, "[1] [A]") || len(unknown) != 1 || unknown[0] != 9 {
		t.Errorf("only what was cited is listed, the unknown reported: %q %v", out, unknown)
	}
}

func fmtAny(v []any) string { return fmt.Sprint(v...) }

// A worker stage's tool calls reach the run's sources through its loop's
// history: each call paired with its result, an error skipped.
func TestCollectSourcesReadsTheStageLoopsHistory(t *testing.T) {
	readerTool() // registers the extractor
	rs := sources.NewRunSources()
	ctx := context.WithValue(context.Background(), runSourcesKey{}, rs)
	collectSources(ctx, []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: sourceToolName}, {ID: "c2", Name: sourceToolName}}},
		{Role: "user", ToolResults: []ToolResult{{ID: "c1", Content: "the page"}, {ID: "c2", Content: "failed", IsError: true}}},
	})
	if rs.Len() != 1 {
		t.Errorf("one call read a page and one errored: %d sources, want 1", rs.Len())
	}
	collectSources(context.Background(), nil) // no run listening: nothing to do, no panic
}

// "Off" has to survive being saved.

// The bug this migration exists for: Think was a *bool, kvlite stores through
// gob, and gob encodes a FALSE POINTER as nothing at all. So an author picked
// "Off — this stage is a transform", saved, reopened, and found the control
// unset — with the run deliberating anyway if the caller's default said so.
// Nothing failed and nothing was reported; the setting simply was not there.
func TestOffSurvivesTheStore(t *testing.T) {
	udb := UserDB(&DBase{Store: kvlite.MemStore()}, "u")
	def := PipelineDef{Name: "research", Owner: "u", Stages: []PipelineStage{
		{Name: "gather", Kind: StageWorker, Prompt: "look", ThinkMode: "on"},
		{Name: "format", Kind: StageWorker, Prompt: "tidy it", ThinkMode: "off"},
		{Name: "reply", Kind: StageWorker, Prompt: "answer"},
	}}
	saved := SavePipelineDef(udb, def)
	back, ok := LoadPipelineDef(udb, "u", saved.ID)
	if !ok {
		t.Fatal("the pipeline did not come back")
	}
	for i, want := range []string{"on", "off", ""} {
		if got := StageThinkMode(back.Stages[i]); got != want {
			t.Errorf("stage %q: think came back %q, saved %q", back.Stages[i].Name, got, want)
		}
	}
	if StageThinks(back.Stages[1]) {
		t.Error("a stage set to off must not deliberate")
	}
}

// A pipeline written before the change keeps what it was given. gob matches on
// field NAME, so the legacy field is still there to be decoded — removing it
// would have failed the whole record, and renaming it would have dropped the
// "on" that older stages legitimately carry.
func TestALegacyStageKeepsItsSetting(t *testing.T) {
	yes := true
	legacy := PipelineStage{Name: "judge", Kind: StageWorker, Prompt: "weigh it", Think: &yes}
	if got := StageThinkMode(legacy); got != "on" {
		t.Errorf("a legacy true should read as on, got %q", got)
	}
	// And it migrates in place, so nothing downstream sees two fields.
	stages := []PipelineStage{legacy}
	normalizeStageThink(stages)
	if stages[0].ThinkMode != "on" || stages[0].Think != nil {
		t.Errorf("the legacy field should fold into the string one: %+v", stages[0])
	}
}

// An exported recipe outlives the field it was written against. "think": true
// is what every pipeline exported before today carries, and an import that
// rejected it would refuse a pipeline that is perfectly good.
func TestARecipeAcceptsBothShapesOfThink(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"name":"a","think":true}`, "on"},
		{`{"name":"a","think":false}`, "off"},
		{`{"name":"a","think":"on"}`, "on"},
		{`{"name":"a","think":"off"}`, "off"},
		{`{"name":"a"}`, ""},
		{`{"name":"a","think":"nonsense"}`, ""},
	} {
		var s PipelineStage
		if err := json.Unmarshal([]byte(tc.raw), &s); err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		if got := StageThinkMode(s); got != tc.want {
			t.Errorf("%s → %q, want %q", tc.raw, got, tc.want)
		}
		if s.Name != "a" {
			t.Errorf("%s: the rest of the stage should decode normally: %+v", tc.raw, s)
		}
	}
}

// The two authoring surfaces agree: a step and a stage say this the same way.
func TestAStageAndAStepSayThinkTheSameWay(t *testing.T) {
	for _, mode := range []string{"on", "off", ""} {
		stage := StageThinks(PipelineStage{ThinkMode: mode})
		phase := PhaseThink(MachinePhase{Think: mode}, false)
		if stage != phase {
			t.Errorf("think %q: stage says %v, step says %v", mode, stage, phase)
		}
	}
}
