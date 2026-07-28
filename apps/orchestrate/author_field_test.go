package orchestrate

import (
	"strings"
	"testing"
)

// The Builder seed gets authoring by IDENTITY — agentCanAuthor OR's the seed
// ID ahead of the flag, so the stored value is never consulted. Rendering a
// live toggle there displayed "off" on an agent holding the full authoring
// catalog, which is how a debugging session concluded authoring was disabled
// when it was not.
func TestAuthorFieldIsNotAToggleForBuilder(t *testing.T) {
	f := authorCapabilityField("seed-builder")
	if f.Type == "toggle" || f.Field == "author" {
		t.Fatalf("Builder still renders a live author toggle: %+v", f)
	}
	if !strings.Contains(strings.ToLower(f.Label), "always on") {
		t.Errorf("label should say the capability is always on, got %q", f.Label)
	}
	if !strings.Contains(f.Help, "owner-only") {
		t.Error("help should mention the owner-only runtime gate — the one condition that DOES withhold authoring")
	}
}

// Every other agent keeps a real, bound toggle: there the flag is the only
// thing agentCanAuthor consults.
func TestAuthorFieldIsAToggleForOtherAgents(t *testing.T) {
	for _, id := range []string{"", "some-agent", "seed-kb"} {
		f := authorCapabilityField(id)
		if f.Field != "author" || f.Type != "toggle" {
			t.Errorf("agent %q should get a bound toggle, got %+v", id, f)
		}
	}
}

// The rendering must track the predicate. If agentCanAuthor ever stops
// special-casing the seed, the toggle becomes meaningful again and this
// test should be revisited alongside it.
func TestBuilderAuthoringIsIdentityNotFlag(t *testing.T) {
	if !agentCanAuthor(AgentRecord{ID: "seed-builder"}) {
		t.Fatal("Builder lost identity-based authoring")
	}
	if !agentCanAuthor(AgentRecord{ID: "seed-builder", Author: false}) {
		t.Error("the Author flag must not be able to disable Builder's authoring")
	}
	if agentCanAuthor(AgentRecord{ID: "other", Author: false}) {
		t.Error("a non-Builder agent without the flag should not author")
	}
	if !agentCanAuthor(AgentRecord{ID: "other", Author: true}) {
		t.Error("the flag should grant authoring to a non-Builder agent")
	}
}
