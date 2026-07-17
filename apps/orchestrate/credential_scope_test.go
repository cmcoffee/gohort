package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestCredentialDenyRoundTrip isolates the persistence path the credential
// scope pill relies on — WITHOUT the Secure() existence check — to prove that
// denying a credential on an agent sticks: save the deny, then re-read via
// listAgents (the state side) and loadAgent (the set side) and confirm the
// credential reads back as denied. If this passes, the "won't uncheck" bug is
// NOT in the AgentRecord round-trip.
func TestCredentialDenyRoundTrip(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	const owner = "alice"
	const cred = "moltbook_api"
	udb := agentUserDB(root, owner)
	if udb == nil {
		t.Fatal("agentUserDB nil")
	}

	// A plain user-created agent (non-seed) in the owner's store.
	saved, err := saveAgent(udb, AgentRecord{
		Name:               "Worker",
		OrchestratorPrompt: "do things",
		Owner:              owner,
	})
	if err != nil {
		t.Fatalf("saveAgent: %v", err)
	}
	agentID := saved.ID

	// --- deny (what setCredentialScope's setOne does for on=false) ---
	a, ok := loadAgent(udb, agentID)
	if !ok {
		t.Fatal("loadAgent after create failed")
	}
	if !containsString(a.DisabledCredentials, cred) {
		a.DisabledCredentials = append(a.DisabledCredentials, cred)
	}
	if _, err := saveAgent(udb, a); err != nil {
		t.Fatalf("saveAgent deny: %v", err)
	}

	// --- state read (what credentialScopeState does) ---
	found := false
	for _, ag := range listAgents(udb, owner) {
		if ag.ID != agentID {
			continue
		}
		found = true
		on := !containsString(ag.DisabledCredentials, cred)
		if on {
			t.Errorf("listAgents: credential reads back as ALLOWED after deny — DisabledCredentials=%v", ag.DisabledCredentials)
		}
	}
	if !found {
		t.Fatal("agent missing from listAgents")
	}

	// --- also via loadAgent (the set side re-reads through this) ---
	a2, _ := loadAgent(udb, agentID)
	if !containsString(a2.DisabledCredentials, cred) {
		t.Errorf("loadAgent: deny dropped on round-trip — DisabledCredentials=%v", a2.DisabledCredentials)
	}
}

// TestCredentialDenyRoundTripSubAgent repeats the round-trip for a SUB-agent
// (OwnedBy set) — the exact case the user reported ("deselect sub-agents from
// api credentials"). enforceSubAgentPosture rewrites several fields on every
// read; this proves it does not clobber DisabledCredentials.
func TestCredentialDenyRoundTripSubAgent(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	const owner = "alice"
	const cred = "moltbook_api"
	udb := agentUserDB(root, owner)

	parent, err := saveAgent(udb, AgentRecord{Name: "Parent", OrchestratorPrompt: "p", Owner: owner})
	if err != nil {
		t.Fatalf("save parent: %v", err)
	}
	sub, err := saveAgent(udb, AgentRecord{
		Name:               "Sub",
		OrchestratorPrompt: "s",
		Owner:              owner,
		OwnedBy:            parent.ID,
	})
	if err != nil {
		t.Fatalf("save sub: %v", err)
	}

	a, ok := loadAgent(udb, sub.ID)
	if !ok {
		t.Fatal("loadAgent sub failed")
	}
	a.DisabledCredentials = append(a.DisabledCredentials, cred)
	if _, err := saveAgent(udb, a); err != nil {
		t.Fatalf("save deny: %v", err)
	}

	for _, ag := range listAgents(udb, owner) {
		if ag.ID == sub.ID && !containsString(ag.DisabledCredentials, cred) {
			t.Errorf("sub-agent deny dropped — DisabledCredentials=%v", ag.DisabledCredentials)
		}
	}
}
