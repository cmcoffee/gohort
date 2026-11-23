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
		routed(app)(w, asUser(r, "alice"))
		if w.Code != http.StatusOK {
			t.Fatalf("lend=%s: %d %s", lend, w.Code, w.Body.String())
		}
	}

	decide(credWrite)
	if !lentToIn(t, "share_panel_wiki", "bob", "a1") {
		t.Fatal("the lend was not made")
	}
	if got := lendModeFor("alice", "a1", "share_panel_wiki", false, false, true); got != credWrite {
		t.Errorf("the control reads back %q, not %q", got, credWrite)
	}

	// Back to "they bring their own": the lend goes with it.
	decide(credOwn)
	if lentToIn(t, "share_panel_wiki", "bob", "a1") {
		t.Error("moving the control back left the key lent")
	}
	if got := lendModeFor("alice", "a1", "share_panel_wiki", false, false, true); got != credOwn {
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

	// The control shows no choice made, because this agent's share lent
	// nothing and its owner chose nothing for it.
	if got := lendModeFor("alice", "a2", "share_panel_hand", false, false, false); got != "" {
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
		r := httptest.NewRequest(http.MethodPost, "/api/agents/a3/reach/credential", strings.NewReader(string(body)))
		w := httptest.NewRecorder()
		routed(app)(w, asUser(r, "alice"))
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
			if !it.Decide {
				t.Errorf("credential row has no decision on it: %+v", it)
			}
			// Not the owner's key: there is nothing of theirs to lend, so
			// the row does not ask them to choose.
			if it.Fix != "" {
				t.Errorf("a credential that is not the owner's asks for a choice: %q", it.Fix)
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

// On a shared agent, a key of the owner's with no choice made yet says so on
// its row, naming the usual answer, and the Share tab counts it. Choosing,
// even "theirs", is recorded as a choice. Disallow withholds it for the people
// it is shared with and takes back the agent's lend; moving off Disallow gives
// it back. An answer that is not one of the four is refused.
func TestACredentialOnASharedAgentAsksForAChoice(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if err := Secure().Save(SecureCredential{Name: "share_panel_dec", Type: SecureCredNone,
		Owner: "alice", AllowedURLPattern: "https://wiki.example/**"}, ""); err != nil {
		t.Skipf("no secure store here: %v", err)
	}
	t.Cleanup(func() { Secure().SetCredentialShares("alice", "share_panel_dec", nil, nil) })
	if _, err := saveAgent(udb, AgentRecord{ID: "a5", Owner: "alice", Name: "Tracker", OrchestratorPrompt: "p",
		AllowedUsers: []string{"bob"},
		Tools:        []TempTool{{Name: "get_page", CommandTemplate: "curl x", Credential: "share_panel_dec"}}}); err != nil {
		t.Fatal(err)
	}
	row := func() (reachItem, agentReachMap) {
		t.Helper()
		a, _ := loadAgent(udb, "a5")
		m := agentReachOf(udb, "alice", a)
		for _, it := range m.Items {
			if it.kind == "credential" && it.id == "share_panel_dec" {
				return it, m
			}
		}
		t.Fatalf("no row for the credential: %+v", m.Items)
		return reachItem{}, m
	}
	decide := func(lend string) int {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"name": "share_panel_dec", "lend": lend})
		r := httptest.NewRequest(http.MethodPost, "/api/agents/a5/reach/credential", strings.NewReader(string(body)))
		w := httptest.NewRecorder()
		routed(app)(w, asUser(r, "alice"))
		return w.Code
	}

	it, m := row()
	if it.Lend != "" || !strings.Contains(it.Fix, "People you add from now on read with your key") || m.Undecided != 1 {
		t.Fatalf("an undecided key on a shared agent: lend %q, fix %q, undecided %d", it.Lend, it.Fix, m.Undecided)
	}
	if code := decide(credOwn); code != http.StatusOK {
		t.Fatalf("theirs: %d", code)
	}
	if it, m := row(); it.Lend != credOwn || m.Undecided != 0 || it.Fix != "" {
		t.Errorf("after choosing theirs: lend %q, undecided %d, fix %q", it.Lend, m.Undecided, it.Fix)
	}
	if code := decide(credRead); code != http.StatusOK || !lentToIn(t, "share_panel_dec", "bob", "a5") {
		t.Fatalf("mine for reads: %d", code)
	}
	if code := decide(credWithhold); code != http.StatusOK {
		t.Fatalf("disallow: %d", code)
	}
	a, _ := loadAgent(udb, "a5")
	if it, _ := row(); it.Lend != credWithhold || lentToIn(t, "share_panel_dec", "bob", "a5") || !namedIn(a.WithheldFromRecipients, "share_panel_dec") {
		t.Errorf("after disallow: lend %q, still lent %v, withheld %v", it.Lend, lentToIn(t, "share_panel_dec", "bob", "a5"), a.WithheldFromRecipients)
	}
	if code := decide(credRead); code != http.StatusOK {
		t.Fatal(code)
	}
	if a, _ := loadAgent(udb, "a5"); namedIn(a.WithheldFromRecipients, "share_panel_dec") {
		t.Error("moving off Disallow left it withheld")
	}
	if code := decide("everything"); code != http.StatusBadRequest {
		t.Errorf("an answer that is not one of the four was taken: %d", code)
	}
}

// A maintainer sees what the agent reaches, as its owner does, and may only
// narrow what the owner's key does on a credential row: to Theirs or
// Disallow, or from writes down to reads. Lending it further, for reads or
// writes, is the owner's alone, and the server holds them to it.
func TestAMaintainerCanOnlyNarrowTheOwnersKey(t *testing.T) {
	app, udb, owner := newTestOrchestrate(t)
	pinRootDB(t)
	adb := AuthDB()
	adb.Set(AuthTable, "user:mia", AuthUser{Username: "mia"})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	if err := Secure().Save(SecureCredential{Name: "jira_m", Type: SecureCredNone, Owner: owner,
		AllowedURLPattern: "https://jira.example/**"}, ""); err != nil {
		t.Skipf("no secure store here: %v", err)
	}
	t.Cleanup(func() { Secure().SetCredentialShares(owner, "jira_m", nil, nil) })
	if _, err := saveAgent(udb, AgentRecord{ID: "a10", Owner: owner, Name: "Tracker", OrchestratorPrompt: "p",
		AllowedUsers: []string{"bob"}, Maintainers: []string{"mia"},
		Tools: []TempTool{{Name: "search", CommandTemplate: "curl x", Credential: "jira_m"}}}); err != nil {
		t.Fatal(err)
	}
	as := func(user, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, user)})
		w := httptest.NewRecorder()
		routed(app)(w, r)
		return w
	}
	decide := func(user, lend string) int {
		return as(user, "POST", "/api/agents/a10/reach/credential", `{"name":"jira_m","lend":"`+lend+`"}`).Code
	}
	if w := as("mia", "GET", "/api/agents/a10/reach", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "jira_m") {
		t.Fatalf("the maintainer does not see what the agent reaches: %d %s", w.Code, w.Body.String())
	}
	for _, lend := range []string{credRead, credWrite} {
		if code := decide("mia", lend); code != http.StatusForbidden {
			t.Errorf("a maintainer lent the owner's key (%s) from no lend: %d", lend, code)
		}
	}
	if lentToIn(t, "jira_m", "bob", "a10") {
		t.Fatal("a refused choice lent the key anyway")
	}
	if code := decide(owner, credWrite); code != 200 {
		t.Fatalf("the owner's writes: %d", code)
	}
	// The owner's choice reaches everybody who runs it, its maintainers too.
	if c, _ := Secure().LoadUser(owner, "jira_m"); !namedIn(c.SharedReadWrite, "bob") || !namedIn(c.SharedReadWrite, "mia") {
		t.Errorf("the owner's writes did not reach its users and maintainers: write %v", c.SharedReadWrite)
	}
	// Somebody the owner never lent it to joins the agent.
	adb.Set(AuthTable, "user:dana", AuthUser{Username: "dana"})
	if a, _ := loadAgent(udb, "a10"); true {
		a.AllowedUsers = append(a.AllowedUsers, "dana")
		saveAgent(udb, a)
	}
	if code := decide("mia", credRead); code != 200 {
		t.Fatalf("a maintainer could not take writes back down to reads: %d", code)
	}
	if lentToIn(t, "jira_m", "dana", "a10") {
		t.Error("a maintainer's reads lent the owner's key to somebody the owner never lent it to")
	}
	if c, _ := Secure().LoadUser(owner, "jira_m"); namedIn(c.SharedReadWrite, "bob") || !namedIn(c.SharedReadOnly, "bob") {
		t.Errorf("from writes down to reads left read %v write %v", c.SharedReadOnly, c.SharedReadWrite)
	}
	if code := decide("mia", credWrite); code != http.StatusForbidden {
		t.Errorf("a maintainer put writes back on the owner's key: %d", code)
	}
	if code := decide("mia", credOwn); code != 200 || lentToIn(t, "jira_m", "bob", "a10") {
		t.Errorf("a maintainer could not move it to Theirs: %d", code)
	}
	if code := decide("mia", credRead); code != http.StatusForbidden {
		t.Errorf("a maintainer lent the owner's key again from Theirs: %d", code)
	}
	if code := decide("mia", credWithhold); code != 200 {
		t.Errorf("a maintainer could not disallow it: %d", code)
	}
	if code := as("bob", "POST", "/api/agents/a10/reach/credential", `{"name":"jira_m","lend":"own"}`).Code; code != http.StatusNotFound {
		t.Errorf("somebody who only uses the agent changed a row: %d", code)
	}
}
