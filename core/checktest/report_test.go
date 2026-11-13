package checktest

import (
	"context"
	"errors"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The report gives each case a model got wrong or could not answer in full,
// with the case's input and System One's confidence against its bar; an
// unsure answer gets one line; the cases every model got right are only
// counted.
func TestTheReportGivesTheMissesInFull(t *testing.T) {
	mapRegistry(t)
	Register(Check{Key: "gate", Label: "Gate", Structured: true,
		Cases: func() []Case {
			return []Case{
				{ID: "miss", Title: "A miss", Want: "block", Input: map[string]string{"message": "hello there", "rules": strings.Repeat("r", reportInputClip+50)}},
				{ID: "doubt", Title: "A doubt", Want: "allow"},
				{ID: "fine", Title: "Fine", Want: "allow"},
				{ID: "broke", Title: "Broke", Want: "allow"},
			}
		},
		Run: func(_ context.Context, _ LLM, c Case) Outcome {
			if c.ID == "broke" {
				return Outcome{Err: errors.New("the server answered HTTP 500")}
			}
			return Outcome{Got: c.Want, Reason: "worker reason"}
		},
		Decide: func(_ context.Context, _ LLM, c Case) Outcome {
			switch c.ID {
			case "miss":
				return Outcome{Got: "allow", Reason: "P(allow)=0.95", Confidence: 0.95, Bar: 0.9}
			case "doubt":
				return Outcome{Got: "block", Unsure: true, Confidence: 0.6, Bar: 0.9}
			}
			return Outcome{Got: c.Want, Confidence: 0.99, Bar: 0.9}
		}})
	rep := Run(context.Background(), []Model{{Name: "worker", LLM: &FakeLLM{}}, {Name: "system one", LLM: &FakeLLM{}, Decide: true}})
	got := TextReport(rep, []string{"gohort 1.2.3"})
	for _, want := range []string{
		"gohort 1.2.3",
		"Wrong or failed (2):",
		"[Gate] A miss (gate / miss)\n  want: block\n  input:\n    message: hello there\n",
		"system one: allow, ",
		"WRONG (confidence 0.95, needs 0.90)\n    reason: P(allow)=0.95",
		"[Gate] Broke (gate / broke)",
		"FAIL (no answer)\n    error: the server answered HTTP 500",
		"Unsure, not used, so no harm (1):\n- [Gate] A doubt, want allow: system one: block, ",
		"wrong, unsure (confidence 0.60, needs 0.90)",
		"Right on every model: 1 of 4 cases.",
		"- Gate (gate): Offload model:",
		strings.Repeat("r", reportInputClip) + "...\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Fine") {
		t.Errorf("a case every model got right is only counted:\n%s", got)
	}
	if strings.Contains(got, strings.Repeat("r", reportInputClip+1)) {
		t.Errorf("a long input is cut")
	}
}

// Before any run, the report says so.
func TestTheReportBeforeARun(t *testing.T) {
	if got := TextReport(RunReport{}, nil); !strings.Contains(got, "Not run yet.") {
		t.Errorf("before a run: %q", got)
	}
}
