package orchestrate

import (
	"strings"
	"testing"
)

// Builder's record allowlist genuinely lacks tool_def: the authoring toolset
// is appended AFTER allowlist filtering. introspect reported only the
// allowlist, so Builder asked what it could do, was handed a list without
// tool_def, and repeated it back to the user as its capabilities — three
// sessions ending in "I will report this to my developers."
//
// The fix must DESCRIBE THE RULE rather than assert the tools are present:
// the runtime gate (owner-only) can still withhold them, and an
// introspection surface that over-reports is as bad as one that under-reports.
func TestIntrospectExplainsAllowlistIsNotTheCatalog(t *testing.T) {
	builder := AgentRecord{
		ID:           "seed-builder",
		AllowedTools: []string{"ask_user", "plan_set", "web_search", "workspace"},
	}
	joined := strings.Join(effectiveExtraToolsets(builder), "\n")

	if !strings.Contains(joined, "tool_def") {
		t.Fatalf("tool_def never mentioned:\n%s", joined)
	}
	if !strings.Contains(joined, "always on for Builder") {
		t.Errorf("Builder's authoring should read as identity, not a grant:\n%s", joined)
	}
	// The load-bearing sentence: absence from the allowlist proves nothing.
	if !strings.Contains(joined, "says nothing about whether you can call them") {
		t.Errorf("must state that allowlist absence is not evidence of absence:\n%s", joined)
	}
	// And it must not promise availability it cannot guarantee.
	if !strings.Contains(joined, "withheld") {
		t.Errorf("must disclose the owner-only runtime gate:\n%s", joined)
	}
}

func TestIntrospectReportsGrantedAuthoring(t *testing.T) {
	joined := strings.Join(effectiveExtraToolsets(AgentRecord{ID: "x", Author: true}), "\n")
	if !strings.Contains(joined, "tool_def") {
		t.Error("an Author-flagged agent should see its authoring toolset")
	}
	if !strings.Contains(joined, "capability granted") {
		t.Error("a flagged agent's authoring should read as a grant, not identity")
	}
}

// The disclosure must be accurate in both directions — an agent without
// authoring must never be told it has tool_def.
func TestIntrospectDoesNotInventCapabilities(t *testing.T) {
	joined := strings.Join(effectiveExtraToolsets(AgentRecord{ID: "plain"}), "\n")
	if strings.Contains(joined, "tool_def") {
		t.Errorf("a non-authoring agent was told it has tool_def:\n%s", joined)
	}
	if strings.Contains(joined, "Conductor toolset") {
		t.Errorf("a non-fleet agent was told it has conductor tools:\n%s", joined)
	}
}

func TestIntrospectReportsConductorToolset(t *testing.T) {
	joined := strings.Join(effectiveExtraToolsets(AgentRecord{ID: "x", Fleet: true}), "\n")
	if !strings.Contains(joined, "Conductor toolset") {
		t.Error("a Fleet agent should see its conductor toolset")
	}
}
