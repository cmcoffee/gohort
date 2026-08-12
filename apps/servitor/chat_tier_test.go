package servitor

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Servitor has two investigator paths and they resolve their model differently.
// The map/probe branch builds an AgentLoopConfig directly and has honored the
// per-appliance tier since it shipped. Chat goes through the scoped-agent
// dispatch, which had no field to carry one — so the setting saved, read back
// correctly, and did nothing on the surface an operator actually uses to ask a
// system a question.

// TestBothInvestigatorPathsCarryTheApplianceTier — a source sweep, because the
// two paths live hundreds of lines apart and neither can see that the other
// disagrees with it.
func TestBothInvestigatorPathsCarryTheApplianceTier(t *testing.T) {
	src, err := os.ReadFile("web.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	// The map/probe path: an AgentLoopConfig with the orchestrator route stage.
	if !strings.Contains(body, `RouteKey:     "app.servitor.orchestrator",`) &&
		!strings.Contains(body, `RouteKey:        "app.servitor.orchestrator",`) {
		t.Fatal("the investigator's route stage has moved")
	}
	// The chat path: the scoped-agent overrides.
	i := strings.Index(body, "leadLoop := &orchestrate.AgentLoopOverrides{")
	if i < 0 {
		t.Fatal("the chat investigator's loop overrides have moved")
	}
	block := body[i:]
	if j := strings.Index(block, "\n\t\t}"); j > 0 {
		block = block[:j]
	}
	if !strings.Contains(block, "TierOverride: applianceTierOverride(appliance.OrchestratorTier)") {
		t.Errorf("the CHAT investigator does not carry the appliance's tier override — "+
			"the setting works when you Map a system and silently does nothing when you ask "+
			"it a question:\n%s", block)
	}
}

// TestTheOverrideIsReadFromTheApplianceNotTheAgent — the chat path's tier used
// to come from the agent record's LeadModel, which is a different question with
// a different owner. The appliance is what the operator configured.
func TestTheOverrideIsReadFromTheApplianceNotTheAgent(t *testing.T) {
	for _, tc := range []struct {
		pref string
		want LLMTier
	}{
		{"lead", LEAD},
		{"worker", WORKER},
		{"", TierUnset},
	} {
		if got := applianceTierOverride(tc.pref); got != tc.want {
			t.Errorf("appliance tier %q resolved to %v, want %v", tc.pref, got, tc.want)
		}
	}
}
