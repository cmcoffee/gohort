package core

// Tests for pipeline structured outputs: the declaration validator, the
// reply decoder + coercion, field templating, and fan_over field access.
// All of it is pure — no LLM, no store — which is the point of keeping
// the contract logic separate from the call.

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// --- Validate ---------------------------------------------------------

func TestValidate_PlainDefStillPasses(t *testing.T) {
	// The compatibility guarantee: a def with no Output declarations
	// validates exactly as it always did.
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "decompose", Kind: StageWorker, Prompt: "Split {input} into a JSON array."},
		{Name: "research", Kind: StageFanout, FanOver: "decompose", Prompt: "Research {item}"},
		{Name: "synth", Kind: StageWorker, Prompt: "Combine {prev} and {stage:decompose}."},
	}}
	if err := def.Validate(); err != nil {
		t.Fatalf("plain def should validate, got: %v", err)
	}
}

func TestValidate_RejectsForwardAndSelfReferences(t *testing.T) {
	cases := map[string]PipelineDef{
		"forward prompt ref": {Stages: []PipelineStage{
			{Name: "a", Prompt: "see {stage:b}"},
			{Name: "b", Prompt: "hi"},
		}},
		"self prompt ref": {Stages: []PipelineStage{
			{Name: "a", Prompt: "see {stage:a}"},
		}},
		"unknown prompt ref": {Stages: []PipelineStage{
			{Name: "a", Prompt: "hi"},
			{Name: "b", Prompt: "see {stage:typo}"},
		}},
		// Previously slipped through: the old check registered the stage
		// before testing FanOver, so a stage could fan over itself and
		// only fail at run time.
		"self fanout": {Stages: []PipelineStage{
			{Name: "a", Kind: StageFanout, FanOver: "a", Prompt: "{item}"},
		}},
		"forward fanout": {Stages: []PipelineStage{
			{Name: "a", Kind: StageFanout, FanOver: "b", Prompt: "{item}"},
			{Name: "b", Prompt: "hi"},
		}},
	}
	for name, def := range cases {
		if err := def.Validate(); err == nil {
			t.Errorf("%s: expected a validation error, got nil", name)
		}
	}
}

