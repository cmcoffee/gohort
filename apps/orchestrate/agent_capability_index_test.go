package orchestrate

import (
	"strings"
	"testing"
	"time"
)

// A fleet where the work asked for is done by a specialist under one agent:
// OSINT's own description says nothing about addresses, its sub-agent does.
func capabilityFleet(t *testing.T) *chatTurn {
	t.Helper()
	capabilityVecs.Lock()
	capabilityVecs.vecs, capabilityVecs.filling = map[string][]float32{}, map[string]bool{}
	capabilityVecs.Unlock()
	fakeProposalEmbedder(t, map[string][]float32{
		"OSINT: Investigates people and companies.":     {0, 1, 0, 0},
		"Hardware: Fixes printers and laptops.":         {0, 0, 1, 0},
		"Addr: Looks up who lives at a street address.": {1, 0, 0, 0},
		"address lookup":              {0.9, 0.2, 0.3, 0},
		"who lives at 12 Elm Street?": {0.95, 0.1, 0.1, 0},
		"tell me a joke about cats":   {0, 0, 0, -1}, // unlike every text the fake does not know, too
	})
	const p = "You do your job."
	turn := reachTurn(t, AgentRecord{ID: "a1", Name: "Lead", OrchestratorPrompt: p},
		AgentRecord{ID: "a2", Name: "OSINT", Description: "Investigates people and companies.", OrchestratorPrompt: p},
		AgentRecord{ID: "a3", Name: "Hardware", Description: "Fixes printers and laptops.", OrchestratorPrompt: p},
		AgentRecord{ID: "a4", Name: "Addr", Description: "Looks up who lives at a street address.", OwnedBy: "a2", Hidden: true, OrchestratorPrompt: p})
	// reachTurn drops a save error; a fleet that silently lost its agents
	// would pass the negative checks below for the wrong reason.
	if n := len(listAgents(turn.udb, turn.user)); n < 4 {
		t.Fatalf("the fleet was not saved: %d agents", n)
	}
	return turn
}

// find names the agent whose specialist does the work, ahead of the agent
// whose own description merely sits nearby, and never the specialist itself.
func TestFindCreditsASpecialistsWorkToItsAgent(t *testing.T) {
	turn := capabilityFleet(t)
	out, err := turn.agentsFindAction(t.Context(), map[string]any{"query": "address lookup"})
	if err != nil {
		t.Fatal(err)
	}
	osint := strings.Index(out, "- **OSINT**, through its specialist Addr: Looks up who lives at a street address.")
	hardware := strings.Index(out, "- **Hardware**")
	if osint < 0 || (hardware >= 0 && hardware < osint) {
		t.Fatalf("OSINT, through Addr, should come first:\n%s", out)
	}
	if strings.Contains(out, "- **Addr**") {
		t.Errorf("the specialist was offered as an agent to send work to:\n%s", out)
	}
	if strings.Count(out, "**OSINT**") != 1 {
		t.Errorf("an agent is listed once, by its best line:\n%s", out)
	}
	if _, err := turn.agentsFindAction(t.Context(), map[string]any{}); err == nil {
		t.Error("find with no query should be refused")
	}
}

// Beside the person's message: nothing on the turn that first meets a
// description (it is embedded off the clock), then the matching agent from the
// next turn on; a message that matches nothing gets no pointer, and an agent a
// trigger already named is not named twice.
func TestTheMessageIsMatchedAgainstWhatAgentsDo(t *testing.T) {
	turn := capabilityFleet(t)
	msg := "who lives at 12 Elm Street?"
	if h := turn.renderAgentCapabilityHints(msg, nil); h != "" {
		t.Fatalf("the first turn waited on embeds it did not have: %q", h)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		capabilityVecs.Lock()
		n, busy := len(capabilityVecs.vecs), len(capabilityVecs.filling)
		capabilityVecs.Unlock()
		if n >= 3 && busy == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the background fill never finished: %d cached, %d filling", n, busy)
		}
		time.Sleep(10 * time.Millisecond)
	}
	h := turn.renderAgentCapabilityHints(msg, nil)
	if !strings.Contains(h, "**OSINT**, through its specialist Addr") || strings.Contains(h, "**Hardware**") {
		t.Fatalf("the next turn should point at OSINT alone: %q", h)
	}
	if !strings.Contains(h, "cannot see it, so never mention it") {
		t.Errorf("the pointer must say the person cannot see it: %q", h)
	}
	if h := turn.renderAgentCapabilityHints("tell me a joke about cats", nil); h != "" {
		t.Errorf("a message that matches no agent's work got a pointer: %q", h)
	}
	if h := turn.renderAgentCapabilityHints(msg, []string{"OSINT"}); h != "" {
		t.Errorf("an agent a trigger named was pointed out again: %q", h)
	}
}
