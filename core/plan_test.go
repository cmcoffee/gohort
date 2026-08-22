// The decisions in a work plan, tested apart from any host.
//
// These are the rules that make a plan worth having rather than a checklist
// card: history cannot be edited away, revision is capped so the model works the
// plan instead of rewriting it, and an unfinished step reaches the answer as a
// stated gap.
package core

import (
	"encoding/json"
	"strings"
	"testing"
)

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
