package core

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/netgate"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Every sign-in, and every attempt that failed, goes in the activity log with
// where it came from. The server log already said so, which an administrator
// does not read; Admin › Activity does.

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

// How the deployment-wide API key may be presented.
//
// The key is a blanket authentication bypass, so where it travels matters: in
// a URL it reaches browser history, Referer headers on any outbound link, and
// the log of every proxy in between. The header has none of that, so the URL
// spelling is off unless a deployment says otherwise.

// keyAuthFixture wires a deployment key and returns the middleware in front of
// a handler that records whether it was reached.
func keyAuthFixture(t *testing.T, allowQuery bool) (http.Handler, *bool) {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}

	prevKey, prevAllow := AuthAPIKey, AuthAPIKeyAllowQuery
	AuthAPIKey = func() string { return "s3cret" }
	AuthAPIKeyAllowQuery = func() bool { return allowQuery }
	t.Cleanup(func() { AuthAPIKey, AuthAPIKeyAllowQuery = prevKey, prevAllow })

	// A configured user, so the middleware does not fall through its
	// "no users yet" pass-through and prove nothing. Written directly: this
	// test is about how a key may be presented, not about account creation.
	db.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})

	reached := new(bool)
	h := AuthMiddleware(db, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	}))
	return h, reached
}

func TestTheKeyIsAcceptedInItsHeader(t *testing.T) {
	h, reached := keyAuthFixture(t, false)
	r := httptest.NewRequest("GET", "/anything", nil)
	r.Header.Set("X-Gohort-Key", "s3cret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !*reached || w.Code != 200 {
		t.Fatalf("the header form was refused: %d %s", w.Code, w.Body.String())
	}
}

// The default. A caller still using the URL is told the whole answer: what was
// wrong, what to send instead, and the setting that puts it back.
func TestTheKeyInAURLIsRefusedAndSaysWhy(t *testing.T) {
	h, reached := keyAuthFixture(t, false)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/anything?key=s3cret", nil))

	if *reached {
		t.Fatal("the URL form was accepted while it is off")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"X-Gohort-Key", "?key=", "setup menu"} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, body)
		}
	}
	// It must never hand the key back in its own complaint.
	if strings.Contains(body, "s3cret") {
		t.Error("the refusal echoed the key")
	}
}

// A deployment that has said so keeps the old spelling working.
func TestTheURLFormWorksWhenTurnedOn(t *testing.T) {
	h, reached := keyAuthFixture(t, true)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/anything?key=s3cret", nil))
	if !*reached || w.Code != 200 {
		t.Fatalf("the URL form was refused while it is on: %d %s", w.Code, w.Body.String())
	}
}

