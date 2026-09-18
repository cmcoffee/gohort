package core

import (
	"context"
	"testing"
)

// One route key must resolve to the SAME thinking setting whichever entry point
// the call arrives through. WorkerChat's fallback for a nil RouteThink is ON
// ("worker tier: thinking on by default"); LeadChat's worker-redirect defaulted
// OFF, so a single key thought or did not depending on how it was reached.
//
// The branch is reachable in production, not just here: RouteThink returns nil
// for the stored values "lead" and "" as well as for a nil lookup, and a
// private stage carrying a stale "lead" lands exactly there.
func TestWorkerThinkingDefaultMatchesAcrossEntryPoints(t *testing.T) {
	// A PRIVATE stage whose value is "lead" — the reachable shape of the
	// fallback. RouteToLead refuses the escalation because the stage is
	// private, so LeadChat takes its worker-redirect; RouteThink returns nil
	// because "lead" carries no thinking preference. Both entry points then
	// fall through to their own defaults, which is where they disagreed.
	const key = "test.thinkparity"
	RegisterRouteStage(RouteStage{Key: key, Label: "test", Default: "lead", Private: true})

	worker := &FakeLLM{Turns: []FakeTurn{{Content: "ok", Repeat: true}}}
	lead := &FakeLLM{Turns: []FakeTurn{{Content: "ok", Repeat: true}}}
	app := &AppCore{LLM: worker, LeadLLM: lead}

	if _, err := app.WorkerChat(context.Background(), []Message{{Role: "user", Content: "hi"}}, WithRouteKey(key)); err != nil {
		t.Fatalf("WorkerChat: %v", err)
	}
	// Routed to the worker by the lead entry point — the redirect branch. Both
	// calls land on the SAME worker, so the two resolutions are its first and
	// second call rather than something that has to be reset between them.
	if _, err := app.LeadChat(context.Background(), []Message{{Role: "user", Content: "hi"}}, WithRouteKey(key)); err != nil {
		t.Fatalf("LeadChat: %v", err)
	}
	if worker.Calls() != 2 {
		t.Fatalf("the lead entry point did not redirect to the worker: %d call(s)", worker.Calls())
	}
	viaWorker, viaLead := worker.Config(0).Think, worker.Config(1).Think
	if lead.Calls() != 0 {
		t.Errorf("the lead model was asked %d time(s); this route redirects", lead.Calls())
	}

	if viaWorker == nil || viaLead == nil {
		t.Fatalf("no Think resolved: worker=%v lead-redirect=%v", viaWorker, viaLead)
	}
	if *viaWorker != *viaLead {
		t.Fatalf("route %q thinks %v via WorkerChat and %v via the LeadChat worker-redirect; one key, one answer", key, *viaWorker, *viaLead)
	}
	if !*viaWorker {
		t.Errorf("the worker default is documented as thinking ON; got %v", *viaWorker)
	}
}

// PanelMaxVoices exists so the editor's stated cap and the validator's cannot
// drift. It was exported for that and then left unreferenced, with the form
// spelling "Two to eight" out in prose — the drift the export was meant to
// prevent, inside the mechanism meant to prevent it.
func TestPanelCapsStayLinkedToTheValidator(t *testing.T) {
	if PanelMaxVoices != panelMaxVoices {
		t.Fatalf("PanelMaxVoices (%d) has drifted from the validator's cap (%d)", PanelMaxVoices, panelMaxVoices)
	}
	if PanelMaxRounds != panelMaxRounds {
		t.Fatalf("PanelMaxRounds (%d) has drifted from the validator's cap (%d)", PanelMaxRounds, panelMaxRounds)
	}
}
