package admin

// One name, one skill, per person, on the admin editor as on Extensions.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestAdminSkillCreateRefusesANameYouHave(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return db }
	RootDB = db
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })
	db.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	token := AuthCreateSession(db, "root")

	a := &AdminApp{db: db}
	mux := http.NewServeMux()
	a.registerSkillsRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	made, err := SaveSkill(db, "root", SkillRecord{Name: "pdf", Description: "d", Instructions: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if w := call("POST", "/api/skills", `{"name":"PDF","description":"d","instructions":"y"}`); w.Code != http.StatusConflict {
		t.Errorf("a second skill under a name the user has: %d %s", w.Code, w.Body.String())
	}
	if got := LoadSkills(db, "root"); len(got) != 1 {
		t.Fatalf("want one skill, have %+v", got)
	}
	// Editing the one that holds the name still saves.
	if w := call("POST", "/api/skills?id="+made.ID, `{"name":"pdf","description":"d2","instructions":"y"}`); w.Code/100 != 2 {
		t.Errorf("an edit keeping its name was refused: %d %s", w.Code, w.Body.String())
	}
}
