package core

// The shipped plan-and-execute pair, checked as a PAIR.
//
// A machine that names a pipeline which is not there does not fail: the
// broken-dependency posture runs that step inline and leaves a breadcrumb, which
// is right for a recipe somebody carried between deployments and wrong for one
// we ship — it would work every step in a single prompt and read like it worked.
// So the reference is checked here, where it costs nothing.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtrasPipelineRecipesValidate(t *testing.T) {
	paths, err := filepath.Glob("../extras/*.pipeline.json")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(paths) == 0 {
		t.Skip("no pipeline recipes in extras/")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			def := readExtrasPipeline(t, path)
			if err := def.Validate(); err != nil {
				t.Fatalf("does not validate:\n%v", err)
			}
			if strings.TrimSpace(def.Description) == "" {
				t.Error("a shipped recipe should describe when to reach for it")
			}
		})
	}
}

func readExtrasPipeline(t *testing.T, path string) PipelineDef {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var def PipelineDef
	if err := json.Unmarshal(raw, &def); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	return def
}

// Every pipeline a shipped machine names must ship beside it.
func TestShippedMachinesNameAShippedPipeline(t *testing.T) {
	pipes := map[string]bool{}
	paths, _ := filepath.Glob("../extras/*.pipeline.json")
	for _, path := range paths {
		pipes[strings.ToLower(strings.TrimSpace(readExtrasPipeline(t, path).Name))] = true
	}
	machines, _ := filepath.Glob("../extras/*.machine.json")
	for _, path := range machines {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var def MachineDef
		if err := json.Unmarshal(raw, &def); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, ph := range def.Phases {
			ref := strings.ToLower(strings.TrimSpace(ph.Pipeline))
			if ref != "" && !pipes[ref] {
				t.Errorf("%s: step %q runs pipeline %q, which does not ship — the step would silently run inline instead",
					filepath.Base(path), ph.Name, ph.Pipeline)
			}
		}
	}
}

// The recipe's SHAPE is the design, so the parts that carry it are pinned. A
// plan worked one step at a time by a single prompt is just a long prompt; what
// makes this worth shipping is that the steps run at once, that what they find
// accumulates into one working set, and that what could not be done survives to
// the report instead of being tidied away.
func TestPlanAndExecuteKeepsItsShape(t *testing.T) {
	raw, err := os.ReadFile("../extras/plan_and_execute.machine.json")
	if err != nil {
		t.Skip("recipe not present")
	}
	var def MachineDef
	if err := json.Unmarshal(raw, &def); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if !def.Unattended {
		t.Error("this recipe exists for work nobody is watching; a conversational one would wait at a step forever")
	}
	if probs := def.Problems(); len(probs) > 0 {
		t.Fatalf("a shipped recipe must be runnable as it lands:\n- %s", strings.Join(probs, "\n- "))
	}
	byName := map[string]MachinePhase{}
	for _, p := range def.Phases {
		byName[p.Name] = p
	}
	// Both working steps accumulate into the SAME two lists, or the second pass
	// replaces the first pass's findings instead of adding to them.
	for _, step := range []string{"execute", "fill_gaps"} {
		ph, ok := byName[step]
		if !ok {
			t.Fatalf("step %q is gone; the recipe's two-pass shape is the point of it", step)
		}
		if strings.TrimSpace(ph.Pipeline) == "" {
			t.Errorf("step %q no longer runs the pipeline — its steps would run as one prompt, in sequence", step)
		}
		got := map[string]string{}
		for _, a := range ph.Accumulates {
			got[a.Name] = a.From
		}
		if got["findings"] != "findings" || got["gaps"] != "blocked" {
			t.Errorf("step %q must add its findings and its blocked steps to the working set: %+v", step, ph.Accumulates)
		}
		for _, a := range ph.Accumulates {
			if m := strings.TrimSpace(a.Mode); m != "" && m != "append" {
				t.Errorf("step %q accumulates in mode %q — anything but append loses the other pass's work", step, m)
			}
		}
	}
	// The report is the terminal step: it hands off nowhere, which is what ends
	// an unattended run, and its text IS the result.
	report, ok := byName["report"]
	if !ok || strings.TrimSpace(report.Next) != "" || len(report.Choices) > 0 {
		t.Fatal("the report step must be where the run ENDS — a terminal step hands off nowhere")
	}
	// And it answers from the working set rather than going looking on its own:
	// a reporting step with tools writes what it just found instead of what the
	// plan established.
	if PhaseReach(report) != ReachNone {
		t.Error("the report step should reach for nothing; the looking belongs in the pipeline")
	}
	for _, ref := range []string{"{state:findings}", "{state:gaps}"} {
		if !strings.Contains(report.Prompt, ref) {
			t.Errorf("the report must read %s, or the working set was built for nothing", ref)
		}
	}
	// One gap pass, not a loop: review may send it round once, and fill_gaps
	// goes straight to the report.
	if next := strings.TrimSpace(byName["fill_gaps"].Next); next != "report" {
		t.Errorf("the gap pass must end at the report, not loop; it goes to %q", next)
	}
}

// The executor fans over a DECLARED list field. Fanning over a stage's raw text
// works only while the model happens to answer with a bare JSON array.
func TestPlanStepsFansOverADeclaredField(t *testing.T) {
	def := readExtrasPipeline(t, "../extras/plan_steps.pipeline.json")
	var fan PipelineStage
	for _, s := range def.Stages {
		if s.Kind == StageFanout {
			fan = s
		}
	}
	if fan.Name == "" {
		t.Fatal("the executor's whole job is the fan-out; there is no fanout stage")
	}
	if !strings.Contains(fan.FanOver, ".") {
		t.Errorf("fan_over %q names a whole stage rather than a declared list field", fan.FanOver)
	}
	src, field := SplitStageRef(fan.FanOver)
	var found bool
	for _, s := range def.Stages {
		if s.Name != src {
			continue
		}
		for _, f := range s.Output {
			if f.Name == field && f.Type == FieldList {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("fan_over points at %s.%s, which is not a declared list field of that stage", src, field)
	}
}
