// The two tool controls a stage carries, and the promise that a machine's
// step carries the same two.
package core

import (
	"context"
	"strings"
	"testing"
)

// A pipeline stage narrows by reach first and by name second — the same two
// controls a machine step carries, deliberately identical, because those are
// the two places an author writes this and one vocabulary is the whole point.
//
// A stage needs the coarse one MORE than a step does: a machine runs for
// whichever agent carries it, but a pipeline is invoked by whichever agent
// ATTACHED it, and several can. "The catalog" is a different set per caller,
// so a name list describes one of them and a capability describes all.
func TestStageReachNarrowsBeforeNames(t *testing.T) {
	inherited := []AgentToolDef{
		{Tool: Tool{Name: "search_prod_logs", Caps: []Capability{CapRead}}},
		{Tool: Tool{Name: "fetch_url", Caps: []Capability{CapNetwork, CapRead}}},
		{Tool: Tool{Name: "run_shell", Caps: []Capability{CapExecute}}},
	}
	names := func(defs []AgentToolDef) string {
		var out []string
		for _, d := range defs {
			out = append(out, d.Tool.Name)
		}
		return strings.Join(out, ",")
	}

	if got := names(StageTools(PipelineStage{}, inherited)); got != "search_prod_logs,fetch_url,run_shell" {
		t.Errorf("an untouched stage inherits the caller's catalog, got %q", got)
	}
	if got := names(StageTools(PipelineStage{Reach: ReachRead}, inherited)); got != "search_prod_logs" {
		t.Errorf("read-only should keep only what reads, got %q", got)
	}
	if got := StageTools(PipelineStage{Reach: ReachNone}, inherited); got != nil {
		t.Errorf("nothing means nothing, got %v", names(got))
	}
	// Names apply on top of the reach, not instead of it: a stage that is
	// read-only cannot name its way back to the network.
	if got := names(StageTools(PipelineStage{Reach: ReachRead,
		Tools: []string{"search_prod_logs", "fetch_url"}}, inherited)); got != "search_prod_logs" {
		t.Errorf("a name must not widen past the reach, got %q", got)
	}
	// And the marker a stage could already store keeps meaning what it meant.
	if got := StageReach(PipelineStage{Tools: []string{NoToolsMarker}}); got != ReachNone {
		t.Errorf("the legacy marker should read as reach none, got %q", got)
	}
}

// The two surfaces must not drift: same values, same resolution, same
// back-compat. A phase and a stage handed the same setting and the same
// catalog have to answer identically, or "an author who learned one has
// learned the other" stops being true.
func TestAStageAndAStepAgreeOnWhatAReachMeans(t *testing.T) {
	catalog := []AgentToolDef{
		{Tool: Tool{Name: "read_file", Caps: []Capability{CapRead}}},
		{Tool: Tool{Name: "fetch_url", Caps: []Capability{CapNetwork}}},
	}
	for _, reach := range []string{ReachAll, ReachRead, ReachNone} {
		stage := StageTools(PipelineStage{Reach: reach, Tools: []string{"read_file"}}, catalog)
		phase := PhaseTools(MachinePhase{Reach: reach, Tools: []string{"read_file"}}, catalog)
		if len(stage) != len(phase) {
			t.Errorf("reach %q: stage gave %d tools, step gave %d", reach, len(stage), len(phase))
		}
	}
}

// A tool stage decodes the answer, not what is wrapped around it: the
// untrusted fence's "[" was read as a JSON array, so a tool that returned
// exactly the declared object failed "reply was not a JSON object".
func TestToolStageBodyUnwrapsTheAnswer(t *testing.T) {
	wrapped := UntrustedToolResultFence + "[Sent through the \"geo\" credential: authenticated for you automatically.]\n\nHTTP 200 OK\n{\"lat\": 48.85, \"lon\": 2.35}"
	body, err := toolStageBody(wrapped)
	if err != nil || body != `{"lat": 48.85, "lon": 2.35}` {
		t.Fatalf("body = %q, %v", body, err)
	}
	if fields, err := decodeStageOutput(body, []PipelineField{{Name: "lat", Type: "number"}}); err != nil || fields["lat"] == nil {
		t.Fatalf("decoded = %v, %v", fields, err)
	}
	if _, err := toolStageBody("HTTP 404 Not Found\n{\"error\": \"unknown place\"}"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("a 404 should be the stage's error: %v", err)
	}
	for _, plain := range []string{`[1, 2]`, "[\n {\"a\": 1}\n]", `{"a": 1}`} {
		if got, _ := toolStageBody(plain); got != plain {
			t.Errorf("%q came back as %q", plain, got)
		}
	}
}

// A worker stage whose whole output is one yes/no field is shadowed like a
// router step: ShadowRoute is asked with the stage's prompt and the field's
// values, each shown with what the field is for, and told the stage's own
// answer, which still decides. A stage with tools, or with more than one
// field, is not.
func TestAYesNoStageIsShadowedAndStillDecides(t *testing.T) {
	saved := ShadowRoute
	t.Cleanup(func() { ShadowRoute = saved })
	var asked, settled []string
	var gotDescs []string
	ShadowRoute = func(_ context.Context, machine, step, prompt string, choices, descs []string) func(string) string {
		asked = append(asked, step+": "+strings.Join(choices, ","))
		gotDescs = descs
		return func(chosen string) string { settled = append(settled, chosen); return "" }
	}
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "frame", Kind: StageWorker, Prompt: "is {input} a real question?",
			Output: []PipelineField{{Name: "rejected", Type: FieldBool, Desc: "true when it is not a real question", Required: true}}},
		{Name: "gate", Kind: StageBranch, When: "frame.rejected"},
		{Name: "work", Kind: StageWorker, Prompt: "answer {input}"},
	}}
	fake := &FakeLLM{Turns: []FakeTurn{{Content: `{"rejected": false}`}, {Content: "the answer", Repeat: true}}}
	out, err := (&AppCore{LLM: fake}).executePipelineDef(context.Background(), def, "why is the sky blue", nil, nil, nil)
	if err != nil || out != "the answer" {
		t.Fatalf("the run: %q %v", out, err)
	}
	if strings.Join(asked, "|") != "frame: true,false" || strings.Join(settled, ",") != "false" ||
		!strings.Contains(gotDescs[0], "rejected is true (true when it is not a real question)") {
		t.Errorf("asked %v, settled %v, descs %v", asked, settled, gotDescs)
	}
	def.Stages[0].Output = append(def.Stages[0].Output, PipelineField{Name: "why"})
	asked = nil
	fake = &FakeLLM{Turns: []FakeTurn{{Content: `{"rejected": false, "why": "fine"}`}, {Content: "the answer", Repeat: true}}}
	if _, err := (&AppCore{LLM: fake}).executePipelineDef(context.Background(), def, "q", nil, nil, nil); err != nil || len(asked) != 0 {
		t.Errorf("a stage with two fields was shadowed (%v, %v)", asked, err)
	}
}