func TestValidate_FieldReferences(t *testing.T) {
	ok := PipelineDef{Stages: []PipelineStage{
		{Name: "frame", Prompt: "Frame {input}", Output: []PipelineField{
			{Name: "verdict", Type: FieldString},
			{Name: "queries", Type: FieldList},
		}},
		{Name: "use", Prompt: "Verdict was {stage:frame.verdict}"},
		{Name: "fan", Kind: StageFanout, FanOver: "frame.queries", Prompt: "{item}"},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid field references rejected: %v", err)
	}

	bad := map[string]PipelineDef{
		"undeclared field in prompt": {Stages: []PipelineStage{
			{Name: "frame", Prompt: "x", Output: []PipelineField{{Name: "verdict"}}},
			{Name: "use", Prompt: "{stage:frame.nope}"},
		}},
		"field on a stage with no output": {Stages: []PipelineStage{
			{Name: "frame", Prompt: "x"},
			{Name: "use", Prompt: "{stage:frame.verdict}"},
		}},
		"fan over a non-list field": {Stages: []PipelineStage{
			{Name: "frame", Prompt: "x", Output: []PipelineField{{Name: "verdict", Type: FieldString}}},
			{Name: "fan", Kind: StageFanout, FanOver: "frame.verdict", Prompt: "{item}"},
		}},
	}
	for name, def := range bad {
		if err := def.Validate(); err == nil {
			t.Errorf("%s: expected a validation error, got nil", name)
		}
	}
}

func TestValidate_OutputShape(t *testing.T) {
	bad := map[string]PipelineDef{
		"dot in stage name": {Stages: []PipelineStage{
			{Name: "a.b", Prompt: "x"},
		}},
		"bad field name": {Stages: []PipelineStage{
			{Name: "a", Prompt: "x", Output: []PipelineField{{Name: "Not-Valid"}}},
		}},
		"duplicate field": {Stages: []PipelineStage{
			{Name: "a", Prompt: "x", Output: []PipelineField{{Name: "q"}, {Name: "q"}}},
		}},
		"unknown type": {Stages: []PipelineStage{
			{Name: "a", Prompt: "x", Output: []PipelineField{{Name: "q", Type: "date"}}},
		}},
		"nests too deep": {Stages: []PipelineStage{
			{Name: "a", Prompt: "x", Output: []PipelineField{{
				Name: "q", Type: FieldList,
				Fields: []PipelineField{{Name: "inner", Type: FieldObject, Fields: []PipelineField{{Name: "deep"}}}},
			}}},
		}},
		"nested fields on a scalar": {Stages: []PipelineStage{
			{Name: "a", Prompt: "x", Output: []PipelineField{{
				Name: "q", Type: FieldString, Fields: []PipelineField{{Name: "inner"}},
			}}},
		}},
		"output on fanout": {Stages: []PipelineStage{
			{Name: "a", Prompt: "x"},
			{Name: "b", Kind: StageFanout, FanOver: "a", Prompt: "{item}", Output: []PipelineField{{Name: "q"}}},
		}},
	}
	for name, def := range bad {
		if err := def.Validate(); err == nil {
			t.Errorf("%s: expected a validation error, got nil", name)
		}
	}
}

func TestStageRefs(t *testing.T) {
	got := stageRefs("start {stage:a} mid {stage:b.c} end {notastage} {stage:unterminated")
	want := []string{"a", "b.c"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// --- decode + coerce --------------------------------------------------

func TestDecodeStageOutput_CoercesAndValidates(t *testing.T) {
	decl := []PipelineField{
		{Name: "title", Type: FieldString, Required: true},
		{Name: "count", Type: FieldNumber},
		{Name: "ready", Type: FieldBool},
		{Name: "queries", Type: FieldList},
		{Name: "absent", Type: FieldList},
	}
	// Every value here is the "wrong" JSON type on purpose — a model
	// quoting a number, spelling a bool, or handing back a bare string
	// where a list was asked for. Reshaping beats bouncing the stage.
	reply := "```json\n{\"title\":\"x\",\"count\":\"42\",\"ready\":\"yes\",\"queries\":\"just one\"}\n```"
	got, err := decodeStageOutput(reply, decl)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if got["title"] != "x" {
		t.Errorf("title = %v", got["title"])
	}
	if got["count"] != float64(42) {
		t.Errorf("count = %v (%T), want 42", got["count"], got["count"])
	}
	if got["ready"] != true {
		t.Errorf("ready = %v", got["ready"])
	}
	if l, ok := got["queries"].([]any); !ok || len(l) != 1 || l[0] != "just one" {
		t.Errorf("queries = %v, want a one-element list", got["queries"])
	}
	// An absent optional field resolves to its zero so a downstream
	// template renders empty instead of leaking the placeholder.
	if l, ok := got["absent"].([]any); !ok || len(l) != 0 {
		t.Errorf("absent = %v, want an empty list", got["absent"])
	}
}

func TestDecodeStageOutput_Failures(t *testing.T) {
	decl := []PipelineField{{Name: "verdict", Type: FieldString, Required: true}}
	if _, err := decodeStageOutput("I'm not JSON at all.", decl); err == nil {
		t.Error("expected an error for a non-JSON reply")
	}
	if _, err := decodeStageOutput(`{"other":"thing"}`, decl); err == nil {
		t.Error("expected an error for a missing required field")
	}
	// A required field present but uncoercible still fails.
	num := []PipelineField{{Name: "n", Type: FieldNumber, Required: true}}
	if _, err := decodeStageOutput(`{"n":"not a number"}`, num); err == nil {
		t.Error("expected an error for an uncoercible number")
	}
}

func TestRenderFieldValue(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"plain", "plain"},
		{float64(3), "3"}, // JSON has one number type; don't render "3.0"
		{float64(2.5), "2.5"},
		{true, "true"},
		{[]any{"a", "b"}, `["a","b"]`}, // JSON so DecodeJSONList can re-read it
		{nil, ""},
	}
	for _, c := range cases {
		if got := renderFieldValue(c.in); got != c.want {
			t.Errorf("renderFieldValue(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// --- templating -------------------------------------------------------

func TestResolveStageTemplate_FieldsAndCollision(t *testing.T) {
	outputs := map[string]stageOutput{
		"plan": {
			Text:   `{"queries":["q1","q2"],"note":"hi"}`,
			Fields: map[string]any{"queries": []any{"q1", "q2"}, "note": "hi"},
		},
	}
	got := resolveStageTemplate(
		"whole={stage:plan} list={stage:plan.queries} note={stage:plan.note} miss={stage:plan.nope}",
		"IN", "PREV", outputs)

	// {stage:plan} must not have matched inside {stage:plan.queries} —
	// the closing brace is part of the literal, which is what lets this
	// stay a plain replacement rather than a scanner.
	if !strings.Contains(got, `whole={"queries":["q1","q2"],"note":"hi"}`) {
		t.Errorf("whole-output substitution wrong: %s", got)
	}
	if !strings.Contains(got, `list=["q1","q2"]`) {
		t.Errorf("field substitution wrong: %s", got)
	}
	if !strings.Contains(got, "note=hi") {
		t.Errorf("string field substitution wrong: %s", got)
	}
	// Unknown placeholders stay visible rather than blanking silently.
	if !strings.Contains(got, "miss={stage:plan.nope}") {
		t.Errorf("unknown field should be left untouched: %s", got)
	}
}

// --- fan_over ---------------------------------------------------------

func TestFanoutItems(t *testing.T) {
	outputs := map[string]stageOutput{
		"bare":  {Text: `["a","b","c"]`},
		"plan":  {Text: `{"queries":["q1","q2"]}`, Fields: map[string]any{"queries": []any{"q1", "q2"}, "n": float64(2)}},
		"prose": {Text: "not a list"},
	}
	if got, err := fanoutItems("bare", outputs); err != nil || len(got) != 3 {
		t.Errorf("whole-output form: got %v, err %v", got, err)
	}
	got, err := fanoutItems("plan.queries", outputs)
	if err != nil || len(got) != 2 || got[0] != "q1" {
		t.Errorf("field form: got %v, err %v", got, err)
	}
	if _, err := fanoutItems("plan.n", outputs); err == nil {
		t.Error("expected an error fanning over a non-list field")
	}
	if _, err := fanoutItems("plan.missing", outputs); err == nil {
		t.Error("expected an error for an undeclared field")
	}
	if _, err := fanoutItems("nosuch", outputs); err == nil {
		t.Error("expected an error for an unknown stage")
	}
}

// --- end to end -------------------------------------------------------

// stubDispatch records every (agent, prompt) it is handed and replies
// from a canned table. Agent stages go through the same runDeclaredStage
// path as worker stages, so an all-agent def exercises contract → decode
// → field templating → fan-over-a-field without an LLM anywhere.
type stubDispatch struct {
	mu    sync.Mutex
	calls []string
	reply map[string]string
}

func (s *stubDispatch) fn(_ context.Context, agentID, input string) (string, error) {
	s.mu.Lock()
	s.calls = append(s.calls, agentID+"|"+input)
	s.mu.Unlock()
	if r, ok := s.reply[agentID]; ok {
		return r, nil
	}
	return "WROTE: " + input, nil
}

func (s *stubDispatch) sawPromptContaining(agent, sub string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if strings.HasPrefix(c, agent+"|") && strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func TestExecutePipelineDef_StructuredEndToEnd(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "plan", Kind: StageAgent, Agent: "planner", Prompt: "Plan for {input}",
			Output: []PipelineField{
				{Name: "topic", Type: FieldString, Required: true},
				{Name: "queries", Type: FieldList, Required: true},
			}},
		{Name: "fan", Kind: StageFanout, Agent: "researcher", FanOver: "plan.queries",
			Prompt: "Research {item} for {stage:plan.topic}"},
		{Name: "final", Kind: StageAgent, Agent: "writer",
			Prompt: "Topic {stage:plan.topic}; findings:\n{prev}"},
	}}
	if err := def.Validate(); err != nil {
		t.Fatalf("def should validate: %v", err)
	}

	stub := &stubDispatch{reply: map[string]string{
		// Fenced, with a leading apology — the shape a real reply takes.
		"planner":    "Sure!\n```json\n{\"topic\":\"ferrets\",\"queries\":[\"diet\",\"housing\"]}\n```",
		"researcher": "a finding",
	}}
	app := &AppCore{}
	out, err := app.executePipelineDef(context.Background(), def, "ferret care", stub.fn, nil, nil)
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}

	// The declared stage got a contract appended to its prompt.
	if !stub.sawPromptContaining("planner", "single JSON object") {
		t.Error("planner prompt should carry the output contract")
	}
	// Fanout ran once per element of the FIELD, with the sibling field
	// templated into each branch prompt.
	for _, q := range []string{"diet", "housing"} {
		if !stub.sawPromptContaining("researcher", "Research "+q+" for ferrets") {
			t.Errorf("missing fanout branch for %q; calls: %v", q, stub.calls)
		}
	}
	// A later stage read one field of a two-stage-old result.
	if !stub.sawPromptContaining("writer", "Topic ferrets") {
		t.Error("writer prompt should have the templated field")
	}
	if !strings.HasPrefix(out, "WROTE:") {
		t.Errorf("final output = %q", out)
	}
}

func TestExecutePipelineDef_RepairsThenFails(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "plan", Kind: StageAgent, Agent: "planner", Prompt: "Plan",
			Output: []PipelineField{{Name: "topic", Type: FieldString, Required: true}}},
	}}

	// Never produces the declared field: one repair attempt, then the
	// stage fails rather than handing an empty field downstream.
	bad := &stubDispatch{reply: map[string]string{"planner": "no json here"}}
	app := &AppCore{}
	if _, err := app.executePipelineDef(context.Background(), def, "x", bad.fn, nil, nil); err == nil {
		t.Error("expected the stage to fail after the repair attempt")
	}
	if len(bad.calls) != 2 {
		t.Errorf("expected exactly one repair retry, got %d calls", len(bad.calls))
	}
	if !strings.Contains(bad.calls[1], "could not be used") {
		t.Errorf("repair prompt should explain the failure: %q", bad.calls[1])
	}

	// Recovers on the retry: one bad reply shouldn't sink the pipeline.
	var n int
	var mu sync.Mutex
	recovering := func(_ context.Context, _, _ string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 {
			return "oops, prose", nil
		}
		return `{"topic":"ferrets"}`, nil
	}
	if _, err := app.executePipelineDef(context.Background(), def, "x", recovering, nil, nil); err != nil {
		t.Errorf("repair retry should have recovered: %v", err)
	}
}

// --- contract rendering -----------------------------------------------

func TestRenderOutputContract_DeclaredOrderIsStable(t *testing.T) {
	decl := []PipelineField{
		{Name: "zebra", Type: FieldString, Required: true, Desc: "first declared"},
		{Name: "alpha", Type: FieldList, Desc: "second declared"},
	}
	got := renderOutputContract(decl)
	z, a := strings.Index(got, `"zebra"`), strings.Index(got, `"alpha"`)
	if z < 0 || a < 0 || z > a {
		t.Fatalf("fields must render in declared order, not sorted:\n%s", got)
	}
	// Same input, same bytes — a prompt that reshuffles never caches.
	if renderOutputContract(decl) != got {
		t.Error("contract rendering is not deterministic")
	}
	if !strings.Contains(got, "required") || !strings.Contains(got, "optional") {
		t.Errorf("contract should mark required vs optional:\n%s", got)
	}
}
