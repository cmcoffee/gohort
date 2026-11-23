package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/gohort/core/promotion"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An administrator adding an account is held to the same name rule as a
// signup (core/accountname): stored lowercased, plain characters only, and
// not an existing account's name in another case.
func TestAnAddedAccountFollowsTheNameRule(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(db, "root", "pw", true)
	AuthSetUser(db, "Mixed@Example.test", "pw", false)
	token := AuthCreateSession(db, "root")
	a := &AdminApp{db: db}
	mux := http.NewServeMux()
	a.registerUsersRoutes(mux)
	add := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/users", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := add(`{"username":" Carol@Example.TEST ","password":"pw-c-123456"}`); w.Code >= 400 {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	if _, ok := AuthGetUser(db, "carol@example.test"); !ok {
		t.Error("the added name was not stored lowercased")
	}
	if w := add(`{"username":"mixed@example.test","password":"pw-c-123456"}`); w.Code != http.StatusConflict {
		t.Errorf("an existing name in another case: %d %s", w.Code, w.Body.String())
	}
	if w := add(`{"username":"eve@example.test:root","password":"pw-c-123456"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a name holding a colon: %d %s", w.Code, w.Body.String())
	}
	if _, ok := AuthGetUser(db, "eve@example.test:root"); ok {
		t.Error("the refused name was stored")
	}
}

// The Allowed column says who a list admits, in words: an unset feature is
// everyone and says so, an emptied one is nobody, a Closed one keeps its
// administrators.
func TestFeatureAccessSaysWhoItAdmits(t *testing.T) {
	for _, c := range []struct {
		p      FeatureAccessPolicy
		closed bool
		want   string
	}{
		{FeatureAccessPolicy{Feature: "openai"}, false, "Everyone (not set yet)"},
		{FeatureAccessPolicy{Feature: "openai", Saved: true}, false, "Nobody"},
		{FeatureAccessPolicy{Feature: "openai", Saved: true, AllowedUsers: []string{"*"}}, false, "Everyone"},
		{FeatureAccessPolicy{Feature: "openai", Saved: true, AllowedUsers: []string{"ann", "group:ops", "group:dev"}}, false, "1 person, 2 teams"},
		{FeatureAccessPolicy{Feature: "model-access", Saved: true}, true, "Administrators only"},
		{FeatureAccessPolicy{Feature: "model-access", Saved: true, AllowedUsers: []string{"ann", "bo"}}, true, "2 people, and administrators"},
	} {
		if got := featureAccessSummary(c.p, c.closed); got != c.want {
			t.Errorf("%+v: %q, want %q", c.p, got, c.want)
		}
	}
}

// An administrator names people where they add and list them: an optional
// name on Add user, kept as given (cleaned), and the Name on a person's row,
// which changes that field alone, so it cannot undo an Admin switch made
// while it was open. A blank clears it, since a name is optional.
func TestAnAdministratorNamesPeople(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(db, "root", "pw", true)
	token := AuthCreateSession(db, "root")
	a := &AdminApp{db: db}
	mux := http.NewServeMux()
	a.registerUsersRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := call("POST", "/api/users", `{"username":"pat@example.test","password":"pw-p-123456","display_name":"  Pat   Smith "}`); w.Code >= 400 {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	if u, _ := AuthGetUser(db, "pat@example.test"); u.DisplayName != "Pat Smith" {
		t.Errorf("added as %q", u.DisplayName)
	}
	if w := call("POST", "/api/users", `{"username":"lee@example.test","password":"pw-l-123456","display_name":"Lee\u0007"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a name the rule refuses: %d", w.Code)
	}
	if _, ok := AuthGetUser(db, "lee@example.test"); ok {
		t.Error("an account was added under a name the rule refuses")
	}
	if w := call("GET", "/api/users/pat@example.test", ""); !strings.Contains(w.Body.String(), `"display_name":"Pat Smith"`) {
		t.Errorf("the row's record: %s", w.Body.String())
	}
	// The Admin switch, then the name from a row opened before it.
	if w := call("PUT", "/api/users/pat@example.test", `{"admin":true}`); w.Code >= 400 {
		t.Fatalf("admin on: %d %s", w.Code, w.Body.String())
	}
	if w := call("PATCH", "/api/users/pat@example.test", `{"display_name":"Patricia"}`); w.Code >= 400 {
		t.Fatalf("rename: %d %s", w.Code, w.Body.String())
	}
	if u, _ := AuthGetUser(db, "pat@example.test"); u.DisplayName != "Patricia" || !u.Admin {
		t.Errorf("after the rename: name %q, admin %v", u.DisplayName, u.Admin)
	}
	// A name is optional: a blank clears it.
	if w := call("PATCH", "/api/users/pat@example.test", `{"display_name":" "}`); w.Code >= 400 {
		t.Errorf("a blank name: %d %s", w.Code, w.Body.String())
	}
	if u, _ := AuthGetUser(db, "pat@example.test"); u.DisplayName != "" {
		t.Errorf("a blank left the name %q", u.DisplayName)
	}
	if w := call("PATCH", "/api/users/pat@example.test", `{"display_name":"Pat\u0007"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a name the rule refuses: %d", w.Code)
	}
	if w := call("PATCH", "/api/users/pat@example.test", `{"display_name":"Patricia"}`); w.Code >= 400 {
		t.Fatalf("rename again: %d", w.Code)
	}
	if !strings.Contains(string(UserListJSON(db)), `"display_name":"Patricia"`) {
		t.Error("the Users list does not carry the name")
	}
}

// A request to share lands in every administrator's notices as waiting on
// them, the requester's own left out, and leaves them once it is settled:
// decided, or withdrawn.
func TestAdministratorsHearOfARequestUntilItIsSettled(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prevRoot, prevAuth := RootDB, AuthDB
	RootDB = db
	AuthDB = func() Database { return db }
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })
	prevReq, prevSet := promotion.OnRequested, promotion.OnSettled
	promotion.OnRequested, promotion.OnSettled = tellAdminsRequested, clearAdminsRequested
	t.Cleanup(func() { promotion.OnRequested, promotion.OnSettled = prevReq, prevSet })
	AuthSetUser(db, "ana", "pw-ana-123", true)
	AuthSetUser(db, "carl", "pw-carl-123", true)
	AuthSetUser(db, "bob", "pw-bob-123", false)
	waiting := func(user string) []notices.Notice {
		var out []notices.Notice
		for _, n := range notices.List(db, user) {
			if n.Kind == notices.KindBlocked {
				out = append(out, n)
			}
		}
		return out
	}
	if err := CreatePromotionRequest(db, "bob", "tool", "get_weather", "for the team"); err != nil {
		t.Fatal(err)
	}
	if err := CreatePromotionRequest(db, "carl", "app", "roster", ""); err != nil {
		t.Fatal(err)
	}
	if n := waiting("ana"); len(n) != 2 {
		t.Fatalf("ana has %d waiting, want both requests: %+v", len(n), n)
	}
	if n := waiting("carl"); len(n) != 1 {
		t.Errorf("carl has %d waiting, want only bob's: his own request is not news to him", len(n))
	}
	if n := waiting("bob"); len(n) != 0 {
		t.Errorf("bob, not an administrator, has %d waiting", len(n))
	}
	if err := SetPromotionRequestState(db, PromotionRequestKey("tool", "bob", "get_weather"), PromotionDeniedState, "ana"); err != nil {
		t.Fatal(err)
	}
	if n := waiting("ana"); len(n) != 1 || n[0].About != requestWaitingAbout(PromotionRequestKey("app", "carl", "roster")) {
		t.Errorf("after deciding bob's, ana still has: %+v", n)
	}
	if n := waiting("carl"); len(n) != 0 {
		t.Errorf("after ana decided bob's, carl still has it: %+v", n)
	}
	promotion.WithdrawPromotionRequest(db, "carl", "app", "roster")
	if n := waiting("ana"); len(n) != 0 {
		t.Errorf("after carl withdrew his, ana still has: %+v", n)
	}
}
