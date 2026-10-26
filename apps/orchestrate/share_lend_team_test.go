package orchestrate

// A credential lent for an agent shared with a team is lent to the people on
// the team, and taken back from whoever the team share stops reaching: a
// member leaving, the owner leaving, the team losing "can share".

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// teamLendWorld: alice and bob on Ops, dana outside it, alice's key, and
// agent a1 shared with Ops. Returns the auth store and a lend function.
func teamLendWorld(t *testing.T, cred string) (Database, func(agentID, lend string)) {
	t.Helper()
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	adb := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
	SaveAppGroup(adb, AppGroup{ID: "g-ops", Name: "Ops", Shareable: true})
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice", Groups: []string{"g-ops"}})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob", Groups: []string{"g-ops"}})
	adb.Set(AuthTable, "user:dana", AuthUser{Username: "dana"})

	if err := Secure().Save(SecureCredential{Name: cred, Type: SecureCredNone,
		Owner: "alice", AllowedURLPattern: "https://wiki.example/**"}, ""); err != nil {
		t.Skipf("no secure store here: %v", err)
	}
	t.Cleanup(func() { Secure().SetCredentialShares("alice", cred, nil, nil) })
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Owner: "alice", Name: "Runbooks",
		OrchestratorPrompt: "p", AllowedUsers: []string{"group:g-ops"}}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	lend := func(agentID, mode string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"name": cred, "lend": mode})
		r := httptest.NewRequest(http.MethodPost, "/api/agents/"+agentID+"/reach/credential", strings.NewReader(string(body)))
		w := httptest.NewRecorder()
		routed(app)(w, asUser(r, "alice"))
		if w.Code != http.StatusOK {
			t.Fatalf("lend %s on %s: %d %s", mode, agentID, w.Code, w.Body.String())
		}
	}
	return adb, lend
}

// A member leaving the team loses the key for that agent, and only that: a
// lend for another agent shared with them by name stays.
func TestALendThroughATeamIsTakenBackWhenTheMemberLeaves(t *testing.T) {
	adb, lend := teamLendWorld(t, "team_lend_wiki")
	udb := UserDB(orchestrateBaseDB, "alice")
	if _, err := saveAgent(udb, AgentRecord{ID: "a2", Owner: "alice", Name: "Other",
		OrchestratorPrompt: "p", AllowedUsers: []string{"bob"}}); err != nil {
		t.Fatal(err)
	}
	lend("a1", credRead)
	lend("a2", credRead)

	c, _ := Secure().LoadUser("alice", "team_lend_wiki")
	for _, u := range append(append([]string{}, c.SharedReadOnly...), c.SharedReadWrite...) {
		if strings.HasPrefix(u, "group:") {
			t.Fatalf("the key was lent to the team token, not its people: %v", c.SharedReadOnly)
		}
	}
	if !lentToIn(t, "team_lend_wiki", "bob", "a1") {
		t.Fatal("bob, on Ops, was not lent the key for the agent shared with Ops")
	}
	if lentToIn(t, "team_lend_wiki", "dana", "a1") {
		t.Fatal("dana, outside Ops, was lent the key")
	}

	AuthSetUserGroups(adb, "bob", nil)

	if lentToIn(t, "team_lend_wiki", "bob", "a1") {
		t.Error("bob left Ops but still holds the key for the agent shared with Ops")
	}
	if !lentToIn(t, "team_lend_wiki", "bob", "a2") {
		t.Error("bob's lend for the agent shared with him by name was taken too")
	}
}

// The owner leaving, the team losing "can share", and the team being deleted
// each end the share, and with it the lend.
func TestALendThroughATeamEndsWithTheTeamShare(t *testing.T) {
	for name, end := range map[string]func(Database){
		"owner leaves":    func(adb Database) { AuthSetUserGroups(adb, "alice", nil) },
		"sharing off":     func(adb Database) { SaveAppGroup(adb, AppGroup{ID: "g-ops", Name: "Ops"}) },
		"team deleted":    func(adb Database) { DeleteAppGroup(adb, "g-ops") },
		"hand lend stays": nil,
	} {
		t.Run(name, func(t *testing.T) {
			adb, lend := teamLendWorld(t, "team_lend_end")
			t.Cleanup(func() { DeleteAppGroup(adb, "g-ops") })
			if end == nil {
				// A lend made by hand on the credential is not the share's,
				// so a team change leaves it standing.
				Secure().SetCredentialShares("alice", "team_lend_end", []string{"bob"}, nil)
				lend("a1", credRead)
				AuthSetUserGroups(adb, "bob", nil)
				if !lentToIn(t, "team_lend_end", "bob", "a1") {
					t.Error("a lend made by hand was taken back by a team change")
				}
				return
			}
			lend("a1", credRead)
			if !lentToIn(t, "team_lend_end", "bob", "a1") {
				t.Fatal("the lend was not made")
			}
			end(adb)
			if lentToIn(t, "team_lend_end", "bob", "a1") {
				t.Error("the team share ended but bob still holds the key")
			}
		})
	}
}
