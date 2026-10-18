package orchestrate

import (
	"context"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A delegated run's card says which agent asked for it. It runs on a record
// named after its target, by id, and its card read as a scheduled report
// from that id.
func TestADelegatedRunsCardNamesWhoAsked(t *testing.T) {
	app, udb := familyFixture(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a-scout", Name: "Scout", Owner: "alice", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	registerStandingRunner(app)
	RegisterStandingRunner(func(ctx context.Context, sa StandingAgent) StandingRunResult {
		return StandingRunResult{Status: RunOK, Summary: "s", Raw: "Jacket day: bring something light."}
	})
	t.Cleanup(func() { RegisterStandingRunner(nil); RegisterStandingReporter(nil) })

	RunDelegation(context.Background(), RootDB, "alice", "a-scout", "check the forecast", "a-writer")

	thread, _ := loadChatSession(udb, "a-scout", cortexSessionID("a-scout"))
	if len(thread.Messages) == 0 {
		t.Fatal("the delegated run left nothing in the agent's Cortex")
	}
	for _, m := range thread.Messages {
		if m.ReportFrom != "Writer" || m.ReportKind != cortexKindRequest {
			t.Errorf("the card reads from %q as %q, want from Writer as a request", m.ReportFrom, m.ReportKind)
		}
	}
	if thread.Title == "a-scout" {
		t.Errorf("the thread is titled with the agent's id")
	}
}
