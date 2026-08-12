package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// allowsWith composes the REAL rule (autonomousToolAllowed) the way the gate
// composes it, with the credential lookup stubbed by name. Tests that went
// through a hand-rolled equivalent are how the pre-flight drifted from the gate
// in the first place.
func allowsWith(subAgent bool, approved, alwaysConfirm map[string]bool) func(string) bool {
	return func(name string) bool {
		return autonomousToolAllowed(subAgent, approved, name, func(n string) bool { return alwaysConfirm[n] })
	}
}

// The friction this exists for: an interactive dispatch auto-confirms every
// tool, so a schedule authored from a working conversation looks fine and then
// refuses hours later when it fires with nobody there to confirm. The pre-flight
// runs the gate's own rule early so the gap is visible while it can be fixed.
func TestConfirmingToolWithoutPreauthIsFlagged(t *testing.T) {
	agent := AgentRecord{ID: "a", Owner: "u", Name: "A", AllowedTools: []string{"message_contact", "web_search"}}
	allows := allowsWith(false, nil, map[string]bool{"message_contact": true})
	got := preflightToolFindings(agent, allows)
	if len(got) != 1 {
		t.Fatalf("expected exactly the confirming tool to be flagged, got %+v", got)
	}
	if got[0].Name != "message_contact" || got[0].Gate != PreflightGateTool {
		t.Errorf("wrong finding: %+v", got[0])
	}
	if got[0].Fix == "" {
		t.Error("a finding must name the action that clears it")
	}
}

// THE regression. The gate allows any tool whose credential isn't configured to
// ask — attaching it to the agent IS the authorization — but the pre-flight kept
// warning on the old NeedsConfirm tier, so authoring a schedule announced that an
// already-enabled tool "asks for confirmation" and would be refused. It never was.
// A pre-flight that cries wolf costs more than no pre-flight, because the one real
// finding stops being distinguishable from the noise.
func TestEnabledToolWithNothingConfiguredIsNotFlagged(t *testing.T) {
	agent := AgentRecord{ID: "a", Owner: "u", Name: "A", AllowedTools: []string{"get_weather", "post_update"}}
	if got := preflightToolFindings(agent, allowsWith(false, nil, nil)); len(got) != 0 {
		t.Errorf("a tool the gate would run must not be flagged, got %+v", got)
	}
}

// Pre-authorized is exactly what the gate checks, so it must silence the
// warning — otherwise the report cries wolf on every correctly-configured task.
func TestPreauthorizedToolIsNotFlagged(t *testing.T) {
	agent := AgentRecord{ID: "a", Owner: "u", Name: "A", AllowedTools: []string{"message_contact"}}
	allows := allowsWith(false, map[string]bool{"message_contact": true}, map[string]bool{"message_contact": true})
	if got := preflightToolFindings(agent, allows); len(got) != 0 {
		t.Errorf("a pre-authorized tool must not be flagged, got %+v", got)
	}
}

// A sub-agent runs under its parent's authority — autonomousGate.confirm returns
// true for everything it calls. The pre-flight has to mirror that or it reports
// refusals that will never happen.
func TestSubAgentIsNotFlagged(t *testing.T) {
	sub := AgentRecord{ID: "s", Owner: "u", Name: "S", OwnedBy: "parent", AllowedTools: []string{"message_contact"}}
	allows := allowsWith(true, nil, map[string]bool{"message_contact": true})
	if got := preflightToolFindings(sub, allows); len(got) != 0 {
		t.Errorf("a sub-agent runs under its parent's authority and must not be flagged, got %+v", got)
	}
}

// The no-tools sentinel is an allow-list marker, not a tool. Reporting it would
// name something the user cannot go and pre-authorize.
func TestSentinelIsNotFlagged(t *testing.T) {
	agent := AgentRecord{ID: "a", Owner: "u", Name: "A", AllowedTools: []string{noToolsSentinel}}
	allows := allowsWith(false, nil, map[string]bool{noToolsSentinel: true})
	if got := preflightToolFindings(agent, allows); len(got) != 0 {
		t.Errorf("the sentinel is not a tool, got %+v", got)
	}
}

