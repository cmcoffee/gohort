package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Builder's plan, driven the way a run drives it: through the six tools, the
// ask_user plan, the authoring auto-advance and the checks at the end of a
// turn. Everything the model reads back and everything the card shows is
// written to one transcript and compared with testdata/build_plan_contract.golden,
// so a change to the state underneath that moves a word the model sees, or a
// row the user sees, shows up here as a diff.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestBuildPlanContract(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	turn := &chatTurn{udb: db, sse: &sseWriter{live: &syncBuf{}}, session: &ChatSession{ID: "s1"}, agent: AgentRecord{ID: "seed-builder"}}
	ctx := context.Background()

	var log strings.Builder
	cards := map[string]string{}
	label := func(id string) string {
		if l, ok := cards[id]; ok {
			return l
		}
		l := fmt.Sprintf("card#%d", len(cards)+1)
		cards[id] = l
		return l
	}
	dumpCards := func() {
		for _, b := range turn.session.UIBlocks {
			if b.Type != "orchestrate_plan" {
				continue
			}
			data, _ := json.Marshal(b.Plan)
			fmt.Fprintf(&log, "  %s %s\n", label(b.ID), data)
		}
	}
	call := func(name string, def AgentToolDef, args map[string]any) {
		out, err := def.Handler(ctx, args)
		a, _ := json.Marshal(args)
		fmt.Fprintf(&log, "%s %s\n", name, a)
		if err != nil {
			fmt.Fprintf(&log, "  ERR %v\n", err)
		} else {
			fmt.Fprintf(&log, "  -> %s\n", out)
		}
		dumpCards()
	}
	note := func(name string, fired bool) {
		fmt.Fprintf(&log, "%s fired=%v\n", name, fired)
		if fired {
			fmt.Fprintf(&log, "  note: %s\n", turn.session.Messages[len(turn.session.Messages)-1].Content)
		}
	}
	steps := func(titles ...string) []any {
		var out []any
		for _, s := range titles {
			title, detail, _ := strings.Cut(s, "|")
			out = append(out, map[string]any{"title": title, "detail": detail})
		}
		return out
	}
	present := func(titles ...string) {
		call("present_build_plan", turn.presentBuildPlanToolDef(), map[string]any{"steps": steps(titles...)})
		fmt.Fprintf(&log, "  budget=%d\n", turn.planBudgetCap)
	}
	done := func(step any, summary string) {
		call("mark_step_done", turn.markStepDoneToolDef(), map[string]any{"step": step, "summary": summary})
	}
	start := func(step any) {
		call("mark_step_in_progress", turn.markStepInProgressToolDef(), map[string]any{"step": step})
	}
	blocked := func(step any, reason string) {
		call("mark_step_blocked", turn.markStepBlockedToolDef(), map[string]any{"step": step, "reason": reason})
	}
	revise := func(args map[string]any) { call("revise_build_plan", turn.reviseBuildPlanToolDef(), args) }
	gaps := func() { call("report_build_gaps", turn.reportBuildGapsToolDef(), map[string]any{}) }
	advance := func(summary string) {
		fmt.Fprintf(&log, "auto_advance %q -> %d\n", summary, turn.autoAdvanceBuildPlan(summary))
		dumpCards()
	}

	// Before any plan: every tool says so in its own way.
	done(1, "x")
	start(1)
	blocked(1, "x")
	revise(map[string]any{"action": "add", "steps": steps("x")})
	gaps()
	note("mismatch_warning", injectAuthoringMismatchWarning(turn.session, nil, "I built it."))
	note("skipped_gap_warning", injectSkippedGapReportWarning(turn.session, db, "All done."))

	// A plan, worked: in progress, the auto-complete of the step before, done,
	// the numbers that are not there, a blocker without and with its reason.
	present("Create agent shell|create_agent", "  Add search tool  |add_tool(api)", "|skipped, no title", "Verify|")
	start(1)
	start(float64(2))
	done(2, "Tool search attached")
	done(9, "x")
	start(0)
	blocked(3, "")
	note("mismatch_warning", injectAuthoringMismatchWarning(turn.session, nil, "I built the agent."))
	note("mismatch_warning_after_authoring", injectAuthoringMismatchWarning(turn.session, []PersistedToolCall{{Name: "create_agent"}}, "I built the agent."))
	blocked(3, "credential not registered")

	// Revisions: an add, a refused remove, the advance an authoring tool makes,
	// the reorder and its refusals, and the cap.
	revise(map[string]any{"action": "add", "steps": steps("Retry verify|tool_def test")})
	revise(map[string]any{"action": "remove", "step_numbers": []any{float64(3)}})
	revise(map[string]any{"action": "remove"})
	advance("AGENT_CREATED ok. id=a1")
	advance("")
	advance("nothing left")
	revise(map[string]any{"action": "reorder", "order": []any{float64(4), float64(1), "2", float64(3)}})
	revise(map[string]any{"action": "reorder", "order": []any{float64(1), float64(2)}})
	revise(map[string]any{"action": "reorder", "order": []any{float64(1), float64(2), float64(3), float64(9)}})
	revise(map[string]any{"action": "reorder", "order": []any{float64(1), float64(1), float64(2), float64(3)}})
	revise(map[string]any{"action": "bogus"})
	revise(map[string]any{"action": "add", "steps": []any{map[string]any{"title": "  "}}})
	revise(map[string]any{"action": "add", "steps": steps("Smoke test|dispatch")})
	revise(map[string]any{"action": "add", "steps": steps("One too many")})

	// The finish: closing the plan without the gap report, the check that
	// stands in for it, and the report with a tool that has not passed.
	note("skipped_gap_warning", injectSkippedGapReportWarning(turn.session, db, "All done."))
	done(5, "smoke passed")
	note("mismatch_warning", injectAuthoringMismatchWarning(turn.session, nil, "All done."))
	note("skipped_gap_warning", injectSkippedGapReportWarning(turn.session, db, "All done."))
	notice, strike := turn.buildGapsFinishCheck()("All done.")
	fmt.Fprintf(&log, "finish_check clean notice=%q strike=%q\n", notice, strike)
	note("skipped_gap_warning", injectSkippedGapReportWarning(turn.session, db, "All done."))
	recordToolVerify(db, "s1", "search_tool", false, "never tested")
	gaps()
	notice, strike = turn.buildGapsFinishCheck()("It works now.")
	fmt.Fprintf(&log, "finish_check unverified notice=%q strike=%q\n", notice, strike)

	// A plan after a finished one gets a card of its own and a fresh revision
	// count; replaced midway, it keeps its card.
	present("Draft persona|update_agent", "Attach tool|add_tool")
	revise(map[string]any{"action": "add", "steps": steps("Verify|dispatch")})
	present("Draft persona|update_agent", "Attach tool|add_tool", "Verify|dispatch")
	blocked(1, "user declined")
	gaps()

	// The one-call Phase 2 shape: the plan rides on ask_user.
	pr := &planRun{t: turn, resp: &Response{}, telem: newTurnTelemetry(), cancelOrch: func() {}}
	turn.session.UIBlocks = nil
	cards = map[string]string{}
	call("ask_user", pr.askUserToolDef(), map[string]any{"question": "Approve this plan?", "plan": steps("Create agent|create_agent", "Verify|")})
	fmt.Fprintf(&log, "  budget=%d\n", turn.planBudgetCap)
	call("ask_user", pr.askUserToolDef(), map[string]any{"question": "Anything else?", "plan": []any{}})
	start(2)
	gaps()

	// Finished with step 2 still in progress, then closed: the remaining count
	// counts it, and the model's own gap report is what quiets the warning.
	done(1, "agent created")
	done(2, "verified")
	gaps()
	note("skipped_gap_warning", injectSkippedGapReportWarning(turn.session, db, "All done."))

	got := log.String()
	const golden = "testdata/build_plan_contract.golden"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read %s: %v (run with UPDATE_GOLDEN=1 to create it)", golden, err)
	}
	if got != string(want) {
		gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := 0; i < len(gl) || i < len(wl); i++ {
			var g, w string
			if i < len(gl) {
				g = gl[i]
			}
			if i < len(wl) {
				w = wl[i]
			}
			if g != w {
				t.Fatalf("transcript differs from %s at line %d:\n got: %s\nwant: %s", golden, i+1, g, w)
			}
		}
	}
}
