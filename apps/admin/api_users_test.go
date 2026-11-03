package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
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
