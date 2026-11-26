package codewriter

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The page answers only at the bare path: a mistyped API path under it is a
// 404, not the page with a 200.
func TestThePageAnswersOnlyAtItsOwnPath(t *testing.T) {
	T := &CodeWriterAgent{}
	for path, notFound := range map[string]bool{"/api/typo": true, "/nope/deeper": true, "/": false} {
		w := httptest.NewRecorder()
		T.handleCodeWriterPage(w, httptest.NewRequest("GET", path, nil))
		if (w.Code == http.StatusNotFound) != notFound {
			t.Errorf("GET %s answered %d", path, w.Code)
		}
	}
}

// CodeWriter's ⚙ sits beside its title, as on every app, and opens a menu of
// what a person sets for themselves: the rules every reply follows and their
// copy of the Code Writer.
func TestCodeWritersGearOpensItsSettings(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, "alice")})
	w := httptest.NewRecorder()
	(&CodeWriterAgent{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}).handleCodeWriterPage(w, r)
	page := w.Body.String()
	for _, want := range []string{`"icon":"settings"`, `"action":"codewriter_settings"`,
		`uiRegisterClientAction("codewriter_settings"`, `uiRegisterClientAction("codewriter_rules"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the page (%d) has no %s", w.Code, want)
		}
	}
	for _, want := range []string{"action: 'codewriter_rules'", "url: 'chat/settings'"} {
		if !strings.Contains(codeWriterSettingsAction, want) {
			t.Errorf("the ⚙ menu does not offer %s", want)
		}
	}
	if !strings.Contains(codeWriterRulesAction, "url: 'api/rules'") {
		t.Error("Rules from the ⚙ does not open the person's rules")
	}
}
