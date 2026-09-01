package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A standing fire is the run nobody watches happen — it goes off at 5am with
// the tab closed — so its record is all there is. It recorded the agent's text
// and nothing about what the agent DID, while every other scheduled surface
// recorded both. StandingRunResult had no field for a trace at all, so the
// runner could not have reported one even having collected it.
func TestStandingResultCarriesATrace(t *testing.T) {
	res := StandingRunResult{
		Status:  RunOK,
		Summary: "checked the feeds",
		Steps: runStepsFromToolCalls([]PersistedToolCall{
			{Name: "fetch_url", Args: map[string]any{"url": "https://example.com"}, Result: "200 ok"},
			{Name: "send_message", Err: "channel not found"},
		}),
	}
	if len(res.Steps) != 2 {
		t.Fatalf("both calls must survive the conversion: %+v", res.Steps)
	}
	if res.Steps[0].Name != "fetch_url" || res.Steps[0].Result != "200 ok" {
		t.Errorf("a call's name and result are the point of keeping it: %+v", res.Steps[0])
	}
	if res.Steps[0].Args == "" {
		t.Error("arguments distinguish two calls to the same tool; without them the trace cannot answer WHAT was fetched")
	}
	// A failed call is the one most worth having.
	if res.Steps[1].Err != "channel not found" {
		t.Errorf("the failure must be carried, not flattened away: %+v", res.Steps[1])
	}
}

// A run with no tool calls must record none rather than an empty-ish step —
// "it called nothing" and "it called something unnameable" are different
// findings when you are working out why a schedule produced nothing.
func TestNoToolCallsRecordsNoSteps(t *testing.T) {
	if steps := runStepsFromToolCalls(nil); steps != nil {
		t.Errorf("no calls means no steps, got %+v", steps)
	}
	if steps := runStepsFromToolCalls([]PersistedToolCall{}); steps != nil {
		t.Errorf("an empty trace is still no steps, got %+v", steps)
	}
}