// Approval inherits down the OwnedBy chain, so a grant on an ancestor has to
// clear the warning for the descendant too — the pre-flight reads the same
// inherited set the gate snapshots.
func TestInheritedApprovalClearsTheWarning(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	mk := func(id, ownedBy string, auto []string) {
		if _, err := saveAgent(db, AgentRecord{ID: id, Owner: "u", Name: id, OrchestratorPrompt: "p", OwnedBy: ownedBy, AutoApproveTools: auto}); err != nil {
			t.Fatalf("saveAgent %s: %v", id, err)
		}
	}
	mk("parent", "", []string{"message_contact"})
	mk("child", "parent", nil)

	// Read through the same helper the gate uses, then apply the rule as if the
	// child were top-level (isolating inheritance from the sub-agent bypass).
	inherited := autonomousApprovedSet(db, "child")
	topLevel := AgentRecord{ID: "child", Owner: "u", Name: "child", AllowedTools: []string{"message_contact"}}
	allows := allowsWith(false, inherited, map[string]bool{"message_contact": true})
	if got := preflightToolFindings(topLevel, allows); len(got) != 0 {
		t.Errorf("an ancestor's grant must clear the warning, got %+v", got)
	}
}

// Findings are ordered so the same configuration always reports the same way.
func TestFindingsAreStablyOrdered(t *testing.T) {
	agent := AgentRecord{ID: "a", Owner: "u", Name: "A", AllowedTools: []string{"zeta", "alpha"}}
	allows := allowsWith(false, nil, map[string]bool{"zeta": true, "alpha": true})
	got := preflightToolFindings(agent, allows)
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Errorf("findings should be name-sorted, got %+v", got)
	}
}

// The anti-drift test: the pre-flight's verdict and the gate's must be the same
// verdict, because they are now the same call. Wire a REAL gate — the shape the
// stub above stands in for — and check the two agree tool by tool. If someone
// re-implements either rule, this fails before a user is told the wrong thing.
func TestPreflightAgreesWithTheGate(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{}
	app.DB = db
	rec := AgentRecord{
		ID: "a", Owner: "u", Name: "A", OrchestratorPrompt: "p",
		AllowedTools:     []string{"get_weather", "run_report", "spend"},
		AutoApproveTools: []string{"spend"},
	}
	if _, err := saveAgent(db, rec); err != nil {
		t.Fatal(err)
	}
	gate := app.newAutonomousGate("u", "a", nil)
	findings := preflightToolFindings(rec, gate.allows)

	flagged := map[string]bool{}
	for _, f := range findings {
		flagged[f.Name] = true
	}
	for _, name := range rec.AllowedTools {
		// A fresh gate per tool: confirm() queues on a refusal, and we want each
		// tool's verdict, not the first one's aftermath.
		g := app.newAutonomousGate("u", "a", nil)
		ranIt := g.confirm(name, "{}")
		if ranIt == flagged[name] {
			t.Errorf("%s: gate allowed=%v but pre-flight flagged=%v — the two rules have drifted", name, ranIt, flagged[name])
		}
	}
	// None of these has a credential configured to ask, so nothing is held back.
	if len(findings) != 0 {
		t.Errorf("no tool here dispatches through a confirming credential, got %+v", findings)
	}
}

// A clean pre-flight must render as empty so callers can treat "" as "nothing to
// say" and not append a blank warning block to every tool result.
func TestCleanPreflightRendersEmpty(t *testing.T) {
	if PreflightSummary(nil) != "" {
		t.Error("no findings must render as the empty string")
	}
	summary := PreflightSummary([]PreflightFinding{{
		Gate: PreflightGateTool, Name: "message_contact",
		Detail: "would be refused", Fix: "pre-authorize it",
	}})
	for _, want := range []string{"message_contact", "would be refused", "pre-authorize it"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary should carry %q, got:\n%s", want, summary)
		}
	}
	// It warns; it must not read as a failure — the task really was created.
	if !strings.Contains(summary, "still created") {
		t.Error("the summary must say the schedule was created, so this reads as advisory")
	}
}
