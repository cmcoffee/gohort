package servitor

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The agent editor links "Machines" to this URL. It pointed at a Manage page
// that had been removed, so the link 404'd; it must be the page the app
// actually serves.
func TestGrantorManageURLIsServed(t *testing.T) {
	var found bool
	for _, s := range AgentGrantSummaries("alice", "agent-a") {
		if s.Name != "servitor" {
			continue
		}
		found = true
		if s.ManageURL != (&Servitor{}).WebPath() {
			t.Errorf("Machines links to %q, which servitor does not serve; want %q", s.ManageURL, (&Servitor{}).WebPath())
		}
	}
	if !found {
		t.Fatal("servitor's grantor is not registered")
	}
}
