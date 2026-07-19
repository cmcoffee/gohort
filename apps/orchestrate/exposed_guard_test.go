package orchestrate

import (
	"testing"

	"github.com/cmcoffee/gohort/core/appagents"
)

// A Hidden app-agent (Servitor Investigator / Guide Author style) must never be
// publicly exposable, even if a stale Exposed flag lingers on a shadow — that
// was the "why is this on my dashboard" leak. But the guard must be scoped so a
// user's own published agent and a deliberately-visible app-agent are untouched.
func TestHiddenAppAgentNotPubliclyExposable(t *testing.T) {
	appagents.RegisterAppAgent(appagents.AppAgentSpec{ID: "test-hidden-appagent-xyz", Name: "TestHidden", Hidden: true})
	if publiclyExposable(AgentRecord{ID: "test-hidden-appagent-xyz", Exposed: true}) {
		t.Fatal("Hidden app-agent must not be publicly exposable even when Exposed=true")
	}

	// A non-app-agent (a user's own agent) with Exposed=true still exposes.
	if !publiclyExposable(AgentRecord{ID: "not-an-app-agent", Exposed: true}) {
		t.Fatal("a normal Exposed agent should remain exposable")
	}

	// An app-agent an app deliberately registers non-Hidden can still expose.
	appagents.RegisterAppAgent(appagents.AppAgentSpec{ID: "test-visible-appagent-xyz", Name: "TestVisible", Hidden: false})
	if !publiclyExposable(AgentRecord{ID: "test-visible-appagent-xyz", Exposed: true}) {
		t.Fatal("a non-Hidden app-agent with Exposed=true should remain exposable")
	}
}
