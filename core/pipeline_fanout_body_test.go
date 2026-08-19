package core

// A fanout that runs SEVERAL stages per item.
//
// The single-prompt fan does breadth with one call per branch. A body
// makes each branch a small pipeline, which is what "investigate each of
// these properly, then compare what came back" needs. The tests that
// matter are the ones about SCOPE: branches run in parallel, so two
// branches running a stage of the same name must not see each other.

import (
	"context"
	"sort"
	"strings"
	"testing"
)

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
