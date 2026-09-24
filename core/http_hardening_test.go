package core

// The HTTP layer's baseline: every body has a ceiling and every response a
// CSP, login cannot be driven from another site or used to test which names
// exist, the session cookie is Secure behind a TLS-terminating proxy, and a
// new password ends the sessions the old one opened.

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/gohort/core/netgate"
	"github.com/cmcoffee/snugforge/kvlite"
)

// zeroes is an endless body, so a test can send past the 64 MiB default
// without allocating it.
type zeroes struct{}

func (zeroes) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

func TestDashboardChainCapsBodiesAndSetsCSP(t *testing.T) {
	prev := AuthDB
	AuthDB = nil
	t.Cleanup(func() { AuthDB = prev })

	mux := http.NewServeMux()
	mux.HandleFunc("/api/echo", func(w http.ResponseWriter, r *http.Request) {
		_, err := io.Copy(io.Discard, r.Body)
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	h := dashboardChain(mux)

	// Streamed, so the cap is what stops it and not a declared length.
	over := io.LimitReader(zeroes{}, netgate.DefaultBodyLimit+1)
	r := httptest.NewRequest(http.MethodPost, "/api/echo", over)
	r.ContentLength = -1
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a body one byte past the default cap: status %d, want 413", w.Code)
	}

	r = httptest.NewRequest(http.MethodPost, "/api/echo", strings.NewReader(`{"ok":true}`))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("a small body: status %d, want 200", w.Code)
	}
	csp := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{"object-src 'none'", "base-uri 'self'", "frame-ancestors 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %s", csp, want)
		}
	}
	// Anything touching scripts or styles would blank the inline-heavy UI.
	for _, banned := range []string{"script-src", "style-src", "default-src"} {
		if strings.Contains(csp, banned) {
			t.Errorf("CSP %q restricts %s, which the UI's inline scripts and styles cannot survive", csp, banned)
		}
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
}

// loginFixture is an auth store with one account, installed as AuthDB.
func loginFixture(t *testing.T) Database {
	t.Helper()
	prevAuth := AuthDB
	db := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(db, "user-a", "pw-a-123", false)
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prevAuth })
	return db
}

// loginPost builds a login form post. remote and origin may be empty.
func loginPost(remote, origin, proto string) *http.Request {
	form := url.Values{"username": {"user-a"}, "password": {"pw-a-123"}}
	r := httptest.NewRequest(http.MethodPost, "http://gohort.test/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if remote != "" {
		r.RemoteAddr = remote
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if proto != "" {
		r.Header.Set("X-Forwarded-Proto", proto)
	}
	return r
}

func sessionCookie(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == auth_cookie_name && c.Value != "" {
			return c
		}
	}
	return nil
}

func TestLoginRefusesCrossOriginPost(t *testing.T) {
	db := loginFixture(t)
	login := LoginHandler(db)

	w := httptest.NewRecorder()
	login(w, loginPost("198.51.100.20:4000", "https://attacker.example", ""))
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-origin login post: status %d, want 403", w.Code)
	}
	if c := sessionCookie(w); c != nil {
		t.Error("a cross-origin login post was handed a session")
	}

	for _, origin := range []string{"http://gohort.test", ""} {
		w = httptest.NewRecorder()
		login(w, loginPost("198.51.100.21:4000", origin, ""))
		if w.Code != http.StatusFound || sessionCookie(w) == nil {
			t.Errorf("same-origin login (Origin %q): status %d, cookie %v", origin, w.Code, sessionCookie(w) != nil)
		}
	}
}

