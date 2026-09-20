package orchestrate

// A share no longer grants dependencies — they travel with the agent — so the
// only thing left to take back is a credential lent as part of one.

import (
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
	shareAgentGuided("alice", "a1", []string{"bob", "carol"},
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

// Nothing else is granted, so nothing else has to be taken back. The report
// says the dependencies travel rather than listing grants it did not make.
func TestASharedAgentGrantsNothingButKeys(t *testing.T) {
	udb, _ := guidedFixture(t)
	lines := shareAgentGuided("alice", "a1", []string{"bob"},
		map[string]string{"cred:wiki": credOwn})
	joined := strings.Join(lines, " | ")

	if !strings.Contains(joined, "travel with it") {
		t.Errorf("the report does not say the dependencies travel: %v", lines)
	}
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
