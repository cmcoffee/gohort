package orchestrate

// The agent-listing seam. What these hold is the degradation and the key: a
// picker built in a package that cannot see an AgentRecord still renders, and
// what it stores is not what it shows.

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// TestAgentNameOptionsIsSafeBeforeAnythingRegisters pins the degradation the
// seam exists to allow. core cannot reach an agent, so every caller of this is
// a picker being built in a package that is not agent-aware; one that had to
// branch on "has anything registered yet" would just go back to a text box.
// Empty is a picker with no suggestions, which still renders.
func TestAgentNameOptionsIsSafeBeforeAnythingRegisters(t *testing.T) {
	if got := AgentNameOptions(""); got != nil {
		t.Errorf("a blank user must not be looked up: %+v", got)
	}
	RegisterAgentNamer(nil)
	if got := AgentNameOptions("alice"); got != nil {
		t.Errorf("no namer registered must list nothing, not panic: %+v", got)
	}
}

// TestTheAgentNamerOffersIDsAndReadsBackNames pins the split that made this a
// select rather than a text box: what is STORED is the id, because that
// survives a rename, and what is SHOWN is the name, because that is what the
// person recognises. A picker that conflated them would either save something
// a rename breaks or show something nobody chose.
func TestTheAgentNamerOffersIDsAndReadsBackNames(t *testing.T) {
	t.Cleanup(func() { RegisterAgentNamer(nil) })
	RegisterAgentNamer(func(user string) []ui.SelectOption {
		if user != "alice" {
			return nil
		}
		return []ui.SelectOption{{Value: "agent-7", Label: "Runbook Curator"}}
	})
	got := AgentNameOptions("alice")
	if len(got) != 1 {
		t.Fatalf("expected the one agent, got %+v", got)
	}
	if got[0].Value == got[0].Label {
		t.Error("the stored value and the shown label must be distinguishable, or the picker may as well be a text box")
	}
	if got[0].Value != "agent-7" || got[0].Label != "Runbook Curator" {
		t.Errorf("value/label came back as %q/%q", got[0].Value, got[0].Label)
	}
	if got := AgentNameOptions("bob"); got != nil {
		t.Errorf("one user's agents must not be offered to another: %+v", got)
	}
}
