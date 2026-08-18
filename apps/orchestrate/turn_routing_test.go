package orchestrate

// The tier a turn runs on and the reasoning it does are ONE decision. They used
// to be two: the route key came from the agent's "Use Lead model" toggle and the
// pin came from the machine phase, so a phase naming "lead" moved the pin, left
// the key on the worker stage, and got neither the model nor its reasoning —
// RouteThink reads that key, and so does the streaming tier resolution.

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

const (
	leadRoute   = "app.orchestrate.orchestrator.lead"
	workerRoute = "app.orchestrate.orchestrator"
)

func routingTurn(leadModel bool, phaseModel string, machineOn bool) *chatTurn {
	t := &chatTurn{agent: AgentRecord{ID: "custom", Name: "A", LeadModel: leadModel}}
	if machineOn {
		t.machine = turnMachine{on: true, phase: MachinePhase{Name: "p", Model: phaseModel}}
	}
	return t
}

func TestTurnRouting_PhasePinDecidesBothTierAndRoute(t *testing.T) {
	cases := []struct {
		name      string
		leadModel bool
		machineOn bool
		phase     string
		wantPin   LLMTier
		wantRoute string
	}{
		// No machine: exactly what it always was.
		{"no machine, agent on lead", true, false, "", TierUnset, leadRoute},
		{"no machine, agent on worker", false, false, "", TierUnset, workerRoute},
		// A phase that names no model inherits, pin included.
		{"phase names no model", true, true, "", TierUnset, leadRoute},
		// The bug: a phase asking for the lead inside a worker-routed agent.
		// The route key has to move too, or RouteThink answers for the wrong
		// stage and the streamed call goes to the wrong model.
		{"phase pins lead on a worker agent", false, true, "lead", LEAD, leadRoute},
		// And the reverse — a cheap classify phase inside an agent that
		// otherwise always escalates.
		{"phase pins worker on a lead agent", true, true, "worker", WORKER, workerRoute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pin, route := routingTurn(tc.leadModel, tc.phase, tc.machineOn).turnRouting()
			if pin != tc.wantPin {
				t.Errorf("pin = %v, want %v", pin, tc.wantPin)
			}
			if route != tc.wantRoute {
				t.Errorf("route key = %q, want %q", route, tc.wantRoute)
			}
		})
	}
}

// TestTurnRouting_PinCannotCrossThePrivacyPin: a phase pin is the most specific
// ROUTING setting there is, and routing is a preference. Privacy is not.
func TestTurnRouting_PinCannotCrossThePrivacyPin(t *testing.T) {
	turn := routingTurn(false, "lead", true)
	turn.privateMode = true
	pin, route := turn.turnRouting()
	if pin != WORKER || route != workerRoute {
		t.Fatalf("a private turn must stay on the worker; got pin=%v route=%q", pin, route)
	}
	// The pin resolves DOWN rather than being dropped, so the loop and the
	// route key can never disagree about where the turn ran.
	turn = routingTurn(true, "lead", true)
	turn.agent.ForcePrivate = true
	if pin, route := turn.turnRouting(); pin != WORKER || route != workerRoute {
		t.Fatalf("ForcePrivate must hold a lead-pinned phase down; got pin=%v route=%q", pin, route)
	}
}
