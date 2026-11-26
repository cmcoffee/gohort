package servitor

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Servitor's ⚙ sits beside its title, as on every app. The one thing a
// person sets for themselves here is which risky commands run without
// asking, so the ⚙ opens that dialog itself.
func TestServitorsGearOpensThePermissions(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, "alice")})
	w := httptest.NewRecorder()
	T := &Servitor{}
	T.DB = &DBase{Store: kvlite.MemStore()}
	T.handleChatPage(w, r)
	page := w.Body.String()
	for _, want := range []string{`"icon":"settings"`, `"action":"servitor_permissions"`, `uiRegisterClientAction('servitor_permissions'`} {
		if !strings.Contains(page, want) {
			t.Errorf("the page (%d) has no %s", w.Code, want)
		}
	}
}