// An unknown name must cost what a wrong password costs. The margin is wide on
// purpose: without the dummy comparison the two differ by four orders of
// magnitude (a map miss against a bcrypt round), so a factor of four still
// catches the regression without being a flaky timing test.
func TestLoginTimingDoesNotRevealWhichUsersExist(t *testing.T) {
	db := loginFixture(t)
	AuthSetUser(db, "invited", "", false) // an invite: account, no password yet
	AuthCheckPassword(db, "nobody", "x")  // build the dummy hash outside the timing

	fastest := func(user string) time.Duration {
		best := time.Duration(1<<63 - 1)
		for i := 0; i < 3; i++ {
			start := time.Now()
			if AuthCheckPassword(db, user, "wrong-password") {
				t.Fatalf("%s accepted a wrong password", user)
			}
			if d := time.Since(start); d < best {
				best = d
			}
		}
		return best
	}
	wrongPassword := fastest("user-a")
	for _, user := range []string{"nobody", "invited"} {
		if d := fastest(user); d < wrongPassword/4 {
			t.Errorf("a failed login for %q took %s, a wrong password for a real account %s: the difference names which accounts exist", user, d, wrongPassword)
		}
	}
}

func TestSessionCookieSecureBehindTrustedProxy(t *testing.T) {
	db := loginFixture(t)
	login := LoginHandler(db)
	prev := netgate.TrustedProxiesFunc
	netgate.TrustedProxiesFunc = nil // loopback only
	t.Cleanup(func() { netgate.TrustedProxiesFunc = prev })

	cases := []struct {
		name, remote, proto string
		secure              bool
	}{
		{"trusted proxy terminated TLS", "127.0.0.1:5000", "https", true},
		{"trusted proxy over plain http", "127.0.0.1:5000", "http", false},
		{"direct client claims https", "203.0.113.9:4000", "https", false},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		login(w, loginPost(c.remote, "", c.proto))
		ck := sessionCookie(w)
		if ck == nil {
			t.Fatalf("%s: no session cookie (status %d)", c.name, w.Code)
		}
		if ck.Secure != c.secure {
			t.Errorf("%s: login cookie Secure = %v, want %v", c.name, ck.Secure, c.secure)
		}
	}

	// The sliding renewal re-stamps the cookie, and has to decide the same way.
	token := AuthCreateSession(db, "user-a")
	sess, _ := loadAuthSession(db, token)
	sess.Expires = time.Now().Add(time.Minute).Unix() // past halfway: renews
	saveAuthSession(db, token, sess)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	if _, ok := authValidateSessionSliding(db, w, r, token); !ok {
		t.Fatal("the session should validate")
	}
	if ck := sessionCookie(w); ck == nil || !ck.Secure {
		t.Errorf("the renewed cookie behind a TLS-terminating proxy is not Secure: %+v", ck)
	}
}

func TestPasswordChangeEndsOtherSessions(t *testing.T) {
	db := loginFixture(t)
	here := AuthCreateSession(db, "user-a")
	elsewhere := AuthCreateSession(db, "user-a")
	r := httptest.NewRequest(http.MethodPost, "/account/api/password", nil)
	r.AddCookie(&http.Cookie{Name: auth_cookie_name, Value: here})

	if !AuthChangePassword(db, "user-a", "pw-a-123", "pw-a-456", r) {
		t.Fatal("the change should succeed")
	}
	if _, ok := AuthValidateSession(db, here); !ok {
		t.Error("the session that made the change was signed out")
	}
	if _, ok := AuthValidateSession(db, elsewhere); ok {
		t.Error("a session opened with the old password survived the change")
	}

	// An admin reset (and the forgotten-password link) trusts no session.
	if !AuthAdminSetPassword(db, "user-a", "pw-a-789") {
		t.Fatal("the admin reset should succeed")
	}
	if _, ok := AuthValidateSession(db, here); ok {
		t.Error("a session survived an admin password reset")
	}

	// So does the admin user editor, which sets passwords through AuthSetUser.
	again := AuthCreateSession(db, "user-a")
	AuthSetUser(db, "user-a", "pw-a-000", false)
	if _, ok := AuthValidateSession(db, again); ok {
		t.Error("a session survived a password replaced through AuthSetUser")
	}
	// Changing only the admin flag is not a password change.
	kept := AuthCreateSession(db, "user-a")
	AuthSetUser(db, "user-a", "", true)
	if _, ok := AuthValidateSession(db, kept); !ok {
		t.Error("an edit that left the password alone signed the user out")
	}
}
