package orchestrate

// A share no longer grants dependencies — they travel with the agent — so the
// only thing left to take back is a credential lent as part of one.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The reason the lends are recorded: taking somebody off the agent takes back
// the key that was lent for it, without touching anything else.
func TestDroppingARecipientTakesBackTheKeyLentForTheAgent(t *testing.T) {
	udb, a := guidedFixture(t)
	a.AllowedUsers = []string{"bob", "carol"}
	saveAgent(udb, a)
	shareThroughRows("alice", "a1", []string{"bob", "carol"},
		map[string]string{"cred:wiki": credRead})

	c, _ := Secure().LoadUser("alice", "wiki")
	if !namedIn(c.SharedReadOnly, "bob") || !namedIn(c.SharedReadOnly, "carol") {
		t.Fatalf("the lend did not happen: %+v", c.SharedReadOnly)
	}

	// Bob leaves the agent; carol stays.
	a, _ = loadAgent(udb, "a1")
	a.AllowedUsers = []string{"carol"}
	if _, err := saveAgent(udb, a); err != nil {
		t.Fatalf("save: %v", err)
	}
	c, _ = Secure().LoadUser("alice", "wiki")
	if namedIn(c.SharedReadOnly, "bob") {
		t.Errorf("bob left the agent and kept the key: %+v", c.SharedReadOnly)
	}
	if !namedIn(c.SharedReadOnly, "carol") {
		t.Errorf("carol was still on the agent and lost the key: %+v", c.SharedReadOnly)
	}
}

// The distinction the ledger exists for. A key the owner lent by hand, for
// their own reasons, survives an un-share of an agent that happens to use it —
// otherwise one convenient share makes every later removal a thing to fear.
func TestAHandLentKeyIsNeverClawedBack(t *testing.T) {
	udb, a := guidedFixture(t)
	// Lent by hand, not through any share.
	if err := Secure().SetCredentialShares("alice", "wiki", []string{"bob"}, nil); err != nil {
		t.Fatalf("lend: %v", err)
	}
	a.AllowedUsers = []string{"bob"}
	saveAgent(udb, a)

	a, _ = loadAgent(udb, "a1")
	a.AllowedUsers = nil
	saveAgent(udb, a)

	c, _ := Secure().LoadUser("alice", "wiki")
	if !namedIn(c.SharedReadOnly, "bob") {
		t.Error("a hand-lent key was taken back by an unrelated un-share")
	}
}

// Nothing else is granted, so nothing else has to be taken back.
func TestASharedAgentGrantsNothingButKeys(t *testing.T) {
	udb, _ := guidedFixture(t)
	shareThroughRows("alice", "a1", []string{"bob"}, map[string]string{"cred:wiki": credOwn})
	// The skill is reachable through the agent and was never handed over.
	for _, s := range LoadSkills(RootDB, "alice") {
		if s.ID == "s1" && len(s.AllowedUsers) != 0 {
			t.Errorf("sharing the agent handed over its skill: %+v", s.AllowedUsers)
		}
	}
	// And it still resolves for a turn bob runs, because it travels.
	turn := &chatTurn{udb: UserDB(RootDB, "bob"), user: "bob", ownerDB: udb, ownerUser: "alice",
		agent: AgentRecord{ID: "a1", Owner: "alice", AllowedSkills: []string{"s1"}}}
	var ids []string
	for _, s := range turn.agentSkills() {
		ids = append(ids, s.ID)
	}
	if !namedIn(ids, "s1") {
		t.Errorf("the skill did not travel with the agent: %v", ids)
	}
}

// Sharing gives the same experience by default: the people added, as users or
// maintainers, get the owner's key for reads, for this agent only. Somebody
// already on it is left as they were; once the row says Theirs, the next
// person added gets nothing.
func TestSharingLendsTheKeyForReadsToThePeopleAdded(t *testing.T) {
	app, udb, owner := newTestOrchestrate(t)
	pinRootDB(t)
	adb := AuthDB()
	for _, u := range []string{"bob", "carol", "dana", "erin"} {
		adb.Set(AuthTable, "user:"+u, AuthUser{Username: u})
	}
	if err := Secure().Save(SecureCredential{Name: "jira_def", Type: SecureCredNone, Owner: owner,
		AllowedURLPattern: "https://jira.example/**"}, ""); err != nil {
		t.Skipf("no secure store here: %v", err)
	}
	t.Cleanup(func() { Secure().SetCredentialShares(owner, "jira_def", nil, nil) })
	if _, err := saveAgent(udb, AgentRecord{ID: "a9", Owner: owner, Name: "Tracker", OrchestratorPrompt: "p",
		AllowedUsers: []string{"dana"},
		Tools:        []TempTool{{Name: "search", CommandTemplate: "curl x", Credential: "jira_def"}}}); err != nil {
		t.Fatal(err)
	}
	as := func(method, path, body string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, owner)})
		w := httptest.NewRecorder()
		routed(app)(w, r)
		return w.Code
	}
	if code := as("POST", "/api/console/permissions/share?agent=a9", `{"allowed_users":["dana","bob"]}`); code != 200 {
		t.Fatalf("share: %d", code)
	}
	if code := as("POST", "/api/console/permissions/share?agent=a9", `{"maintainers":["erin"]}`); code != 200 {
		t.Fatalf("maintainers: %d", code)
	}
	if !lentToIn(t, "jira_def", "bob", "a9") || !lentToIn(t, "jira_def", "erin", "a9") {
		t.Fatal("the people added were not lent the key for reads")
	}
	if c, _ := Secure().LoadUser(owner, "jira_def"); !namedIn(c.SharedReadOnly, "bob") || namedIn(c.SharedReadWrite, "bob") {
		t.Errorf("the default lend is not for reads: read %v write %v", c.SharedReadOnly, c.SharedReadWrite)
	}
	if lentToIn(t, "jira_def", "dana", "a9") {
		t.Error("somebody already on the agent was lent the key by somebody else being added")
	}
	// With the row at Mine with writes too, the next person added gets writes.
	if code := as("POST", "/api/agents/a9/reach/credential", `{"name":"jira_def","lend":"write"}`); code != 200 {
		t.Fatalf("writes: %d", code)
	}
	adb.Set(AuthTable, "user:finn", AuthUser{Username: "finn"})
	if code := as("POST", "/api/console/permissions/share?agent=a9", `{"allowed_users":["dana","bob","finn"]}`); code != 200 {
		t.Fatalf("share with finn: %d", code)
	}
	if c, _ := Secure().LoadUser(owner, "jira_def"); !namedIn(c.SharedReadWrite, "finn") {
		t.Errorf("with the row at writes, somebody added was not lent writes: write %v", c.SharedReadWrite)
	}
	if code := as("POST", "/api/agents/a9/reach/credential", `{"name":"jira_def","lend":"own"}`); code != 200 {
		t.Fatalf("theirs: %d", code)
	}
	if code := as("POST", "/api/console/permissions/share?agent=a9", `{"allowed_users":["dana","carol"]}`); code != 200 {
		t.Fatalf("share again: %d", code)
	}
	if lentToIn(t, "jira_def", "carol", "a9") {
		t.Error("with the row at Theirs, somebody added was lent the key anyway")
	}
}
