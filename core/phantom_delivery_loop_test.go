package core

// The loop's half of the phantom-delivery fix: a reply promising a file that
// does not exist gets its claim removed and one correction round, the same
// remedy a fake tool call gets. Both are an action claimed but never taken.

import (
	"strings"
	"testing"
)

func TestDeliveryMarkersAreStrippedFromAClaim(t *testing.T) {
	got := StripDeliveryMarkers("Here you go! [ATTACH: find-dkfindcraig.jpg]")
	if strings.Contains(got, "ATTACH") {
		t.Errorf("the claim must not survive: %q", got)
	}
	if !strings.Contains(got, "Here you go!") {
		t.Errorf("the sentence around it should remain: %q", got)
	}
	// A reply that was ONLY the marker strips to nothing, which is the state the
	// correction round exists to replace.
	if got := StripDeliveryMarkers("[ATTACH: nope.png]"); got != "" {
		t.Errorf("a bare marker should strip to empty, got %q", got)
	}
	// Several markers, all removed.
	if got := StripDeliveryMarkers("[ATTACH: a.png] and [ATTACH: b.png]"); strings.Contains(got, "ATTACH") {
		t.Errorf("every marker must go: %q", got)
	}
}

func TestTheLoopAsksTheHostAndDefaultsToSilence(t *testing.T) {
	// No hook = no check, which is exactly how every host behaved before this
	// existed — a nil hook must never be a panic or a false positive.
	if refs := phantomDeliveryRefs(AgentLoopConfig{}, "[ATTACH: whatever.png]"); refs != nil {
		t.Errorf("a host with no hook reports nothing, got %v", refs)
	}
	cfg := AgentLoopConfig{PhantomDeliveryRefs: func(string) []string { return []string{"ghost.png"} }}
	if refs := phantomDeliveryRefs(cfg, "[ATTACH: ghost.png]"); len(refs) != 1 {
		t.Errorf("the host's answer should come back, got %v", refs)
	}
	// Empty content is never a claim.
	if refs := phantomDeliveryRefs(cfg, "   "); refs != nil {
		t.Errorf("empty content asks nothing, got %v", refs)
	}
}
