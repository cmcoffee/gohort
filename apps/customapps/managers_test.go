package customapps

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/appscript"
)

// What each person is to an app.
func TestWhatEachPersonIsToAnApp(t *testing.T) {
	spec := AppSpec{Owner: "alice", Managers: []string{"Bob"}}
	for user, want := range map[string]string{"alice": "owner", "bob": "manager", "carol": "user", "": "user"} {
		if got := spec.Role(user); got != want {
			t.Errorf("%q is %q, want %q", user, got, want)
		}
	}
}

// What an app keeps for managers is theirs and the owner's: an ordinary user
// is refused its action and its data source, gets the page without them, and
// the app's chat agent does not get them while answering that user. Every
// script is told what the person is to the app, by the host.
func TestWhatAnAppKeepsForManagers(t *testing.T) {
	T := sharingTestApp(t)
	spec := SaveAppSpec(AppSpec{Slug: "poll", Name: "Lunch Poll", Owner: "alice", RecordKey: "id",
		Managers: []string{"bob"},
		Page:     []byte(`{"title":"full"}`), UserPage: []byte(`{"title":"for users"}`),
		Actions: []AppAction{
			{Name: "vote", Script: "x"},
			{Name: "reset", Script: "x", ManagersOnly: true},
		},
		DataSources: []AppDataSource{{Name: "audit", Script: "x", ManagersOnly: true}},
	})
	prev := runAppScript
	t.Cleanup(func() { runAppScript = prev })
	roles := map[string]string{}
	runAppScript = func(j appscript.Job) (string, error) {
		roles[j.Caller] = j.Args["caller_role"].(string)
		return `{"message":"ok"}`, nil
	}
	post := func(user, path string) int {
		return appAs(t, T, spec, user, httptest.NewRequest(http.MethodPost, "/apps/poll/"+path, strings.NewReader(`{"caller_role":"owner"}`))).Code
	}
	if code := post("carol", "action/reset"); code != http.StatusForbidden {
		t.Errorf("an ordinary user ran a managers' action: %d", code)
	}
	if code := appAs(t, T, spec, "carol", httptest.NewRequest(http.MethodGet, "/apps/poll/data/audit", nil)).Code; code != http.StatusForbidden {
		t.Errorf("an ordinary user read a managers' data source: %d", code)
	}
	for _, who := range []string{"bob", "alice"} {
		if code := post(who, "action/reset"); code >= 300 {
			t.Errorf("%s was refused the managers' action: %d", who, code)
		}
	}
	if code := post("carol", "action/vote"); code >= 300 {
		t.Errorf("an ordinary user was refused an ordinary action: %d", code)
	}
	if roles["carol"] != "user" || roles["bob"] != "manager" || roles["alice"] != "owner" {
		t.Errorf("caller_role as scripts saw it (a body cannot set it): %v", roles)
	}

	buttons := func(user string) string {
		return appAs(t, T, spec, user, httptest.NewRequest(http.MethodGet, "/apps/poll/actions", nil)).Body.String()
	}
	if b := buttons("carol"); strings.Contains(b, `"reset"`) || !strings.Contains(b, `"vote"`) {
		t.Errorf("an ordinary user's buttons: %s", b)
	}
	if b := buttons("bob"); !strings.Contains(b, `"reset"`) {
		t.Errorf("a manager's buttons: %s", b)
	}

	stored, _ := loadSpec("alice", "poll")
	for user, want := range map[string]string{"carol": "for users", "bob": "full", "alice": "full"} {
		if got := string(appPageFor(stored, user)); !strings.Contains(got, want) {
			t.Errorf("%s gets the page %s, want %q", user, got, want)
		}
	}

	names := func(user string) map[string]bool {
		out := map[string]bool{}
		for _, d := range T.agentToolsFor("alice", user, T.recordBase(stored, user), forUser(stored, user), "", "") {
			out[d.Tool.Name] = true
		}
		return out
	}
	if n := names("carol"); n["poll_reset"] || n["read_poll_audit"] || !n["poll_vote"] {
		t.Errorf("the app's agent, answering an ordinary user: %v", n)
	}
	if n := names("bob"); !n["poll_reset"] || !n["read_poll_audit"] {
		t.Errorf("the app's agent, answering a manager: %v", n)
	}
}

// Naming a manager shares the app with them; unsharing them drops the role;
// taking the role off leaves them able to use it.
func TestNamingAManagerSharesTheApp(t *testing.T) {
	T := sharingTestApp(t)
	auth := &DBase{Store: T.DB.(*DBase).Store}
	prev := AuthDB
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { AuthDB = prev })
	AuthSetUser(auth, "alice", "pw", false)
	AuthSetUser(auth, "bob", "pw", false)
	SaveAppSpec(AppSpec{Slug: "poll", Name: "Lunch Poll", Owner: "alice"})

	if err := T.setManager("alice", "poll", "bob", true); err != nil {
		t.Fatal(err)
	}
	spec, _ := loadSpec("alice", "poll")
	if !spec.ManagedBy("bob") || !personShareReaches(spec, "bob") {
		t.Fatalf("named a manager: managers %v shared with %v", spec.Managers, spec.SharedWith)
	}
	if !strings.Contains(shareManagersJSON(spec), `"with":["bob"]`) {
		t.Errorf("the dialog does not list bob: %s", shareManagersJSON(spec))
	}
	T.setManager("alice", "poll", "bob", false)
	if spec, _ = loadSpec("alice", "poll"); spec.ManagedBy("bob") || !personShareReaches(spec, "bob") {
		t.Errorf("taking the role off: managers %v shared with %v", spec.Managers, spec.SharedWith)
	}
	T.setManager("alice", "poll", "bob", true)
	T.setPersonShare("alice", "poll", "bob", false)
	if spec, _ = loadSpec("alice", "poll"); spec.ManagedBy("bob") {
		t.Errorf("unshared, bob still manages it: %v", spec.Managers)
	}
}
