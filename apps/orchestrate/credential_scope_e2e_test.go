package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestSetCredentialScopeEndToEnd exercises the ACTUAL setCredentialScope +
// credentialScopeState functions (not just the persistence primitives),
// including the Secure() existence gate, to prove the full credential-deny
// path the pill drives works: register a credential, deny it on one agent,
// then read the scope state back and confirm that agent shows On=false.
func TestSetCredentialScopeEndToEnd(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}

	// Bind Secure() to a real store and register the credential so the
	// existence gate in credentialScopeState/setCredentialScope passes.
	secStore := &DBase{Store: kvlite.MemStore()}
	prevAuthDB := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prevAuthDB }()

	const cred = "moltbook_api"
	if err := Secure().Save(SecureCredential{
		Name:    cred,
		Type:    SecureCredBearer,
		BaseURL: "https://moltbook.test",
	}, "tok"); err != nil {
		t.Fatalf("register credential: %v (Secure bound? %v)", err, Secure() != nil)
	}
	if exists, _, _ := Secure().CredentialStatus(cred); !exists {
		t.Fatal("credential not registered in Secure()")
	}

	const owner = "alice"
	udb := agentUserDB(root, owner)
	// A TOP-LEVEL agent (not a sub-agent): credentialScopeState excludes
	// sub-agents (OwnedBy set), so only a top-level agent is a scope target.
	sub, err := saveAgent(udb, AgentRecord{Name: "Sub", OrchestratorPrompt: "s", Owner: owner})
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}

	// Baseline state: agent should read On=true (allowed by default).
	st, ok := credentialScopeState(root, owner, cred)
	if !ok {
		t.Fatal("credentialScopeState returned not-found for a registered credential")
	}
	if !agentOn(st, sub.ID) {
		t.Fatal("baseline: agent should start allowed (On=true)")
	}

	// Deny via the real setter.
	if err := setCredentialScope(root, owner, cred, sub.ID, false); err != nil {
		t.Fatalf("setCredentialScope deny: %v", err)
	}

	// Re-read: the agent must now read On=false.
	st2, ok := credentialScopeState(root, owner, cred)
	if !ok {
		t.Fatal("state not-found after deny")
	}
	if agentOn(st2, sub.ID) {
		t.Errorf("deny did not stick — agent still reads On=true after setCredentialScope(...,false)")
	}

	// And re-allow toggles back to On=true.
	if err := setCredentialScope(root, owner, cred, sub.ID, true); err != nil {
		t.Fatalf("setCredentialScope allow: %v", err)
	}
	st3, _ := credentialScopeState(root, owner, cred)
	if !agentOn(st3, sub.ID) {
		t.Errorf("re-allow did not stick — agent still reads On=false")
	}
}

// TestCredentialDenyBuilder pins the Builder-specific bug: loadAgent's
// seed-builder special-case rebases everything except Rules onto the in-code
// seed, which used to DROP DisabledCredentials written to Builder's shadow —
// so a credential deny on Builder saved but never read back, and the pill
// snapped on ("can't unselect Builder via api scope"). The scope deny-lists
// must survive on Builder like Rules does.
func TestCredentialDenyBuilder(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	secStore := &DBase{Store: kvlite.MemStore()}
	prevAuthDB := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prevAuthDB }()

	const cred = "moltbook_api"
	if err := Secure().Save(SecureCredential{Name: cred, Type: SecureCredBearer, BaseURL: "https://moltbook.test"}, "tok"); err != nil {
		t.Fatalf("register credential: %v", err)
	}

	const owner = "alice"
	udb := agentUserDB(root, owner)

	if err := setCredentialScope(root, owner, cred, "seed-builder", false); err != nil {
		t.Fatalf("deny on builder: %v", err)
	}
	st, ok := credentialScopeState(root, owner, cred)
	if !ok {
		t.Fatal("state not-found after builder deny")
	}
	if agentOn(st, "seed-builder") {
		t.Error("Builder credential deny did not stick — seed-builder still reads On=true (loadAgent dropped DisabledCredentials)")
	}
	// The shadow must actually carry the deny (tier-2 per-agent opt-out).
	if a, _ := loadAgent(udb, "seed-builder"); !containsString(a.DisabledCredentials, cred) {
		t.Errorf("loadAgent(seed-builder) dropped DisabledCredentials — got %v", a.DisabledCredentials)
	}
}

func agentOn(st ToolScopeState, id string) bool {
	for _, a := range st.Agents {
		if a.ID == id {
			return a.On
		}
	}
	return false
}
