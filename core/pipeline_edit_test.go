package core

// Renaming and removing a stage.
//
// Both exist because a pipeline's references are CHECKED: Validate
// refuses a {stage:NAME} that resolves to nothing, so an editor that
// changes a name without rewriting the references produces a definition
// nothing will store.

import (
	"strings"
	"testing"
)

func renameFixture() PipelineDef {
	think := true
	return PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "plan", Kind: StageWorker, Prompt: "break up {input}",
			Output: []PipelineField{
				{Name: "queries", Type: FieldList, Desc: "q"},
				{Name: "done", Type: FieldBool, Desc: "settled"},
			}},
		{Name: "dig", Kind: StageFanout, FanOver: "plan.queries", Prompt: "research {item}"},
		{Name: "gate", Kind: StageBranch, When: "plan.done", SkipTo: "answer"},
		{Name: "calc", Kind: StageTool, Tool: "math", Args: map[string]string{"x": "{stage:plan.queries}"}},
		{Name: "round", Kind: StageLoop, Count: 3, Until: "critic.ok", Body: []PipelineStage{
			// A body stage reading an OUTER stage: a rename has to reach
			// inside the loop.
			{Name: "critic", Kind: StageWorker, Prompt: "judge {stage:plan}", Think: &think,
				Output: []PipelineField{
					{Name: "ok", Type: FieldBool, Desc: "settled"},
					{Name: "kept", Type: FieldString, From: "{stage:plan.queries}"},
				}},
		}},
		{Name: "answer", Kind: StageWorker, Prompt: "from {stage:dig} and {stage:plan.queries}"},
	}}
}

func TestRenamingAStageRewritesEveryReference(t *testing.T) {
	d := renameFixture()
	if err := d.Validate(); err != nil {
		t.Fatalf("fixture does not validate: %v", err)
	}
	touched := d.RenameStage("plan", "decompose")

	if d.Stages[0].Name != "decompose" {
		t.Fatalf("the stage was not renamed: %q", d.Stages[0].Name)
	}
	checks := []struct{ what, got, want string }{
		{"fan_over", d.Stages[1].FanOver, "decompose.queries"},
		{"when", d.Stages[2].When, "decompose.done"},
		{"skip_to", d.Stages[2].SkipTo, "answer"},
		{"args", d.Stages[3].Args["x"], "{stage:decompose.queries}"},
		{"body prompt", d.Stages[4].Body[0].Prompt, "judge {stage:decompose}"},
		{"body from", d.Stages[4].Body[0].Output[1].From, "{stage:decompose.queries}"},
		{"prompt", d.Stages[5].Prompt, "from {stage:dig} and {stage:decompose.queries}"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %q want %q", c.what, c.got, c.want)
		}
	}
	// The whole point: it still stores.
	if err := d.Validate(); err != nil {
		t.Fatalf("a rename must leave a runnable pipeline: %v", err)
	}
	// And it says what it touched, so a reply can be checked against the
	// pipeline rather than taken on trust.
	joined := strings.Join(touched, ", ")
	for _, want := range []string{"dig", "gate", "calc", "answer", "critic"} {
		if !strings.Contains(joined, want) {
			t.Errorf("touched should name %s: %v", want, touched)
		}
	}
	// A bare reference keeps whatever follows it — a rename must not eat
	// the comparison a condition is written with.
	if got, hit := renameBare("plan.done == true", "plan", "decompose"); !hit || got != "decompose.done == true" {
		t.Errorf("renameBare: got %q hit=%v", got, hit)
	}

	// A field that happens to share the name is not a stage reference.
	other := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "a", Kind: StageWorker, Prompt: "x",
			Output: []PipelineField{{Name: "plan", Type: FieldString, Desc: "d"}}},
		{Name: "b", Kind: StageWorker, Prompt: "read {stage:a.plan}"},
	}}
	other.RenameStage("plan", "nope")
	if other.Stages[1].Prompt != "read {stage:a.plan}" {
		t.Errorf("a FIELD called plan is not the stage: %q", other.Stages[1].Prompt)
	}
}

// Removal refuses while anything still reads the stage, and the refusal
// is the feature: a pipeline's references live in prose, and rewriting
// somebody's sentence so a delete can succeed is worse than naming the
// sentences in the way.
func TestRemovingAReadStageRefusesAndSaysWhere(t *testing.T) {
	d := renameFixture()
	err := d.RemoveStage("plan")
	if err == nil {
		t.Fatal("removing a stage five others read should refuse")
	}
	msg := err.Error()
	for _, want := range []string{"dig (fan_over)", "answer (prompt)", "calc (args.x)", "gate (when)"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal should name %s:\n%s", want, msg)
		}
	}
	if len(d.Stages) != 6 {
		t.Error("a refused removal must change nothing")
	}

	// A branch's skip_to counts as reading it, which is the point of
	// walking every reference field rather than only prompts.
	if err := d.RemoveStage("answer"); err == nil ||
		!strings.Contains(err.Error(), "gate (skip_to)") {
		t.Errorf("a jump target is a reference too: %v", err)
	}

	// Nothing reads calc, so it goes.
	if err := d.RemoveStage("calc"); err != nil {
		t.Fatalf("an unreferenced stage should remove: %v", err)
	}
	if len(d.Stages) != 5 {
		t.Fatalf("it did not go: %d stages", len(d.Stages))
	}
	if err := d.RemoveStage("calc"); err == nil {
		t.Error("removing it twice should say it is not there")
	}
}
