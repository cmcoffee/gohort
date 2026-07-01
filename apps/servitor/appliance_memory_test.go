package servitor

import (
	"strings"
	"testing"
)

// TestApplianceMemScope locks the synthetic scope-user format the orchestrate
// seeding APIs + the …ForScope handlers key off. Drifting this silently
// detaches the modal from whatever the lead writes.
func TestApplianceMemScope(t *testing.T) {
	if got := applianceMemScope("abc-123"); got != "app:servitor:abc-123" {
		t.Fatalf("scope = %q, want app:servitor:abc-123", got)
	}
}

// TestApplianceMemoryModalWiring guards the two seams that must agree across
// the chat-page toolbar button, the shared modal builder, and the JS base
// resolver. If any drifts, the Memory button opens a modal that fetches the
// wrong place (or nothing).
func TestApplianceMemoryModalWiring(t *testing.T) {
	s := applianceMemoryModalScript
	// The toolbar action in chat_page.go targets this exact name.
	if !strings.Contains(s, "'servitor_appliance_memory'") {
		t.Error("modal script missing the servitor_appliance_memory client action")
	}
	// The base is resolved at open via servitorMemBase() (web_assets.go).
	if !strings.Contains(s, "var MEMBASE = (servitorMemBase());") {
		t.Error("modal script missing the servitorMemBase() base resolver")
	}
	// Endpoints must build off MEMBASE, not a baked literal — spot-check the
	// ones handleApplianceMemory routes.
	for _, ep := range []string{"MEMBASE + 'facts'", "MEMBASE + 'graph'", "MEMBASE + 'inferred'", "MEMBASE + 'agent'"} {
		if !strings.Contains(s, ep) {
			t.Errorf("modal script missing endpoint %q", ep)
		}
	}
}
