package core

import "testing"

// The tier dial for a custom app's pipeline. The whole risk in it is the
// DEFAULT: RouteValueIsLead treats every value outside the closed worker set —
// the empty string included — as lead, and these keys are born at runtime while
// the route registry lives in memory. Resolving through RouteToLead would
// therefore promote every stage of every pipeline to the precision tier for the
// whole stretch after a restart, and nothing would fail: the runs would cost
// more and read slightly better.

func withRoute(t *testing.T, values map[string]string) {
	t.Helper()
	prev := LookupRouteFunc
	LookupRouteFunc = func(key string) string { return values[key] }
	t.Cleanup(func() { LookupRouteFunc = prev })
}

// The regression the design exists to prevent. No override stored anywhere:
// every stage must resolve exactly as its author wrote it.
func TestNoOverrideMeansTheAuthorDecides(t *testing.T) {
	withRoute(t, nil)
	r := &pipelineRun{defID: "p1"}
	for _, tc := range []struct {
		model string
		want  LLMTier
	}{
		{"", WORKER}, // the case that would have flipped to LEAD
		{"worker", WORKER},
		{"lead", LEAD},
	} {
		if got := r.stageTierFor(PipelineStage{Name: "judge", Model: tc.model}); got != tc.want {
			t.Errorf("model %q with no override = %v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestAnOperatorOverrideWins(t *testing.T) {
	withRoute(t, map[string]string{
		"pipeline.p1.judge": "worker",
		"pipeline.p1.draft": "lead",
		"pipeline.p1.think": "lead (thinking)",
	})
	r := &pipelineRun{defID: "p1"}
	if got := r.stageTierFor(PipelineStage{Name: "judge", Model: "lead"}); got != WORKER {
		t.Errorf("an override to worker on a lead stage = %v", got)
	}
	if got := r.stageTierFor(PipelineStage{Name: "draft"}); got != LEAD {
		t.Errorf("an override to lead on an unset stage = %v", got)
	}
	// The four legal values carry thinking as well as tier; only the tier is
	// this function's business.
	if got := r.stageTierFor(PipelineStage{Name: "think"}); got != LEAD {
		t.Errorf("\"lead (thinking)\" = %v, want LEAD", got)
	}
}

// An override belongs to one stage of one definition. A run with no stored
// definition behind it — a pipeline invoked inline, a test — has no key at all
// and must fall back rather than collide with another definition's dial.
func TestOverridesAreScopedToTheirDefinitionAndStage(t *testing.T) {
	withRoute(t, map[string]string{"pipeline.p1.judge": "lead"})
	if got := (&pipelineRun{defID: "p2"}).stageTierFor(PipelineStage{Name: "judge"}); got != WORKER {
		t.Errorf("another definition's override applied: %v", got)
	}
	if got := (&pipelineRun{defID: "p1"}).stageTierFor(PipelineStage{Name: "other"}); got != WORKER {
		t.Errorf("another stage's override applied: %v", got)
	}
	if got := (&pipelineRun{}).stageTierFor(PipelineStage{Name: "judge"}); got != WORKER {
		t.Errorf("a run with no definition took an override: %v", got)
	}
	if PipelineStageRouteKey("", "judge") != "" || PipelineStageRouteKey("p1", "") != "" {
		t.Error("an incomplete key must be empty, not a prefix that could collide")
	}
}

// RouteOverride reports what was STORED and nothing else. routeEffectiveVal
// answers a different question and folding in its defaults here is the bug.
func TestRouteOverrideReportsOnlyWhatWasStored(t *testing.T) {
	withRoute(t, map[string]string{"set.key": "worker"})
	if got := RouteOverride("set.key"); got != "worker" {
		t.Errorf("RouteOverride(set) = %q", got)
	}
	if got := RouteOverride("unset.key"); got != "" {
		t.Errorf("RouteOverride(unset) = %q, want empty — empty is how a caller knows nobody set it", got)
	}
	if got := RouteOverride(""); got != "" {
		t.Errorf("RouteOverride(\"\") = %q", got)
	}
}
