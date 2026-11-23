package account

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The page answers only at the bare path: a mistyped API path under it is a
// 404, not the page with a 200.
func TestThePageAnswersOnlyAtItsOwnPath(t *testing.T) {
	T := &Account{}
	for path, notFound := range map[string]bool{"/api/typo": true, "/nope/deeper": true, "/": false} {
		w := httptest.NewRecorder()
		T.servePage(w, httptest.NewRequest("GET", path, nil))
		if (w.Code == http.StatusNotFound) != notFound {
			t.Errorf("GET %s answered %d", path, w.Code)
		}
	}
}

// A key somebody lent you is listed under Connected accounts, saying whose it
// is, and you put your own key on it there: the save names its owner, so two
// people's "jira" never mix, and somebody it is not lent to cannot.
func TestAKeyLentToYouTakesYourOwnUnderConnectedAccounts(t *testing.T) {
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	t.Cleanup(func() { AuthDB = prev })
	if err := Secure().Save(SecureCredential{Name: "jira", Type: SecureCredBearer, Owner: "alice", BaseURL: "https://jira.example"}, "alice-key"); err != nil {
		t.Fatal(err)
	}
	if err := Secure().LendForAgent("alice", "jira", "a1", []string{"bob"}, false); err != nil {
		t.Fatal(err)
	}
	T := &Account{}
	lent := func(user string) []PerUserConnection {
		got, err := T.handleConnections(&jsonapi.Call{User: user})
		if err != nil {
			t.Fatal(err)
		}
		var out []PerUserConnection
		for _, c := range got.([]PerUserConnection) {
			if c.Kind == "lent" {
				out = append(out, c)
			}
		}
		return out
	}
	l := lent("bob")
	if len(l) != 1 || l[0].Name != "jira" || l[0].Owner != "alice" || l[0].Connected || !strings.Contains(l[0].Description, "Lent to you by alice") {
		t.Fatalf("bob's lent connections: %+v", l)
	}
	if _, err := T.handleConnectionSave(&jsonapi.Call{User: "bob"}, connectionSave{Name: "jira", Kind: "lent", Owner: "alice", Secret: "bob-key"}); err != nil {
		t.Fatal(err)
	}
	if l := lent("bob"); len(l) != 1 || !l[0].Connected {
		t.Errorf("after connecting: %+v", l)
	}
	if _, err := T.handleConnectionSave(&jsonapi.Call{User: "carol"}, connectionSave{Name: "jira", Kind: "lent", Owner: "alice", Secret: "carol-key"}); err == nil {
		t.Error("somebody the key is not lent to put a key on it")
	}
	if _, err := T.handleConnectionSave(&jsonapi.Call{User: "bob"}, connectionSave{Name: "jira", Kind: "lent", Owner: "alice", Disconnect: true}); err != nil {
		t.Fatal(err)
	}
	if l := lent("bob"); len(l) != 1 || l[0].Connected {
		t.Errorf("after disconnecting: %+v", l)
	}
	if l := lent("alice"); len(l) != 0 {
		t.Errorf("the owner's own key is listed as lent to them: %+v", l)
	}
}
