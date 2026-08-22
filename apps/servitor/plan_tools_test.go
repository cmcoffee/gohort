package servitor

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

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

// Two investigations in one chat session must post two checklists. The plan
// block used to carry a constant id, so the follow-up's plan rewrote the first
// one's card in place — the original investigation's steps and findings were
// replaced by the new plan rather than kept above it.
func TestEachInvestigationGetsItsOwnPlanBlock(t *testing.T) {
	first := buildPlanTools("same-session", false)
	second := buildPlanTools("same-session", false)

	if first.ID == "" || second.ID == "" {
		t.Fatal("every plan instance needs an id to key its block on")
	}
	if first.ID == second.ID {
		t.Fatal("a second investigation in the same session must not reuse the first's id")
	}

	// Through the real bridge: distinct plans must produce distinct block ids,
	// and every event of ONE plan must keep landing on that same card.
	blockID := func(planID, kind string) string {
		out := translateProbeEvent(probeEvent{Kind: kind, PlanID: planID, Plan: []WorkPlanStep{{ID: 1, Title: "x"}}})
		id, _ := out["id"].(string)
		return id
	}
	a := blockID(first.ID, "plan_set")
	b := blockID(second.ID, "plan_set")
	if a == "" || b == "" {
		t.Fatal("plan events must render as identified blocks")
	}
	if a == b {
		t.Fatal("two investigations rendered onto one block — the follow-up overwrites the first checklist")
	}
	if got := blockID(first.ID, "plan_step"); got != a {
		t.Errorf("step updates must land on their own plan's card: %q vs %q", got, a)
	}

	// An event with no plan id (any other emitter) still renders, on the
	// original shared block — no regression for callers that don't set one.
	if got := blockID("", "plan_set"); got != "servitor-plan" {
		t.Errorf("an unidentified plan must keep the legacy block id, got %q", got)
	}
}
