package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The shipped plan-and-execute pair, checked as a PAIR.
//
// A machine that names a pipeline which is not there does not fail: the
// broken-dependency posture runs that step inline and leaves a breadcrumb, which
// is right for a recipe somebody carried between deployments and wrong for one
// we ship — it would work every step in a single prompt and read like it worked.
// So the reference is checked here, where it costs nothing.

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

// The parameter is step_id and models routinely write "step". Reported live:
// an agent called mark_step_in_progress({"step": 1}) four rounds running,
// narrating "let me start" each time, never reaching the tool it was asked to
// test — because a missing integer reads as 0 and "step 0 not found in plan"
// does not tell anyone which WORD to change.
func TestStepIDAcceptsTheNamesModelsWrite(t *testing.T) {
	for _, args := range []map[string]any{
		{"step_id": 1},
		{"step": 1}, // the reported case
		{"id": 1},
		{"stepId": 1},
		{"step_id": "1"}, // stringified, as some providers send
		{"step_id": float64(1)},
	} {
		got, ok := stepIDArg(args)
		if !ok || got != 1 {
			t.Errorf("%v resolved to (%d, %v), want (1, true)", args, got, ok)
		}
	}
	// Nothing usable must REPORT nothing usable rather than resolving to 0 and
	// producing an error about a step that was never named.
	for _, args := range []map[string]any{
		{},
		{"reason": "because"},
		{"step_id": nil},
	} {
		if _, ok := stepIDArg(args); ok {
			t.Errorf("%v claimed to carry a step id", args)
		}
	}
}

// Re-marking the step that is already in progress must SAY it changed nothing.
// A cheerful success reads as progress, and an agent just told it progressed
// will happily say so again — which is exactly the four identical rounds.
func TestReMarkingTheActiveStepReportsNoChange(t *testing.T) {
	var plan WorkPlan
	if err := plan.SetSteps([]string{"list submolts", "fetch feed"}, []string{"which submolts exist", "what is on the feed"}); err != nil {
		t.Fatal(err)
	}
	tools := WorkPlanTools(WorkPlanToolSpec{Plan: &plan})

	var start AgentToolDef
	for _, td := range tools.All() {
		if td.Tool.Name == "mark_step_in_progress" {
			start = td
		}
	}
	if start.Handler == nil {
		t.Fatal("no mark_step_in_progress tool")
	}

	first, err := start.Handler(map[string]any{"step_id": 1})
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if strings.Contains(first, "ALREADY") {
		t.Errorf("the first call reported no change: %s", first)
	}

	// The same call again — the shape that looped.
	second, err := start.Handler(map[string]any{"step": 1})
	if err != nil {
		t.Fatalf("the synonym form failed: %v", err)
	}
	if !strings.Contains(second, "ALREADY") {
		t.Fatalf("re-marking the active step reported success again: %s", second)
	}
	// And it must say what to do INSTEAD, or the model has been told to stop
	// without being told to start.
	if !strings.Contains(second, "record_step_findings") {
		t.Errorf("the no-op message does not say what to do next: %s", second)
	}
}

// A missing step id names the parameter to use, rather than failing about a
// step nobody mentioned.
func TestAMissingStepIDNamesTheParameter(t *testing.T) {
	var plan WorkPlan
	_ = plan.SetSteps([]string{"one"}, []string{"anything"})
	tools := WorkPlanTools(WorkPlanToolSpec{Plan: &plan})
	for _, td := range tools.All() {
		if td.Tool.Name != "mark_step_in_progress" {
			continue
		}
		_, err := td.Handler(map[string]any{})
		if err == nil {
			t.Fatal("a call with no step id succeeded")
		}
		if !strings.Contains(err.Error(), "step_id") {
			t.Errorf("the error does not name the parameter: %v", err)
		}
	}
}

