package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The friction this exists for: an interactive dispatch auto-confirms every
// tool, so a schedule authored from a working conversation looks fine and then
// refuses hours later when it fires with nobody there to confirm. The pre-flight
// runs the gate's own rule early so the gap is visible while it can be fixed.
func TestConfirmingToolWithoutPreauthIsFlagged(t *testing.T) {
	agent := AgentRecord{ID: "a", Owner: "u", Name: "A"}
	tools := []confirmingTool{
		{Name: "message_contact", NeedsConfirm: true},
		{Name: "web_search", NeedsConfirm: false},
	}
	got := preflightToolFindings(agent, map[string]bool{}, tools)
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

// Pre-authorized is exactly what the gate checks, so it must silence the
// warning — otherwise the report cries wolf on every correctly-configured task.
func TestPreauthorizedToolIsNotFlagged(t *testing.T) {
	agent := AgentRecord{ID: "a", Owner: "u", Name: "A"}
	tools := []confirmingTool{{Name: "message_contact", NeedsConfirm: true}}
	if got := preflightToolFindings(agent, map[string]bool{"message_contact": true}, tools); len(got) != 0 {
		t.Errorf("a pre-authorized tool must not be flagged, got %+v", got)
	}
}

// A sub-agent runs under its parent's authority — autonomousGate.confirm returns
// true for everything it calls. The pre-flight has to mirror that or it reports
// refusals that will never happen.
func TestSubAgentIsNotFlagged(t *testing.T) {
	sub := AgentRecord{ID: "s", Owner: "u", Name: "S", OwnedBy: "parent"}
	tools := []confirmingTool{{Name: "message_contact", NeedsConfirm: true}}
	if got := preflightToolFindings(sub, map[string]bool{}, tools); len(got) != 0 {
		t.Errorf("a sub-agent runs under its parent's authority and must not be flagged, got %+v", got)
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
	topLevel := AgentRecord{ID: "child", Owner: "u", Name: "child"}
	tools := []confirmingTool{{Name: "message_contact", NeedsConfirm: true}}
	if got := preflightToolFindings(topLevel, inherited, tools); len(got) != 0 {
		t.Errorf("an ancestor's grant must clear the warning, got %+v", got)
	}
}

// Findings are ordered so the same configuration always reports the same way.
func TestFindingsAreStablyOrdered(t *testing.T) {
	agent := AgentRecord{ID: "a", Owner: "u", Name: "A"}
	tools := []confirmingTool{
		{Name: "zeta", NeedsConfirm: true},
		{Name: "alpha", NeedsConfirm: true},
	}
	got := preflightToolFindings(agent, map[string]bool{}, tools)
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Errorf("findings should be name-sorted, got %+v", got)
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
