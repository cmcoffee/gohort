package orchestrate

import (
	"strings"
	"testing"
)

// The Builder seed gets authoring by IDENTITY, so its line is a read-only
// note rather than a toggle. Rendering a live toggle there displayed "off" on an
// agent holding the full authoring catalog, which is how a debugging session
// concluded authoring was disabled when it was not.
func TestAuthorFieldIsNotAToggleForBuilder(t *testing.T) {
	fields := authorCapabilityFields("seed-builder")
	if len(fields) != 1 {
		t.Fatalf("Builder should get one read-only line, got %+v", fields)
	}
	f := fields[0]
	if f.Type == "toggle" || f.Field == "author" {
		t.Fatalf("Builder still renders a live author toggle: %+v", f)
	}
	if !strings.Contains(strings.ToLower(f.Label), "always on") {
		t.Errorf("label should say the capability is always on, got %q", f.Label)
	}
	if !strings.Contains(f.Help+f.Detail, "owner-only") {
		t.Error("the field should mention the owner-only runtime gate, the one condition that DOES withhold authoring")
	}
}

// The Author flag is retired, so no other agent gets a toggle for it: a
// control that changes nothing is worse than none.
func TestNoAuthorToggleForOtherAgents(t *testing.T) {
	for _, id := range []string{"", "some-agent", "seed-kb"} {
		if f := authorCapabilityFields(id); len(f) != 0 {
			t.Errorf("agent %q should get no authoring control, got %+v", id, f)
		}
	}
}

// Authoring is Builder's alone: the retired flag grants nothing.
func TestOnlyBuilderAuthors(t *testing.T) {
	if !agentCanAuthor(AgentRecord{ID: "seed-builder"}) {
		t.Fatal("Builder lost identity-based authoring")
	}
	if agentCanAuthor(AgentRecord{ID: "other"}) {
		t.Error("a non-Builder agent should not author")
	}
	if agentCanAuthor(AgentRecord{ID: "other", Author: true}) {
		t.Error("the retired Author flag must grant nothing")
	}
}

// The migration keeps what an authoring agent could get done: it may hand the
// work to Builder, and Consult the Lead stays on unless the owner chose.
func TestRetiringTheAuthorFlag(t *testing.T) {
	got := retireAuthorFlag(AgentRecord{ID: "a1", Author: true})
	if got.Author || !got.AllowBuilderDispatch || got.ConsultLead != settingOn {
		t.Errorf("an authoring agent should move to Builder dispatch with consult kept: %+v", got)
	}
	chose := retireAuthorFlag(AgentRecord{ID: "a2", Author: true, ConsultLead: settingOff})
	if chose.ConsultLead != settingOff {
		t.Error("an owner's own consult choice must survive the migration")
	}
	plain := AgentRecord{ID: "a3"}
	if retireAuthorFlag(plain).AllowBuilderDispatch {
		t.Error("an agent that never authored gains nothing")
	}
}

// With the flag retired, the prompt has to tell an agent how it gets something
// built: an agent that may hand work to Builder is told to, and only an agent
// that cannot is told it cannot. Keyed to the dispatch gate, so a grant under
// "Allow none" still counts and a conductor under it does not.
func TestThePromptSaysHowAnAgentGetsThingsBuilt(t *testing.T) {
	has := func(a AgentRecord, marker string) bool {
		return strings.Contains(frameworkPromptBlocks("", a, true), marker)
	}
	granted := AgentRecord{ID: "g", AllowBuilderDispatch: true}
	if !has(granted, builderRoutingMarker) || has(granted, cannotAuthorMarker) {
		t.Error("an agent granted Builder dispatch should be told to hand building to Builder")
	}
	plain := AgentRecord{ID: "p"}
	if has(plain, builderRoutingMarker) || !has(plain, cannotAuthorMarker) {
		t.Error("an agent that cannot reach Builder should be told it cannot build, with request_build as its path")
	}
	grounded := AgentRecord{ID: "c", Fleet: true, DispatchMode: dispatchNone}
	if has(grounded, builderRoutingMarker) {
		t.Error("a conductor under Allow none cannot reach Builder, so it must not be told to")
	}
	if has(AgentRecord{ID: "seed-builder"}, builderRoutingMarker) || has(AgentRecord{ID: "seed-builder"}, cannotAuthorMarker) {
		t.Error("Builder is told neither to route to itself nor that it cannot build")
	}
}
