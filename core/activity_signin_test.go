package core

// Every sign-in, and every attempt that failed, goes in the activity log with
// where it came from. The server log already said so, which an administrator
// does not read; Admin › Activity does.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/activity"
)

func signInPost(user, pass, remote string) *http.Request {
	form := url.Values{"username": {user}, "password": {pass}}
	r := httptest.NewRequest(http.MethodPost, "http://gohort.test/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("User-Agent", "TestBrowser/1.0")
	r.RemoteAddr = remote
	return r
}

func TestEverySignInAndFailedAttemptIsLogged(t *testing.T) {
	db := loginFixture(t)
	lockoutMu.Lock()
	saved := lockouts
	lockouts = map[string]*lockoutEntry{}
	lockoutMu.Unlock()
	t.Cleanup(func() { lockoutMu.Lock(); lockouts = saved; lockoutMu.Unlock() })
	login := LoginHandler(db)

	login(httptest.NewRecorder(), signInPost("user-a", "wrong", "198.51.100.30:4000"))
	login(httptest.NewRecorder(), signInPost("nobody", "x", "198.51.100.31:4000"))
	AuthSetUser(db, "waiting", "pw-w-123", false)
	if u, ok := AuthGetUser(db, "waiting"); ok {
		u.Pending = true
		db.Set(AuthTable, "user:waiting", u)
	}
	login(httptest.NewRecorder(), signInPost("waiting", "pw-w-123", "198.51.100.32:4000"))
	w := httptest.NewRecorder()
	login(w, signInPost("user-a", "pw-a-123", "198.51.100.33:4000"))
	cookie := sessionCookie(w)
	if cookie == nil {
		t.Fatal("precondition: the good sign-in should succeed")
	}
	out := httptest.NewRequest(http.MethodPost, "http://gohort.test/logout", nil)
	out.AddCookie(cookie)
	LogoutHandler(db)(httptest.NewRecorder(), out)
	for i := 0; i <= maxAttempts(); i++ {
		login(httptest.NewRecorder(), signInPost("guessed", "x", "198.51.100.40:4000"))
	}

	events := activity.List(db, activity.Filter{})
	has := func(kind, user, detail, ip string) bool {
		for _, e := range events {
			if e.Kind == kind && e.User == user && strings.Contains(e.Detail, detail) && (ip == "" || e.IP == ip) {
				return true
			}
		}
		return false
	}
	for _, c := range []struct{ kind, user, detail, ip string }{
		{activity.SignInFailed, "user-a", "wrong username or password", "198.51.100.30"},
		{activity.SignInFailed, "nobody", "wrong username or password", "198.51.100.31"},
		{activity.SignInFailed, "waiting", "waiting for approval", "198.51.100.32"},
		{activity.SignIn, "user-a", "signed in", "198.51.100.33"},
		{activity.SignOut, "user-a", "signed out", ""},
		{activity.SignInFailed, "guessed", "locked out", "198.51.100.40"},
	} {
		if !has(c.kind, c.user, c.detail, c.ip) {
			t.Errorf("no %s event for %q (%q from %s) in %+v", c.kind, c.user, c.detail, c.ip, events)
		}
	}
	if events[0].Browser == "" {
		t.Error("the browser should be recorded")
	}
	for _, e := range events {
		if strings.Contains(e.Detail, "pw-") || strings.Contains(e.User, "pw-") {
			t.Fatalf("a password reached the log: %+v", e)
		}
	}
}
