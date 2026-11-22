package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/userprofile"
	"github.com/cmcoffee/snugforge/kvlite"
)

// User profiles through the real routes: only an administrator manages
// them; a profile naming someone who is not a user, or a team that is not
// one, is refused, and so is a second one for the same person; edit and
// delete act on the one named; Who gets which says who gets what, and why.
func TestUserProfilesThroughTheirRoutes(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return adb }
	RootDB = &DBase{Store: kvlite.MemStore()}
	userprofile.SetStore(RootDB)
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot; userprofile.SetStore(nil) })

	mux := http.NewServeMux()
	(&AdminApp{}).RegisterRoutes(mux, "")
	do := func(user, method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, user)})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}

	if code, _ := do("alice", "POST", "/api/user-profiles", `{"name":"Mine","users":["alice"]}`); code < 400 || len(userprofile.List()) != 0 {
		t.Fatalf("a user who is not an administrator made a profile: %d", code)
	}
	if code, body := do("root", "POST", "/api/user-profiles", `{"name":"Desk","users":["nobody"]}`); code != 400 || !strings.Contains(body, "no user named nobody") {
		t.Errorf("a profile for someone who is not a user: %d %s", code, body)
	}
	code, body := do("root", "POST", "/api/user-profiles", `{"name":"Front Desk","users":["ALICE"],"apps":["/notes"],"start_path":"apps/desk/"}`)
	if code != 200 {
		t.Fatalf("create: %d %s", code, body)
	}
	p, ok := userprofile.Of("alice")
	if !ok || p.Name != "Front Desk" || p.Users[0] != "alice" || p.StartPath != "/apps/desk/" {
		t.Fatalf("alice's profile: %+v", p)
	}
	if code, body := do("root", "POST", "/api/user-profiles", `{"name":"Ops","users":["alice"]}`); code != 400 || !strings.Contains(body, "Front Desk") {
		t.Errorf("a second profile for alice: %d %s", code, body)
	}
	if _, body := do("root", "GET", "/api/user-profiles", ""); !strings.Contains(body, `"who":"alice"`) || !strings.Contains(body, `"opens":"/apps/desk/"`) {
		t.Errorf("the list: %s", body)
	}
	if code, body := do("root", "GET", "/api/user-profiles/one?id="+p.ID, ""); code != 200 || !strings.Contains(body, `"apps":["/notes"]`) {
		t.Errorf("one for the edit dialog: %d %s", code, body)
	}
	if code, _ := do("root", "POST", "/api/user-profiles", `{"id":"`+p.ID+`","name":"Reception","users":["alice"]}`); code != 200 {
		t.Errorf("edit: %d", code)
	}
	if p, _ := userprofile.Of("alice"); p.Name != "Reception" || len(userprofile.List()) != 1 {
		t.Errorf("after the edit: %+v, %d profile(s)", p, len(userprofile.List()))
	}
	team, err := SaveAppGroup(adb, AppGroup{Name: "Ops"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { DeleteAppGroup(adb, team.ID) }) // teams are cached past the test
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob", Groups: []string{team.ID}})
	if code, body := do("root", "POST", "/api/user-profiles", `{"name":"Ops","teams":["no-such-team"]}`); code != 400 || !strings.Contains(body, "no such team") {
		t.Errorf("a profile for a team that is not one: %d %s", code, body)
	}
	if code, body := do("root", "POST", "/api/user-profiles", `{"name":"Ops","teams":["`+team.ID+`"],"also_web":true,"removes":["api_keys","feature:mcp"]}`); code != 200 {
		t.Fatalf("a team's profile: %d %s", code, body)
	}
	if code, body := do("root", "POST", "/api/user-profiles", `{"name":"Odd","users":["alice"],"removes":["the_kitchen_sink"]}`); code != 400 {
		t.Errorf("a profile taking away something unknown: %d %s", code, body)
	}
	_, body = do("root", "GET", "/api/user-profiles", "")
	if !strings.Contains(body, `"who":"team Ops"`) || !strings.Contains(body, `"where":"Desktop app and browser"`) || !strings.Contains(body, `"removes":"API keys, `) {
		t.Errorf("the list: %s", body)
	}
	_, body = do("root", "GET", "/api/user-profiles/people", "")
	for _, want := range []string{`{"user":"alice","profile":"Reception","why":"Named on it"}`, `{"user":"bob","profile":"Ops","why":"On the team Ops"}`, `"user":"root","profile":"None","why":"An administrator`} {
		if !strings.Contains(body, want) {
			t.Errorf("who gets which is missing %s: %s", want, body)
		}
	}
	if code, _ := do("root", "DELETE", "/api/user-profiles?id="+p.ID, ""); code != 200 || len(userprofile.List()) != 1 {
		t.Errorf("delete: %d", code)
	}
}
