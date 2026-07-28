package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func stageByKey(t *testing.T, key string) RouteStage {
	t.Helper()
	for _, s := range ListRouteStages() {
		if s.Key == key {
			return s
		}
	}
	t.Fatalf("route stage %q is not registered", key)
	return RouteStage{}
}

// Builder has NO dedicated routing stage. It used to — defaulting to lead —
// which meant two controls decided one thing: an admin routing row AND the
// per-agent "Use Lead model" toggle. Worse, the toggle was inert for Builder,
// because orchestratorRouteKey returned the dedicated stage before ever
// reading it. One control, in the place an owner already looks.
func TestNoDedicatedBuilderStage(t *testing.T) {
	for _, s := range ListRouteStages() {
		if s.Key == "app.orchestrate.builder" {
			t.Fatal("the Builder routing stage is back — it duplicates the per-agent \"Use Lead model\" toggle and made that toggle inert")
		}
	}
}

// Builder off the toggle runs on the worker like any other agent, and reaches
// the lead through `consult`.
func TestBuilderRoutesLikeAnyAgent(t *testing.T) {
	if got := orchestratorRouteKey("seed-builder", false); got != "app.orchestrate.orchestrator" {
		t.Errorf("Builder without the lead toggle routed to %q, want the worker-locked orchestrator stage", got)
	}
	// And the toggle now actually WORKS for Builder — the whole point.
	if got := orchestratorRouteKey("seed-builder", true); got != "app.orchestrate.orchestrator.lead" {
		t.Errorf("Builder WITH the lead toggle routed to %q — the toggle is still inert", got)
	}
}

// The escape hatch has to remain an escape hatch. Worker-driven Builder is
// only reasonable BECAUSE consult can still reach the stronger model for the
// questions worth escalating; if consult also dropped to worker there would be
// no path to the lead at all.
func TestConsultStillEscalatesToLead(t *testing.T) {
	s := stageByKey(t, ConsultRouteKey)
	if s.Default == "worker" || s.Default == "worker (thinking)" {
		t.Errorf("consult defaults to %q — with Builder on the worker this removes every path to the lead", s.Default)
	}
	if s.Private {
		t.Error("consult is Private, so it can never escalate")
	}
}

// Route-key selection is unchanged: Builder has its own stage, other agents
// pick lead-vs-worker from their own flag.
func TestOrchestratorRouteKeySelection(t *testing.T) {
	if got := orchestratorRouteKey("other", true); !strings.HasSuffix(got, ".lead") {
		t.Errorf("an opted-in agent routed to %q", got)
	}
	if got := orchestratorRouteKey("other", false); got != "app.orchestrate.orchestrator" {
		t.Errorf("a plain agent routed to %q", got)
	}
}
