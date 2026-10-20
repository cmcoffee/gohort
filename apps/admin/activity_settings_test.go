package admin

// A setting an administrator changes goes in the activity log, in the words
// the server log uses, under the administrator who changed it.

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestAChangedSettingIsInTheActivityLog(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prevRoot, prevAuth := RootDB, AuthDB
	RootDB = db
	AuthDB = func() Database { return db }
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })
	SetTunablesDB(db)
	t.Cleanup(func() { SetTunablesDB(nil) })
	AuthSetUser(db, "root", "pw", true)
	token := AuthCreateSession(db, "root")
	a := &AdminApp{db: db}
	mux := http.NewServeMux()
	a.registerSystemRoutes(mux)
	a.registerCostRoutes(mux)
	do := func(method, path, body string) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code >= 400 {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
	}
	knob := overridable(t, 1)[0]
	do("POST", "/api/settings", `{"session_days":7,"`+knob.Key+`":`+strconv.FormatFloat(knob.Default+1, 'g', -1, 64)+`}`)
	do("PUT", "/api/cost-rates", `{"search_per_call":0.02}`)

	got := map[string]bool{}
	for _, e := range activity.List(db, activity.Filter{Kind: activity.Settings}) {
		if e.User != "root" {
			t.Errorf("a setting change should name who made it: %+v", e)
		}
		got[e.Detail] = true
	}
	for _, want := range []string{
		"set session_days=7",
		"set " + knob.Key + "=" + strconv.FormatFloat(knob.Default+1, 'g', -1, 64),
		"set search_per_call=0.02",
	} {
		if !got[want] {
			t.Errorf("missing %q in %v", want, got)
		}
	}
}
