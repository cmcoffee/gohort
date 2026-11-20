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

// A failed or wrong answer says how its model's replies ended: one cut off at
// its output limit reads as that, not as the model being wrong. A right one
// does not.
func TestAFailedAnswerSaysHowItsRepliesEnded(t *testing.T) {
	mapRegistry(t)
	Register(Check{Key: "conflict", Label: "Conflict", Structured: true,
		Cases: func() []Case { return []Case{{ID: "cut", Title: "Cut", Want: "none"}, {ID: "fine", Title: "Fine", Want: "none"}} },
		Run: func(ctx context.Context, _ LLM, c Case) Outcome {
			// What a model client records of each reply (core.RecordUsage).
			if c.ID == "cut" {
				RecordUsage(ctx, WORKER, &Response{StopReason: "max_tokens", InputTokens: 900, OutputTokens: 600, ReasoningTokens: 580})
				return Outcome{Err: errors.New("the reply could not be read as the verdict asked: the reply was empty")}
			}
			RecordUsage(ctx, WORKER, &Response{StopReason: "end_turn", InputTokens: 900, OutputTokens: 40})
			return Outcome{Got: "none"}
		},
		// Right on the case the worker failed, so its line is in the report.
		Decide: func(ctx context.Context, _ LLM, c Case) Outcome {
			RecordUsage(ctx, WORKER, &Response{StopReason: "end_turn", InputTokens: 900, OutputTokens: 41})
			return Outcome{Got: "none", Confidence: 0.99, Bar: 0.6}
		}})
	rep := Run(context.Background(), []Model{{Name: "worker", LLM: &FakeLLM{}}, {Name: "system one", LLM: &FakeLLM{}, Decide: true}})
	var cut, fine Answer
	for _, r := range rep.Rows {
		if r.Case == "cut" {
			cut = r.Answers["worker"]
		} else {
			fine = r.Answers["worker"]
		}
	}
	if cut.Replies != "stopped at its output limit after 600 output tokens, about 580 of them thinking, 900 in (worker)" {
		t.Errorf("the cut case's replies: %q", cut.Replies)
	}
	if !strings.Contains(fine.Replies, "finished after 40 output tokens") {
		t.Errorf("the right case's replies: %q", fine.Replies)
	}
	got := TextReport(rep, nil)
	if !strings.Contains(got, "the reply was empty\n    replies: stopped at its output limit after 600 output tokens, about 580 of them thinking") {
		t.Errorf("the report does not say how the failed reply ended:\n%s", got)
	}
	if !strings.Contains(got, "system one: none, ") || strings.Contains(got, "finished after 41") || strings.Contains(got, "finished after 40") {
		t.Errorf("a right answer's replies are in the report:\n%s", got)
	}
}
