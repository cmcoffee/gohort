package scribe

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// routed is the app's real routes, under its sub-mux.
func routed(T *Scribe) http.HandlerFunc {
	mux := http.NewServeMux()
	T.SetWebMux(mux, "")
	T.routes()
	return mux.ServeHTTP
}

// as serves r through the app's real routes, signed in as user: an account
// for them, granted Scribe, is made if the auth store has none (and the store itself, if the
// test set none up). The /scribe mount is taken off the path, as the
// server's mux does.
func as(t *testing.T, T *Scribe, user string, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	return serveAs(t, T, user, false, r)
}

// asAdmin is as for an administrator.
func asAdmin(t *testing.T, T *Scribe, user string, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	return serveAs(t, T, user, true, r)
}

func serveAs(t *testing.T, T *Scribe, user string, admin bool, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	if AuthDB == nil || AuthDB() == nil {
		auth := &DBase{Store: kvlite.MemStore()}
		saved := AuthDB
		AuthDB = func() Database { return auth }
		t.Cleanup(func() { AuthDB = saved })
	}
	db := AuthDB()
	if _, ok := AuthGetUser(db, user); !ok {
		db.Set(AuthTable, "user:"+user, AuthUser{Username: user, Admin: admin, Apps: []string{T.WebPath()}})
	}
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(db, user)})
	r.URL.Path = strings.TrimPrefix(r.URL.Path, "/scribe")
	w := httptest.NewRecorder()
	routed(T)(w, r)
	return w
}
