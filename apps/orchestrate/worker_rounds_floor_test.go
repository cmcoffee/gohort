package orchestrate

import "testing"

func TestResolveMaxWorkerRoundsFloor(t *testing.T) {
	if got := resolveMaxWorkerRounds(AgentRecord{MaxWorkerRounds: 3}); got != minWorkerRounds {
		t.Errorf("an explicit 3 should floor to %d, got %d", minWorkerRounds, got)
	}
	if got := resolveMaxWorkerRounds(AgentRecord{MaxWorkerRounds: minWorkerRounds}); got != minWorkerRounds {
		t.Errorf("exactly the floor stays: got %d", got)
	}
	if got := resolveMaxWorkerRounds(AgentRecord{MaxWorkerRounds: 10}); got != 10 {
		t.Errorf("a value above the floor is unchanged, got %d", got)
	}
	if got := resolveMaxWorkerRounds(AgentRecord{}); got != defaultMaxWorkerRounds {
		t.Errorf("unset uses the default %d, got %d", defaultMaxWorkerRounds, got)
	}
}