// Unset reads as off. A deployment that has never configured this, or one
// whose setting failed to load, must not have the permissive answer.
func TestAnUnsetSettingReadsAsOff(t *testing.T) {
	h, reached := keyAuthFixture(t, false)
	prev := AuthAPIKeyAllowQuery
	AuthAPIKeyAllowQuery = nil
	t.Cleanup(func() { AuthAPIKeyAllowQuery = prev })

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/anything?key=s3cret", nil))
	if *reached {
		t.Fatal("an unset setting allowed the URL form")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// A WRONG key in a URL is answered the way a wrong key anywhere else is, not
// with the refusal above. Telling an attacker which spelling to stop guessing
// at is information the refusal should not give away.
func TestAWrongKeyInAURLIsNotToldAboutTheHeader(t *testing.T) {
	h, reached := keyAuthFixture(t, false)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/anything?key=wrong", nil))
	if *reached {
		t.Fatal("a wrong key was accepted")
	}
	if strings.Contains(w.Body.String(), "X-Gohort-Key") {
		t.Error("a wrong key was told how the right one should be presented")
	}
}

// The refusal takes away the bypass, not the session.
//
// A signed-in operator who follows a link carrying the key in its query still
// has a valid cookie. Answering them with a 401 about a credential they did not
// need is a lockout dressed as a security improvement, so the refusal only
// fires when nothing else would have let the request through.
func TestAValidSessionSurvivesTheKeyRefusal(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prevKey, prevAllow, prevAuth := AuthAPIKey, AuthAPIKeyAllowQuery, AuthDB
	AuthAPIKey = func() string { return "s3cret" }
	AuthAPIKeyAllowQuery = func() bool { return false }
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthAPIKey, AuthAPIKeyAllowQuery, AuthDB = prevKey, prevAllow, prevAuth })

	db.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	token := AuthCreateSession(db, "alice")

	reached := false
	h := AuthMiddleware(db, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	// Signed in AND carrying the key in the url. The assertion is that it is
	// not refused OVER THE KEY — whatever the per-app gate decides about a
	// made-up path afterwards is a different question and not this one.
	r := httptest.NewRequest("GET", "/anything?key=s3cret", nil)
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == http.StatusUnauthorized || strings.Contains(w.Body.String(), deploymentKeyHeader) {
		t.Fatalf("a valid session was refused over a credential it did not need: %d %s", w.Code, w.Body.String())
	}

	// No session, same url: still refused, because now the key IS the claim.
	_ = reached
	reached = false
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/anything?key=s3cret", nil))
	if reached || w.Code != http.StatusUnauthorized {
		t.Fatalf("the bypass survived without a session: reached=%v status=%d", reached, w.Code)
	}
}

// Nothing a URL carries reaches an auth page as markup: a reset link's token,
// an app path in "access denied" or "app unavailable".
func TestAuthPagesDoNotReflectMarkup(t *testing.T) {
	const evil = `"><script>alert(1)</script>`
	db := &DBase{Store: kvlite.MemStore()}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		w := httptest.NewRecorder()
		ResetHandler(db)(w, httptest.NewRequest(method, "/reset?token="+strings.ReplaceAll(evil, " ", "%20"), nil))
		// The payload itself, not any <script>: every page's head carries the
		// framework's own (webui.StaleRestoreScript).
		if strings.Contains(w.Body.String(), "<script>alert(1)") {
			t.Errorf("%s /reset reflected the token: %s", method, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	serveResetPage(w, evil, "")
	if strings.Contains(w.Body.String(), "<script>alert(1)") || strings.Contains(w.Body.String(), `"><`) {
		t.Errorf("the reset form wrote the token raw: %s", w.Body.String())
	}

	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	w = httptest.NewRecorder()
	writeForbidden(w, r, `<svg onload=alert(1)>`)
	if strings.Contains(w.Body.String(), "<svg") {
		t.Errorf("access denied reflected the path: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	writeAppDisabled(w, r, `<svg onload=alert(1)>`)
	if strings.Contains(w.Body.String(), "<svg") {
		t.Errorf("app unavailable reflected the path: %s", w.Body.String())
	}
}

func TestPasswordHashingBcryptAndMigration(t *testing.T) {
	pw := "correct horse battery staple"

	// New scheme is bcrypt (self-identifying by the "$2" prefix).
	h := hashPassword(pw)
	if !strings.HasPrefix(h, "$2") {
		t.Fatalf("hashPassword should produce a bcrypt hash, got %q", h)
	}
	if ok, legacy := verifyPassword(h, pw); !ok || legacy {
		t.Errorf("bcrypt verify: got ok=%v legacy=%v, want true,false", ok, legacy)
	}
	if ok, _ := verifyPassword(h, "wrong"); ok {
		t.Errorf("bcrypt verify accepted a wrong password")
	}

	// Per-user salt: two hashes of the SAME password must differ.
	if hashPassword(pw) == h {
		t.Errorf("bcrypt hashes of the same password should differ (missing salt)")
	}

	// Legacy SHA-256 hashes still verify AND flag for upgrade-on-login.
	legacy := legacyPasswordHash(pw)
	if ok, needsUpgrade := verifyPassword(legacy, pw); !ok || !needsUpgrade {
		t.Errorf("legacy verify: got ok=%v legacy=%v, want true,true", ok, needsUpgrade)
	}
	if ok, _ := verifyPassword(legacy, "wrong"); ok {
		t.Errorf("legacy verify accepted a wrong password")
	}

	// An empty stored hash never verifies (guards against a blank PassHash bypass).
	if ok, _ := verifyPassword("", pw); ok {
		t.Errorf("empty stored hash should never verify")
	}

	// Passwords longer than bcrypt's 72-byte input cap: the SHA-256 pre-digest
	// means two long passwords that differ only past byte 72 must NOT collide.
	long1 := strings.Repeat("a", 100) + "1"
	long2 := strings.Repeat("a", 100) + "2"
	lh := hashPassword(long1)
	if ok, _ := verifyPassword(lh, long1); !ok {
		t.Errorf("a long password should verify against its own hash")
	}
	if ok, _ := verifyPassword(lh, long2); ok {
		t.Errorf("distinct long passwords must not collide (bcrypt 72-byte cap not handled)")
	}
}

// An admin changing a user's role or password keeps that user's own
// preferences: AuthSetUser used to rebuild the record from five fields.

func TestChangingARoleKeepsTheUsersPreferences(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(AuthTable, "user:alice", AuthUser{
		Username: "alice", PassHash: "old", Timezone: "America/Los_Angeles", NotifyForward: "phone",
		PrivateMode: true, InferredDisabled: true, Apps: []string{"/x"},
		PrivateModePerAgent: map[string]bool{"a1": true},
	})
	AuthSetUser(db, "alice", "", true)
	u, _ := AuthGetUser(db, "alice")
	if !u.Admin || u.Timezone != "America/Los_Angeles" || u.NotifyForward != "phone" || !u.PrivateMode ||
		!u.InferredDisabled || !u.PrivateModePerAgent["a1"] || u.PassHash != "old" || len(u.Apps) != 1 {
		t.Fatalf("promotion lost something: %+v", u)
	}
	AuthSetUser(db, "alice", "new-password-123", false)
	u, _ = AuthGetUser(db, "alice")
	if u.Admin || u.PassHash == "old" || u.Timezone != "America/Los_Angeles" {
		t.Fatalf("a password reset should change the hash and nothing else: %+v", u)
	}
}

func csrfReq(host, origin, referer string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "http://"+host+"/x", nil)
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if referer != "" {
		r.Header.Set("Referer", referer)
	}
	return r
}

func TestSameOriginRequest(t *testing.T) {
	cases := []struct {
		name            string
		host            string
		origin, referer string
		want            bool
	}{
		{"same origin", "app.example.com", "https://app.example.com", "", true},
		{"cross origin", "app.example.com", "https://evil.com", "", false},
		{"port mismatch is cross-origin", "app.example.com:8181", "https://app.example.com:9999", "", false},
		{"same host:port", "app.example.com:8181", "https://app.example.com:8181", "", true},
		{"referer fallback same", "app.example.com", "", "https://app.example.com/page", true},
		{"referer fallback cross", "app.example.com", "", "https://evil.com/page", false},
		{"origin wins over referer", "app.example.com", "https://app.example.com", "https://evil.com/x", true},
		{"neither present fails open", "app.example.com", "", "", true},
		{"malformed origin rejected", "app.example.com", "://bad", "", false},
		{"host compare is case-insensitive", "App.Example.com", "https://app.example.com", "", true},
		// gohort-desktop webview: loopback origin → remote server host. Allowed.
		{"desktop loopback 127.0.0.1", "gohort.example.com", "http://127.0.0.1:34567", "", true},
		{"desktop localhost", "gohort.example.com", "http://localhost:8080", "", true},
		{"desktop wails.localhost", "gohort.example.com", "http://wails.localhost", "", true},
		{"desktop ipv6 loopback", "gohort.example.com", "http://[::1]:9000", "", true},
		// A remote site can never present a loopback origin — normal cross-origin stays blocked.
		{"remote evil not loopback", "gohort.example.com", "https://evil.com", "", false},
	}
	for _, c := range cases {
		if got := SameOriginRequest(csrfReq(c.host, c.origin, c.referer)); got != c.want {
			t.Errorf("%s: SameOriginRequest = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestIsStateChangingMethod(t *testing.T) {
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if !IsStateChangingMethod(m) {
			t.Errorf("%s should be state-changing", m)
		}
	}
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if IsStateChangingMethod(m) {
			t.Errorf("%s should be treated as safe", m)
		}
	}
}

// The HTTP layer's baseline: every body has a ceiling and every response a
// CSP, login cannot be driven from another site or used to test which names
// exist, the session cookie is Secure behind a TLS-terminating proxy, and a
// new password ends the sessions the old one opened.

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

// Public means public in the router's reading too: an escaped slash or a dot
// segment cannot carry a request from a public prefix to somewhere else.
func TestAPublicPrefixCannotBeWalkedOutOf(t *testing.T) {
	RegisterPublicPath("/pubtest/")
	for _, target := range []string{"/pubtest%2f..%2fsecret", "/pubtest/%2e%2e/admin", "/pubtest/./x"} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.URL.Path, r.URL.RawPath = strings.ReplaceAll(strings.ReplaceAll(target, "%2f", "/"), "%2e", "."), target
		if isPublicRequest(r) {
			t.Errorf("%s passed as public", target)
		}
	}
	if !isPublicRequest(httptest.NewRequest(http.MethodGet, "/pubtest/hook", nil)) {
		t.Error("a plain public path was refused")
	}
}

// A reset token is stored under its hash; an older raw record still works and
// moves; a stored key presented as a token does not; setting the password
// ends every other outstanding link.
func TestResetTokensAreHashedAndEndWithAPasswordChange(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	tok := createResetToken(db, "alice")
	for _, k := range db.Keys(AuthResetTable) {
		if strings.Contains(k, tok) {
			t.Fatal("the token is a key name")
		}
		if _, ok := validateResetToken(db, k); ok {
			t.Error("a stored key worked as a token")
		}
	}
	if u, ok := validateResetToken(db, tok); !ok || u != "alice" {
		t.Fatal("a fresh token does not validate")
	}
	db.Set(AuthResetTable, "legacyraw", resetToken{Username: "alice", Expires: 1 << 40})
	if _, ok := validateResetToken(db, "legacyraw"); !ok {
		t.Fatal("a token issued before hashing stopped working")
	}
	AuthSetUser(db, "alice", "old-password", false)
	other := createResetToken(db, "alice")
	AuthAdminSetPassword(db, "alice", "new-password")
	if _, ok := validateResetToken(db, other); ok {
		t.Error("a reset link outlived the password being set")
	}
}

// With no accounts, only a Host a third-party site cannot point here is served.
func TestHostsThatCannotBeRebound(t *testing.T) {
	for _, h := range []string{"127.0.0.1:8080", "[::1]:8080", "localhost", "gohort:8080", "nas.local", "box.lan:9000", "10.0.0.5"} {
		if !hostCannotBeRebound(h) {
			t.Errorf("%s refused", h)
		}
	}
	for _, h := range []string{"attacker.example", "rebind.attacker.example:8080"} {
		if hostCannotBeRebound(h) {
			t.Errorf("%s accepted", h)
		}
	}
}

// A renewal does not write back a session that was revoked meanwhile.
func TestARenewalDoesNotReviveARevokedSession(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	if saveRenewedAuthSession(db, "gone-token", authSession{User: "alice", Expires: 1 << 40}) {
		t.Error("a renewal wrote a session that no longer exists")
	}
	var cur authSession
	if db.Get(AuthSessionTable, sessionStoreKey("gone-token"), &cur) {
		t.Error("the revoked session is back in the store")
	}
}

// The person doing the sharing is not one of the people they can share to.

// Every setter drops the owner from a recipient list, so offering their own
// name was offering a choice that does nothing: pick it, save, watch the chip
// vanish. The small version of a picker that stores names and changes nothing.
func TestTheSharerIsNotOfferedTheirOwnName(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	db.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	db.Set(AuthTable, "user:zed", AuthUser{Username: "zed", Pending: true})

	got := string(UserCandidatesJSON(db, "alice"))
	if strings.Contains(got, "alice") {
		t.Errorf("the sharer is offered their own name: %s", got)
	}
	if !strings.Contains(got, "bob") {
		t.Errorf("a colleague is missing: %s", got)
	}
	// Somebody who has not accepted their invitation is nobody to share with.
	if strings.Contains(got, "zed") {
		t.Errorf("a pending user is offered: %s", got)
	}
	// A caller with nobody to exclude gets everybody — an admin granting on a
	// deployment resource may legitimately be one of the grantees.
	if all := string(UserCandidatesJSON(db, "")); !strings.Contains(all, "alice") {
		t.Errorf("an unfiltered listing dropped somebody: %s", all)
	}
}
