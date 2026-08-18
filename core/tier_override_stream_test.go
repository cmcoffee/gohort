package core

// A per-run tier pin has to survive the STREAMING path. It didn't: the tier was
// resolved from AgentLoopConfig on the plain-Chat branch and from the route key
// alone on the streaming one, so a pin worked everywhere except the path every
// interactive turn takes. A machine phase naming "lead" is what surfaced it.

import (
	"context"
	"testing"
)

func noToolCalls(int) []ToolCall { return nil }

// streamedTiers runs one streaming round and reports which tier served it.
func streamedTiers(t *testing.T, app *AppCore, cfg AgentLoopConfig, log *[]string) []string {
	t.Helper()
	cfg.MaxRounds = 1
	cfg.Stream = func(string) {}
	if _, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "hi"}}, cfg); err != nil {
		t.Fatalf("loop: %v", err)
	}
	return *log
}

func TestStreamingHonorsTierOverride(t *testing.T) {
	app, log := withTierStubs(t, "test.tierpin.lead", noToolCalls)
	RegisterRouteStage(RouteStage{Key: "test.tierpin.worker", Label: "test", Default: "worker"})

	// Control: with no pin, the stage still decides — a worker stage streams
	// from the worker.
	got := streamedTiers(t, app, AgentLoopConfig{
		ChatOptions: []ChatOption{WithRouteKey("test.tierpin.worker")},
	}, log)
	if len(got) != 1 || got[0] != "worker" {
		t.Fatalf("control: a worker stage must stream from the worker; got %v", got)
	}

	// The fix: a pin beats the stage, on the streaming path as on the other one.
	*log = (*log)[:0]
	got = streamedTiers(t, app, AgentLoopConfig{
		TierOverride: LEAD,
		ChatOptions:  []ChatOption{WithRouteKey("test.tierpin.worker")},
	}, log)
	if len(got) != 1 || got[0] != "lead" {
		t.Fatalf("a LEAD pin must reach the streamed call; got %v", got)
	}

	// And in the other direction — a pin that de-escalates is a pin too.
	*log = (*log)[:0]
	got = streamedTiers(t, app, AgentLoopConfig{
		TierOverride: WORKER,
		ChatOptions:  []ChatOption{WithRouteKey("test.tierpin.lead")},
	}, log)
	if len(got) != 1 || got[0] != "worker" {
		t.Fatalf("a WORKER pin must hold a lead stage down; got %v", got)
	}
}

// TestStreamingPinDoesNotOutrankPrivacy pins the floor: the pin is a routing
// preference, and the privacy pin is not one.
func TestStreamingPinDoesNotOutrankPrivacy(t *testing.T) {
	app, log := withTierStubs(t, "test.tierpin.private", noToolCalls)
	app.NoLead = true // the deployment says: no lead, whatever anyone prefers
	got := streamedTiers(t, app, AgentLoopConfig{
		TierOverride: LEAD,
		ChatOptions:  []ChatOption{WithRouteKey("test.tierpin.private")},
	}, log)
	if len(got) != 1 || got[0] != "worker" {
		t.Fatalf("a pin must not escalate past the privacy pin; got %v", got)
	}
}
