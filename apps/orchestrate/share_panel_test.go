package orchestrate

// The share panel on agent edit: who gets it, what they may add, and the one
// decision per dependency that is not a copy.
//
// Everything an agent reaches travels with it and is scoped to it, so its row
// states a fact. A credential is whose identity the call goes out as, so its
// row is a control — and, unlike the guided flow that used to be its only
// home, one that can be moved back.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A control that can only run forward is a lie the second time you look at it.
// Moving a credential off a lend has to take the lend back, and take back only
// what this agent's share granted.
func TestMovingACredentialOffALendTakesItBack(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)

	if err := Secure().Save(SecureCredential{Name: "share_panel_wiki", Type: SecureCredNone,
		Owner: "alice", AllowedURLPattern: "https://wiki.example/**"}, ""); err != nil {
		t.Skipf("no secure store here: %v", err)
	}
	t.Cleanup(func() { Secure().SetCredentialShares("alice", "share_panel_wiki", nil, nil) })

	rec := AgentRecord{ID: "a1", Owner: "alice", Name: "Runbooks", OrchestratorPrompt: "p",
		AllowedUsers: []string{"bob"}}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	decide := func(lend string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"name": "share_panel_wiki", "lend": lend})
		r := httptest.NewRequest(http.MethodPost, "/api/agents/a1/reach/credential", strings.NewReader(string(body)))
		w := httptest.NewRecorder()
		app.handleAgentCredentialDecision(w, asUser(r, "alice"), "alice", "a1")
		if w.Code != http.StatusOK {
			t.Fatalf("lend=%s: %d %s", lend, w.Code, w.Body.String())
		}
	}

	decide(credWrite)
	if !lentToIn(t, "share_panel_wiki", "bob", "a1") {
		t.Fatal("the lend was not made")
	}
	if got := lendModeFor("alice", "a1", "share_panel_wiki", false); got != credWrite {
		t.Errorf("the control reads back %q, not %q", got, credWrite)
	}

	// Back to "they bring their own": the lend goes with it.
	decide(credOwn)
	if lentToIn(t, "share_panel_wiki", "bob", "a1") {
		t.Error("moving the control back left the key lent")
	}
	if got := lendModeFor("alice", "a1", "share_panel_wiki", false); got != credOwn {
		t.Errorf("the control still reads %q", got)
	}
}

// A lend the owner made by hand is not this agent's doing, so the control must
// neither claim it nor revoke it. This is the whole reason the ledger exists.
func TestAHandMadeLendIsNotTheAgentsToTakeBack(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)

	if err := Secure().Save(SecureCredential{Name: "share_panel_hand", Type: SecureCredNone,
		Owner: "alice", AllowedURLPattern: "https://wiki.example/**"}, ""); err != nil {
		t.Skipf("no secure store here: %v", err)
	}
	t.Cleanup(func() { Secure().SetCredentialShares("alice", "share_panel_hand", nil, nil) })
	// Lent by hand, months ago, for something else entirely.
	if err := Secure().SetCredentialShares("alice", "share_panel_hand", []string{"bob"}, nil); err != nil {
		t.Fatalf("hand lend: %v", err)
	}
	if _, err := saveAgent(udb, AgentRecord{ID: "a2", Owner: "alice", Name: "Other",
		OrchestratorPrompt: "p", AllowedUsers: []string{"bob"}}); err != nil {
		t.Fatal(err)
	}

	// The control shows "theirs", because this agent's share lent nothing.
	if got := lendModeFor("alice", "a2", "share_panel_hand", false); got != credOwn {
		t.Errorf("the control claimed a hand-made lend as its own: %q", got)
	}
	// And moving it leaves that grant standing.
	withdrawOneCredentialLend("alice", "a2", "share_panel_hand")
	if !lentToIn(t, "share_panel_hand", "bob", "a2") {
		t.Error("a lend made by hand was revoked by an agent that never made it")
	}
}

// Turning the agent's use of a key off, then choosing an answer that needs it,
// has to re-enable it — or the pill says "lend mine" over a key the agent
// cannot dispatch through at all.
func TestLeavingOffReEnablesTheKey(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)

	if err := Secure().Save(SecureCredential{Name: "share_panel_off", Type: SecureCredNone,
		Owner: "alice", AllowedURLPattern: "https://wiki.example/**"}, ""); err != nil {
		t.Skipf("no secure store here: %v", err)
	}
	t.Cleanup(func() { Secure().SetCredentialShares("alice", "share_panel_off", nil, nil) })
	if _, err := saveAgent(udb, AgentRecord{ID: "a3", Owner: "alice", Name: "Third",
		OrchestratorPrompt: "p", AllowedUsers: []string{"bob"}}); err != nil {
		t.Fatal(err)
	}
	decide := func(lend string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"name": "share_panel_off", "lend": lend})
		r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(string(body)))
		w := httptest.NewRecorder()
		app.handleAgentCredentialDecision(w, asUser(r, "alice"), "alice", "a3")
		if w.Code != http.StatusOK {
			t.Fatalf("lend=%s: %d %s", lend, w.Code, w.Body.String())
		}
	}
	decide(shareSkip)
	if a, _ := loadAgent(udb, "a3"); !namedIn(a.DisabledCredentials, "share_panel_off") {
		t.Fatal("Off did not turn the key off for the agent")
	}
	decide(credRead)
	a, _ := loadAgent(udb, "a3")
	if namedIn(a.DisabledCredentials, "share_panel_off") {
		t.Error("choosing a lend left the key turned off for the agent")
	}
}

// Only credential rows carry a control. Every other row states a fact, and a
// pill on one would be four ways to say the same thing.
func TestOnlyCredentialRowsOfferADecision(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	rec := AgentRecord{ID: "a4", Owner: "alice", Name: "Fourth", OrchestratorPrompt: "p",
		AllowedUsers: []string{"bob"},
		Tools:        []TempTool{{Name: "get_page", CommandTemplate: "curl x", Credential: "share_panel_k"}},
	}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	a, _ := loadAgent(udb, "a4")
	var creds, others int
	for _, it := range agentReachOf(udb, "alice", a).Items {
		if it.kind == "credential" {
			creds++
			if !it.Decide || it.Lend == "" {
				t.Errorf("credential row has no decision on it: %+v", it)
			}
			continue
		}
		others++
		if it.Decide || it.Lend != "" {
			t.Errorf("a row that states a fact was given a control: %+v", it)
		}
	}
	if creds == 0 || others == 0 {
		t.Fatalf("expected both kinds of row; creds=%d others=%d", creds, others)
	}
}

// lentToIn asks the grant, not the resolution. Resolution answers a wider
// question (the recipient may own a key of the same name), so a test that
// checked it would pass for the wrong reason.
func lentToIn(t *testing.T, cred, user, agentID string) bool {
	t.Helper()
	for _, c := range Secure().SharedWithUserIn(user, agentID) {
		if c.Name == cred {
			return true
		}
	}
	return false
}
