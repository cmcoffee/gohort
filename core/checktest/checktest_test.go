package checktest

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A run asks every case of every registered check of each model, side by
// side, marks each answer pass or fail against the case's answer, keeps the
// run as the last, and sums it up per model and check. A stopped run keeps
// what it finished and says so.
func TestARunAsksEveryCaseOfEachModel(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	registry.mu.Lock()
	prev := registry.checks
	registry.checks = nil
	registry.mu.Unlock()
	t.Cleanup(func() { registry.mu.Lock(); registry.checks = prev; registry.mu.Unlock() })

	partial := true
	Register(Check{Key: "echo", Label: "Echo check",
		Cases: func() []Case {
			return []Case{{ID: "a", Title: "says yes", Want: "yes"}, {ID: "b", Title: "says no", Want: "no"}}
		},
		Run: func(ctx context.Context, llm LLM, c Case) Outcome {
			// The run is kept as it goes: by the second case, the first is
			// already readable, as a page polling the results reads it.
			if c.ID == "b" {
				if last, ok := Last(); !ok || len(last.Rows) != 1 {
					partial = false
				}
			}
			resp, err := llm.Chat(ctx, []Message{{Role: "user", Content: c.ID}})
			if err != nil {
				return Outcome{Err: err}
			}
			return Outcome{Got: resp.Content, Reason: "the model said " + resp.Content}
		}})
	Register(Check{Key: "echo", Label: "A second registration", Cases: func() []Case { return nil }})

	worker := &FakeLLM{Turns: []FakeTurn{{Content: "yes", Repeat: true}}}
	judge := &FakeLLM{Turns: []FakeTurn{{Content: "no", Repeat: true}}}
	rep := Run(context.Background(), []Model{{Name: "worker", LLM: worker}, {Name: "judge", LLM: judge}})
	if len(rep.Rows) != 2 || worker.Calls() != 2 || judge.Calls() != 2 {
		t.Fatalf("rows %d, worker asked %d, judge asked %d: want every case of both", len(rep.Rows), worker.Calls(), judge.Calls())
	}
	a := rep.Rows[0].Answers
	if !a["worker"].Pass || a["judge"].Pass || a["judge"].Got != "no" || a["judge"].Reason != "the model said no" {
		t.Errorf("case a, side by side: %+v", a)
	}
	if b := rep.Rows[1].Answers; b["worker"].Pass || !b["judge"].Pass {
		t.Errorf("case b, side by side: %+v", b)
	}
	if !partial {
		t.Error("the first case was not kept before the second ran; a page could not show the run as it goes")
	}
	if got := Summary(rep); got != "worker 1 of 2 (Echo check 1/2); judge 1 of 2 (Echo check 1/2)" {
		t.Errorf("summary %q", got)
	}
	if last, ok := Last(); !ok || len(last.Rows) != 2 || strings.Join(last.Models, ",") != "worker,judge" {
		t.Errorf("the run is not kept as the last: %+v", last)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stopped := Run(ctx, []Model{{Name: "worker", LLM: worker}})
	if !stopped.Canceled || len(stopped.Rows) != 0 || !strings.Contains(Summary(stopped), "stopped early") {
		t.Errorf("a stopped run: %+v %q", stopped, Summary(stopped))
	}
}

// A case a model does not answer in time is a failure, "timed out", and the
// run goes on to the next: one runaway generation cannot hold the run.
func TestACaseThatHangsTimesOut(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	registry.mu.Lock()
	prev := registry.checks
	registry.checks = nil
	registry.mu.Unlock()
	t.Cleanup(func() { registry.mu.Lock(); registry.checks = prev; registry.mu.Unlock() })
	prevTimeout := CaseTimeout
	CaseTimeout = 50 * time.Millisecond
	t.Cleanup(func() { CaseTimeout = prevTimeout })

	Register(Check{Key: "slow", Label: "Slow check",
		Cases: func() []Case { return []Case{{ID: "hangs", Want: "yes"}, {ID: "answers", Want: "yes"}} },
		Run: func(ctx context.Context, llm LLM, c Case) Outcome {
			if c.ID == "hangs" {
				<-ctx.Done()
				return Outcome{Err: ctx.Err()}
			}
			return Outcome{Got: "yes"}
		}})
	began := time.Now()
	rep := Run(context.Background(), []Model{{Name: "judge", LLM: &FakeLLM{}}})
	if time.Since(began) > 2*time.Second {
		t.Fatalf("the run waited %s on a hung case", time.Since(began))
	}
	if len(rep.Rows) != 2 {
		t.Fatalf("the run stopped at the hung case: %+v", rep.Rows)
	}
	if a := rep.Rows[0].Answers["judge"]; a.Pass || a.Got != "timed out" {
		t.Errorf("the hung case: %+v", a)
	}
	if a := rep.Rows[1].Answers["judge"]; !a.Pass {
		t.Errorf("the case after it: %+v", a)
	}
}

// A model asked as System One uses each check's System One form, and leaves
// out a check that has none, rather than asking it the full question.
func TestSystemOneUsesTheDecisionForm(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	registry.mu.Lock()
	prev := registry.checks
	registry.checks = nil
	registry.mu.Unlock()
	t.Cleanup(func() { registry.mu.Lock(); registry.checks = prev; registry.mu.Unlock() })
	one := func(id string) func() []Case { return func() []Case { return []Case{{ID: id, Want: "yes"}} } }
	Register(Check{Key: "both", Label: "Both forms", Cases: one("b"),
		Run:    func(context.Context, LLM, Case) Outcome { return Outcome{Got: "no", Reason: "full"} },
		Decide: func(context.Context, LLM, Case) Outcome { return Outcome{Got: "yes", Reason: "decided"} }})
	Register(Check{Key: "full", Label: "Full only", Cases: one("f"),
		Run: func(context.Context, LLM, Case) Outcome { return Outcome{Got: "yes"} }})
	rep := Run(context.Background(), []Model{{Name: "judge", LLM: &FakeLLM{}}, {Name: "system one", LLM: &FakeLLM{}, Decide: true}})
	for _, r := range rep.Rows {
		s1, has := r.Answers["system one"]
		switch r.Check {
		case "both":
			if !has || s1.Reason != "decided" || !s1.Pass || r.Answers["judge"].Reason != "full" {
				t.Errorf("a check with both forms: %+v", r.Answers)
			}
		case "full":
			if has {
				t.Errorf("a check with no decision form was asked as one: %+v", s1)
			}
		}
	}
}
