package servitor

import "testing"

// Chat must be able to launch a real investigation on a FOLLOW-UP. Before this,
// the plan group was built inside the Map branch only, so chat had read_doc,
// update_doc and probe — no way to say "this question is bigger than one probe".
func TestChatGetsThePlanGroupAndMapKeepsItRequired(t *testing.T) {
	chat := buildPlanTools("s1", false)
	if got := len(chat.All()); got != 6 {
		t.Fatalf("plan group should be 6 tools, got %d", got)
	}
	names := map[string]bool{}
	for _, td := range chat.All() {
		names[td.Tool.Name] = true
		// Every plan tool must be on the orchestrator allow-list, or servitor
		// panics at request setup rather than at build time.
		if !servitorOrchestratorToolAllowList[td.Tool.Name] {
			t.Errorf("%s is not on the orchestrator allow-list", td.Tool.Name)
		}
	}
	for _, want := range []string{"set_plan", "mark_step_in_progress", "record_step_findings", "mark_step_blocked", "revise_plan", "report_gaps"} {
		if !names[want] {
			t.Errorf("missing %s", want)
		}
	}

	// Map keeps set_plan mandatory (mapping a system IS the plan); chat must not
	// inherit that, or every one-probe follow-up pays a 5-step tax.
	mapSet := buildPlanTools("s2", true)
	if !contains(mapSet.Set.Tool.Description, "REQUIRED FIRST CALL") {
		t.Error("map's set_plan must stay the required first call")
	}
	if contains(chat.Set.Tool.Description, "REQUIRED FIRST CALL") {
		t.Error("chat's set_plan must be optional — most questions are one probe")
	}

	// Separate sessions must not share plan state.
	if chat.Plan == mapSet.Plan {
		t.Error("each session needs its own plan")
	}
}

// Pending drives the continuation budget: a capped pass with steps left is
// unfinished, not stuck, and must not be mistaken for "nothing new happened".
func TestPendingCountsUnfinishedWork(t *testing.T) {
	ts := buildPlanTools("s3", false)
	if ts.Pending() != 0 {
		t.Fatal("an unset plan has no pending work")
	}
	if _, err := ts.Set.Handler(map[string]any{"steps": []any{
		map[string]any{"title": "Find the config", "what_to_find": "path to app config"},
		map[string]any{"title": "Read the DB block", "what_to_find": "connection string"},
	}}); err != nil {
		t.Fatal(err)
	}
	if got := ts.Pending(); got != 2 {
		t.Fatalf("both steps pending, got %d", got)
	}
	if _, err := ts.Start.Handler(map[string]any{"step_id": float64(1)}); err != nil {
		t.Fatal(err)
	}
	if got := ts.Pending(); got != 2 {
		t.Fatalf("in-progress still counts as unfinished, got %d", got)
	}
	if _, err := ts.Findings.Handler(map[string]any{"step_id": float64(1), "findings": "/etc/app/config.yml"}); err != nil {
		t.Fatal(err)
	}
	if got := ts.Pending(); got != 1 {
		t.Fatalf("one done, one left, got %d", got)
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(hay); i++ {
			if hay[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
