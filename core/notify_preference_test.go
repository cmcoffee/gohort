package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// The bell's notify switch saves what it is posted. A body that is not JSON
// is refused: it used to decode to "off" and save that.
func TestNotifyPreferenceRefusesABadBody(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
	cookie := &http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, "alice")}
	call := func(method, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/notify-preference", strings.NewReader(body))
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		handleNotifyPreference(w, r)
		return w
	}

	if w := call("POST", `{"notify":true}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"notify":true`) {
		t.Fatalf("turning it on: %d %s", w.Code, w.Body.String())
	}
	if w := call("POST", `{"notify":`); w.Code != http.StatusBadRequest {
		t.Errorf("a malformed body should be a 400, got %d", w.Code)
	}
	if w := call("GET", ""); !strings.Contains(w.Body.String(), `"notify":true`) {
		t.Errorf("a refused post must leave the setting as it was: %s", w.Body.String())
	}
	if w := call("POST", `{"notify":false}`); !strings.Contains(w.Body.String(), `"notify":false`) || AuthGetNotifyDefault(adb, "alice") {
		t.Errorf("turning it off: %s", w.Body.String())
	}
}
