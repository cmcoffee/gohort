package orchestrate

// The churn detector: a turn that spent its tool calls without moving.
// Modeled on the live incident that destroyed a toolbox — seventeen
// tool_def calls in one turn, five with duplicate args, each reporting
// success, ending in a delete.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// feed replays rounds into the telemetry the way the agent loop does.
func feed(tt *turnTelemetry, round int, errs int, calls ...ToolCall) {
	tt.record(StepInfo{Round: round, ToolCalls: calls, ToolErrors: errs})
}

func call(name string, args map[string]any) ToolCall {
	return ToolCall{Name: name, Args: args}
}

func TestChurnDiag_HealthyTurnsAreQuiet(t *testing.T) {
	cases := map[string]func(*turnTelemetry){
		"a few distinct tools": func(tt *turnTelemetry) {
			feed(tt, 1, 0, call("web_search", map[string]any{"q": "a"}))
			feed(tt, 2, 0, call("fetch_url", map[string]any{"url": "x"}))
			feed(tt, 3, 0, call("web_search", map[string]any{"q": "b"}))
		},
		// Volume alone is a research turn, not a fight — no errors, no card.
		"many distinct searches, no errors": func(tt *turnTelemetry) {
			for i := 0; i < 12; i++ {
				feed(tt, i+1, 0, call("web_search", map[string]any{"q": string(rune('a' + i))}))
			}
		},
		"one repeat is not churn": func(tt *turnTelemetry) {
			feed(tt, 1, 1, call("moltbook", map[string]any{"action": "x"}))
			feed(tt, 2, 1, call("moltbook", map[string]any{"action": "x"}))
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			tt := newTurnTelemetry()
			build(tt)
			if _, detail, ok := tt.churnDiag(); ok {
				t.Errorf("healthy turn should not raise a card, got: %s", detail)
			}
		})
	}
}

func TestChurnDiag_IdenticalArgsRepeated(t *testing.T) {
	tt := newTurnTelemetry()
	args := map[string]any{"action": "update", "name": "moltbook"}
	for i := 0; i < churnDupLimit; i++ {
		feed(tt, i+1, 0, call("tool_def", args))
	}
	kind, detail, ok := tt.churnDiag()
	if !ok {
		t.Fatal("identical args repeated to the dup limit must raise a card")
	}
	if kind != "tool_churn" {
		t.Errorf("kind = %q", kind)
	}
	if !strings.Contains(detail, "tool_def") || !strings.Contains(detail, "identical") {
		t.Errorf("detail should name the tool and the reason: %s", detail)
	}
	// Note the incident's real tell: each call REPORTED SUCCESS, so errors
	// alone would never have caught it.
	if tt.toolErrorCount != 0 {
		t.Fatalf("guard precondition: this case has no errors, got %d", tt.toolErrorCount)
	}
}

func TestChurnDiag_HighVolumeWithErrors(t *testing.T) {
	tt := newTurnTelemetry()
	// Distinct args every round, so the dup rule can't be what fires.
	for i := 0; i < churnCallLimit; i++ {
		feed(tt, i+1, 1, call("tool_def", map[string]any{"action": "update", "n": i}))
	}
	_, detail, ok := tt.churnDiag()
	if !ok {
		t.Fatal("sustained volume WITH errors must raise a card")
	}
	if !strings.Contains(detail, "tool_def") || !strings.Contains(detail, "error") {
		t.Errorf("detail should name the tool and the errors: %s", detail)
	}
}

func TestChurnDiag_NilSafe(t *testing.T) {
	if _, _, ok := (*turnTelemetry)(nil).churnDiag(); ok {
		t.Error("nil telemetry must not raise a card")
	}
}
