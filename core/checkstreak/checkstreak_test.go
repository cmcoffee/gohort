package checkstreak

import (
	"context"
	"testing"

	"github.com/cmcoffee/gohort/core/llmcalls"
)

type told struct {
	check, model, reason string
	n                    int
}

func listen(t *testing.T) *[]told {
	t.Helper()
	var got []told
	prev := OnStreak
	OnStreak = func(check, model, reason string, n int) { got = append(got, told{check, model, reason, n}) }
	mu.Lock()
	runs = map[string]*run{}
	mu.Unlock()
	t.Cleanup(func() { OnStreak = prev })
	return &got
}

// After a run of no-verdicts in a row on one model, the check is told once,
// with the last reason; a verdict from that model ends the run, and the next
// run is told again.
func TestARunOfNoVerdictsIsToldOnce(t *testing.T) {
	got := listen(t)
	ctx := context.Background()
	for i := 1; i < After; i++ {
		Failed(ctx, "memory.gate", "m1", "first")
	}
	if len(*got) != 0 {
		t.Fatalf("told before %d in a row: %+v", After, *got)
	}
	Failed(ctx, "memory.gate", "m1", "the last")
	Failed(ctx, "memory.gate", "m1", "later")
	if len(*got) != 1 || (*got)[0] != (told{"memory.gate", "m1", "the last", After}) {
		t.Fatalf("a run of %d: %+v", After, *got)
	}
	Answered(ctx, "memory.gate", "m1")
	for i := 0; i < After-1; i++ {
		Failed(ctx, "memory.gate", "m1", "again")
	}
	if len(*got) != 1 {
		t.Fatalf("a verdict did not start the count over: %+v", *got)
	}
	Failed(ctx, "memory.gate", "m1", "again")
	if len(*got) != 2 {
		t.Errorf("a second run was not told: %+v", *got)
	}
}

// Runs are per check and per model: another model's verdict, or another
// check's failures, neither end nor add to one.
func TestARunIsOneCheckOnOneModel(t *testing.T) {
	got := listen(t)
	ctx := context.Background()
	for i := 0; i < After-1; i++ {
		Failed(ctx, "memory.gate", "m1", "x")
		Failed(ctx, "memory.conflict_check", "m1", "x")
		Failed(ctx, "memory.gate", "m2", "x")
	}
	Answered(ctx, "memory.gate", "m2")
	if len(*got) != 0 {
		t.Fatalf("another check's or another model's failures made a run: %+v", *got)
	}
	Failed(ctx, "memory.gate", "m1", "x")
	if len(*got) != 1 || (*got)[0].check != "memory.gate" || (*got)[0].model != "m1" {
		t.Errorf("told: %+v", *got)
	}
}

// A check test asks a model on purpose, case by case: its failures are not a
// run, and its verdicts do not end one. A check with no key is not counted.
func TestACheckTestIsNotCounted(t *testing.T) {
	got := listen(t)
	watched, _ := llmcalls.Watch(context.Background())
	for i := 0; i < After+1; i++ {
		Failed(watched, "memory.gate", "m1", "x")
		Failed(context.Background(), " ", "m1", "x")
	}
	if len(*got) != 0 {
		t.Fatalf("a check test's failures were told: %+v", *got)
	}
	for i := 0; i < After-1; i++ {
		Failed(context.Background(), "memory.gate", "m1", "x")
	}
	Answered(watched, "memory.gate", "m1")
	Failed(context.Background(), "memory.gate", "m1", "x")
	if len(*got) != 1 {
		t.Errorf("a check test's verdict ended a live run: %+v", *got)
	}
	OnStreak = nil
	for i := 0; i < After; i++ {
		Failed(context.Background(), "memory.conflict_check", "m9", "x")
	}
}