// The decisions in a work plan, tested apart from any host.
//
// These are the rules that make a plan worth having rather than a checklist
// card: history cannot be edited away, revision is capped so the model works the
// plan instead of rewriting it, and an unfinished step reaches the answer as a
// stated gap.
func setThreeStepPlan(t *testing.T) *WorkPlan {
	t.Helper()
	p := &WorkPlan{}
	if err := p.SetSteps(
		[]string{"read the logs", "check the config", "ask the vendor"},
		[]string{"what failed", "whether it is set", "whether it is known"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	return p
}

// A step that HAPPENED is history. Removing it would erase the very thing the
// gap report exists to state.
func TestOnlyPendingStepsCanBeRemoved(t *testing.T) {
	p := setThreeStepPlan(t)
	if err := p.RecordFindings(1, "the disk filled"); err != nil {
		t.Fatal(err)
	}
	if err := p.MarkBlocked(2, "no read access"); err != nil {
		t.Fatal(err)
	}
	removed, refused := p.RemoveSteps([]int{1, 2, 3})
	if len(removed) != 1 || removed[0] != 3 {
		t.Errorf("only the pending step should go; removed %v", removed)
	}
	if len(refused) != 2 {
		t.Errorf("done and blocked steps are durable history; refused %v", refused)
	}
	if got := len(p.Snapshot()); got != 2 {
		t.Errorf("plan should still hold both kept steps, got %d", got)
	}
}

// The gap report is the point of the whole object: what was blocked, and what
// was simply never done, each with a reason a reader can act on.
func TestGapReportNamesBlockedAndUnfinishedSteps(t *testing.T) {
	p := setThreeStepPlan(t)
	if err := p.RecordFindings(1, "the disk filled"); err != nil {
		t.Fatal(err)
	}
	if err := p.MarkBlocked(2, "no read access"); err != nil {
		t.Fatal(err)
	}
	gaps := p.MarkGapsReported()
	if len(gaps.Blocked) != 1 || gaps.Blocked[0].Reason != "no read access" {
		t.Errorf("the blocked step and its real reason must survive to the report: %+v", gaps.Blocked)
	}
	if len(gaps.Skipped) != 1 || gaps.Skipped[0].ID != 3 {
		t.Errorf("a step nobody finished is a gap, not an omission: %+v", gaps.Skipped)
	}
	if !p.GapsReported() {
		t.Error("taking the report should record that it was taken")
	}
	// A completed step is not a gap.
	for _, g := range append(gaps.Blocked, gaps.Skipped...) {
		if g.ID == 1 {
			t.Error("a step with findings was reported as a gap")
		}
	}
}

// Reordering is a permutation or it is refused: a partial ordering would drop
// steps silently, which is the same failure as removing history.
func TestReorderMustBeAPermutation(t *testing.T) {
	p := setThreeStepPlan(t)
	if err := p.ReorderSteps([]int{3, 1}); err == nil {
		t.Error("a short ordering must be refused, not applied")
	}
	if err := p.ReorderSteps([]int{3, 1, 1}); err == nil {
		t.Error("a repeated id must be refused")
	}
	if err := p.ReorderSteps([]int{3, 2, 1}); err != nil {
		t.Fatalf("a full permutation should apply: %v", err)
	}
	if p.Snapshot()[0].ID != 3 {
		t.Error("the reorder did not take effect")
	}
}

// The whole plan has to survive being written down and read back, or it dies at
// the end of the turn — which is what kept the earlier version app-local.
func TestAWorkPlanRoundTripsThroughJSON(t *testing.T) {
	p := setThreeStepPlan(t)
	_ = p.RecordFindings(1, "the disk filled")
	p.IncrRevision()
	blob, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back WorkPlan
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Snapshot()) != 3 || back.RevisionCount() != 1 {
		t.Fatalf("a persisted plan must come back whole: %d steps, %d revisions", len(back.Snapshot()), back.RevisionCount())
	}
	if back.Snapshot()[0].Findings != "the disk filled" {
		t.Error("findings are the value of a finished step; they must persist")
	}
	// And adding to a restored plan must not reissue an id it already used.
	added, err := back.AddSteps([]string{"one more"}, []string{"the last thing"})
	if err != nil {
		t.Fatal(err)
	}
	if added[0] != 4 {
		t.Errorf("a restored plan reissued id %d over an existing step", added[0])
	}
}

// The tool group is the seam a host mounts. What matters here is that it stays
// host-agnostic: every mutation reports out, and nothing is cacheable.
func TestWorkPlanToolsReportEveryChangeAndAreNeverCached(t *testing.T) {
	var changes []WorkPlanChange
	set := WorkPlanTools(WorkPlanToolSpec{
		PlanID:   "plan-1",
		OnChange: func(c WorkPlanChange) { changes = append(changes, c) },
	})
	if got := len(set.All()); got != 6 {
		t.Fatalf("the group is six tools; got %d", got)
	}
	// A plan tool that declared capabilities would become cacheable within a run,
	// and a cached mutation is a call that reports success without moving the
	// plan. Unannotated is the whole guard.
	for _, td := range set.All() {
		if len(td.Tool.Caps) != 0 {
			t.Errorf("%s declares capabilities, which makes it cacheable", td.Tool.Name)
		}
	}
	// Nothing works before the plan is set, and saying so beats a bare error.
	if out, _ := set.Start.Handler(map[string]any{"step_id": 1}); !strings.Contains(out, "NO PLAN") {
		t.Errorf("a step tool before set_plan should say what is missing: %q", out)
	}
	out, err := set.Set.Handler(map[string]any{"steps": []any{
		map[string]any{"title": "read the logs", "what_to_find": "what failed"},
	}})
	if err != nil {
		t.Fatalf("set_plan: %v", err)
	}
	if !strings.Contains(out, "mark_step_in_progress") {
		t.Errorf("set_plan should say what to do next: %q", out)
	}
	if _, err := set.Findings.Handler(map[string]any{"step_id": 1, "findings": "it was the disk"}); err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 || changes[0].Kind != "set" || changes[1].Kind != "step" {
		t.Fatalf("every mutation must reach the host, kinds first-then-rest: %+v", changes)
	}
	if changes[0].PlanID != "plan-1" {
		t.Error("a change must name the plan instance, or a surface with two plans updates the wrong card")
	}
	if len(changes[1].Steps) != 1 || changes[1].Steps[0].Status != WorkStepDone {
		t.Errorf("the host is handed a snapshot of the plan as it now stands: %+v", changes[1].Steps)
	}
	if set.Pending() != 0 {
		t.Errorf("nothing is pending once the only step is done; got %d", set.Pending())
	}
}

// A step missing half of itself is refused. A title with no what_to_find is a
// step nobody can tell was finished.
func TestASetStepNeedsBothHalves(t *testing.T) {
	set := WorkPlanTools(WorkPlanToolSpec{})
	if _, err := set.Set.Handler(map[string]any{"steps": []any{
		map[string]any{"title": "look at it"},
	}}); err == nil {
		t.Error("a step with no what_to_find should be refused")
	}
	if _, err := set.Set.Handler(map[string]any{"steps": []any{}}); err == nil {
		t.Error("an empty plan is not a plan")
	}
}

// Revision is capped so the model works the plan instead of rewriting it.
func TestRevisionIsCapped(t *testing.T) {
	set := WorkPlanTools(WorkPlanToolSpec{})
	if _, err := set.Set.Handler(map[string]any{"steps": []any{
		map[string]any{"title": "a", "what_to_find": "x"},
	}}); err != nil {
		t.Fatal(err)
	}
	var last string
	for i := 0; i < WorkPlanRevisionLimit+1; i++ {
		out, err := set.Revise.Handler(map[string]any{"reason": "found something new"})
		if err != nil {
			t.Fatal(err)
		}
		last = out
	}
	if !strings.Contains(last, "REVISION LIMIT REACHED") {
		t.Errorf("past the cap the tool must refuse and say why: %q", last)
	}
}
