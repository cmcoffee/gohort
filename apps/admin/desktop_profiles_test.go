package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/desktopprofile"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Desktop profiles through the real routes: only an administrator manages
// them; a profile naming someone who is not a user is refused, and so is a
// second one for the same person; edit and delete act on the one named.
func TestDesktopProfilesThroughTheirRoutes(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return adb }
	RootDB = &DBase{Store: kvlite.MemStore()}
	desktopprofile.SetStore(RootDB)
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot; desktopprofile.SetStore(nil) })

	mux := http.NewServeMux()
	(&AdminApp{}).RegisterRoutes(mux, "")
	do := func(user, method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, user)})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}

	if code, _ := do("alice", "POST", "/api/desktop-profiles", `{"name":"Mine","users":["alice"]}`); code < 400 || len(desktopprofile.List()) != 0 {
		t.Fatalf("a user who is not an administrator made a profile: %d", code)
	}
	if code, body := do("root", "POST", "/api/desktop-profiles", `{"name":"Desk","users":["nobody"]}`); code != 400 || !strings.Contains(body, "no user named nobody") {
		t.Errorf("a profile for someone who is not a user: %d %s", code, body)
	}
	code, body := do("root", "POST", "/api/desktop-profiles", `{"name":"Front Desk","users":["ALICE"],"apps":["/notes"],"start_path":"apps/desk/"}`)
	if code != 200 {
		t.Fatalf("create: %d %s", code, body)
	}
	p, ok := desktopprofile.For("alice")
	if !ok || p.Name != "Front Desk" || p.Users[0] != "alice" || p.StartPath != "/apps/desk/" {
		t.Fatalf("alice's profile: %+v", p)
	}
	if code, body := do("root", "POST", "/api/desktop-profiles", `{"name":"Ops","users":["alice"]}`); code != 400 || !strings.Contains(body, "Front Desk") {
		t.Errorf("a second profile for alice: %d %s", code, body)
	}
	if _, body := do("root", "GET", "/api/desktop-profiles", ""); !strings.Contains(body, `"who":"alice"`) || !strings.Contains(body, `"opens":"/apps/desk/"`) {
		t.Errorf("the list: %s", body)
	}
	if code, body := do("root", "GET", "/api/desktop-profiles/one?id="+p.ID, ""); code != 200 || !strings.Contains(body, `"apps":["/notes"]`) {
		t.Errorf("one for the edit dialog: %d %s", code, body)
	}
	if code, _ := do("root", "POST", "/api/desktop-profiles", `{"id":"`+p.ID+`","name":"Reception","users":["alice"]}`); code != 200 {
		t.Errorf("edit: %d", code)
	}
	if p, _ := desktopprofile.For("alice"); p.Name != "Reception" || len(desktopprofile.List()) != 1 {
		t.Errorf("after the edit: %+v, %d profile(s)", p, len(desktopprofile.List()))
	}
	if code, _ := do("root", "DELETE", "/api/desktop-profiles?id="+p.ID, ""); code != 200 || len(desktopprofile.List()) != 0 {
		t.Errorf("delete: %d", code)
	}
}
