package customapps

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// routed is the app's real routes, under its sub-mux.
func routed(T *CustomApps) http.HandlerFunc {
	mux := http.NewServeMux()
	T.SetWebMux(mux, "")
	T.routes()
	return mux.ServeHTTP
}

// as serves r through the app's real routes, signed in as user: an account
// for them, holding the My Apps grant, is made if the auth store has none
// (and the store itself, if the test set none up). The /apps mount is taken
// off the path, as the server's mux does.
func as(t *testing.T, T *CustomApps, user string, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	if AuthDB == nil || AuthDB() == nil {
		auth := &DBase{Store: kvlite.MemStore()}
		saved := AuthDB
		AuthDB = func() Database { return auth }
		t.Cleanup(func() { AuthDB = saved })
	}
	db := AuthDB()
	if _, ok := AuthGetUser(db, user); !ok {
		db.Set(AuthTable, "user:"+user, AuthUser{Username: user, Apps: []string{"/apps"}})
	}
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(db, user)})
	r.URL.Path = strings.TrimPrefix(r.URL.Path, "/apps")
	w := httptest.NewRecorder()
	routed(T)(w, r)
	return w
}

// appAs serves r to spec's app, signed in as user. The spec is saved under
// its owner when the owner has no app at that slug, and anyone else is a
// person the owner shared it with by name.
func appAs(t *testing.T, T *CustomApps, spec AppSpec, user string, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	stored, ok := loadSpec(spec.Owner, spec.Slug)
	if !ok {
		stored = SaveAppSpec(spec)
	}
	if user != spec.Owner && !slices.Contains(stored.SharedWith, user) {
		stored.SharedWith = append(stored.SharedWith, user)
		SaveAppSpec(stored)
		SetSharedOwner(T.DB, sharedAppsIndex, spec.Slug, spec.Owner, true)
	}
	return as(t, T, user, r)
}
