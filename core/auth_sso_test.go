package core

// Sign-in with Microsoft, end to end against a provider in the test: a
// browser with its own cookie jar, the real routes behind AuthMiddleware, and
// oidctest standing in for Entra. Nothing reaches Microsoft.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/oidc"
	"github.com/cmcoffee/gohort/core/oidc/oidctest"
	"github.com/cmcoffee/snugforge/kvlite"
)

const (
	ssoTestTenant = "11111111-2222-3333-4444-555555555555"
	ssoTestClient = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
)

type ssoFixture struct {
	db   *DBase
	fake *oidctest.Provider
	srv  *httptest.Server
}

func newSSOFixture(t *testing.T, newPeople string) *ssoFixture {
	t.Helper()
	f := &ssoFixture{db: &DBase{Store: kvlite.MemStore()}}
	prevDB, prevBase, prevIssuer := AuthDB, WebBaseURL, ssoIssuer
	AuthDB = func() Database { return f.db }
	t.Cleanup(func() { AuthDB, WebBaseURL, ssoIssuer = prevDB, prevBase, prevIssuer })
	f.fake = oidctest.New(t, ssoTestClient, ssoTestTenant)
	f.fake.ClientSecret = "s3cret"
	ssoIssuer = func(oidc.Settings) string { return f.fake.Issuer }

	mux := http.NewServeMux()
	mux.HandleFunc("/login", LoginHandler(f.db))
	mux.HandleFunc(ssoStartPath, ssoStartHandler(f.db))
	mux.HandleFunc(ssoCallbackPath, ssoCallbackHandler(f.db))
	mux.HandleFunc("/api/sso/connect", ssoConnectHandler(f.db))
	mux.HandleFunc("/api/sso/me", ssoMeHandler(f.db))
	mux.HandleFunc("/api/sso/unlink", ssoUnlinkHandler(f.db))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "home of "+AuthCurrentUser(r)) })
	mux.HandleFunc("/account/", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "account of "+AuthCurrentUser(r)) })
	f.srv = httptest.NewServer(AuthMiddleware(f.db, mux))
	t.Cleanup(f.srv.Close)
	WebBaseURL = func() string { return f.srv.URL }

	AuthSetUser(f.db, "root@example.test", "pw-root-123", true)
	oidc.SaveSecret(f.db, "s3cret")
	if err := oidc.SaveSettings(f.db, oidc.Settings{Enabled: true, Tenant: ssoTestTenant, ClientID: ssoTestClient,
		Credential: oidc.CredentialSecret, NewPeople: newPeople}); err != nil {
		t.Fatal(err)
	}
	return f
}

// browser is a person's browser: its own cookies, and it does not follow
// redirects, so each step can be looked at.
func (f *ssoFixture) browser() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// start presses the button: the provider's authorize URL.
func (f *ssoFixture) start(t *testing.T, b *http.Client) string {
	t.Helper()
	resp, err := b.Get(f.srv.URL + ssoStartPath)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("start answered %d", resp.StatusCode)
	}
	return resp.Header.Get("Location")
}

// back is the browser arriving at the callback the provider sent it to.
func (f *ssoFixture) back(t *testing.T, b *http.Client, callback string) (*http.Response, string) {
	t.Helper()
	if !strings.HasPrefix(callback, f.srv.URL+ssoCallbackPath+"?") {
		t.Fatalf("the provider sent the browser to %s", callback)
	}
	resp, err := b.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(body)
}

// signIn is the whole sign-in in one browser.
func (f *ssoFixture) signIn(t *testing.T, b *http.Client, u oidctest.User) (*http.Response, string) {
	t.Helper()
	return f.back(t, b, f.fake.Authorize(t, f.start(t, b), u))
}

// who is the account the browser is signed in as.
func (f *ssoFixture) who(t *testing.T, b *http.Client) string {
	t.Helper()
	resp, err := b.Get(f.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, _ := io.ReadAll(resp.Body)
	return strings.TrimPrefix(string(body), "home of ")
}

func (f *ssoFixture) identity(u oidctest.User) oidc.Identity {
	return oidc.Identity{Issuer: f.fake.Issuer, Subject: u.OID, PreferredUsername: u.PreferredUsername}
}

func (f *ssoFixture) activity(kind string) string {
	var out []string
	for _, e := range activity.List(f.db, activity.Filter{Kind: kind}) {
		out = append(out, e.User+": "+e.Detail)
	}
	return strings.Join(out, " | ")
}

var pat = oidctest.User{OID: "oid-pat", PreferredUsername: "pat@example.test", Name: "Pat"}

func TestALinkedIdentitySignsInItsAccount(t *testing.T) {
	f := newSSOFixture(t, oidc.NewPeopleRefuse)
	AuthSetUser(f.db, "patricia", "pw-pat-123", false)
	oidc.SetLink(f.db, f.identity(pat), "patricia", time.Now())
	b := f.browser()
	resp, _ := f.signIn(t, b, pat)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/" {
		t.Fatalf("callback answered %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got := f.who(t, b); got != "patricia" {
		t.Fatalf("signed in as %q, want the linked account, not one named for the identity", got)
	}
	if got := f.activity(activity.SignIn); !strings.Contains(got, "patricia: signed in with Microsoft (pat@example.test)") {
		t.Errorf("activity %q", got)
	}
	// The ID token was all it kept: the provider's access token was dropped.
	if strings.Contains(strings.Join(f.db.Keys(oidc.SettingsTable), ","), "token") {
		t.Error("a provider token was stored")
	}
}

func TestANewIdentityIsHandledAsTheSettingSays(t *testing.T) {
	t.Run("refuse", func(t *testing.T) {
		f := newSSOFixture(t, oidc.NewPeopleRefuse)
		b := f.browser()
		_, page := f.signIn(t, b, pat)
		if !strings.Contains(page, "No account here is connected to pat@example.test") || f.who(t, b) != "" {
			t.Fatalf("page %q", page)
		}
		if _, ok := AuthGetUser(f.db, "pat@example.test"); ok {
			t.Error("an account was made")
		}
	})
	t.Run("wait for approval", func(t *testing.T) {
		f := newSSOFixture(t, oidc.NewPeopleApproval)
		b := f.browser()
		_, page := f.signIn(t, b, pat)
		if !strings.Contains(page, "pending approval") || f.who(t, b) != "" {
			t.Fatalf("page %q", page)
		}
		u, ok := AuthGetUser(f.db, "pat@example.test")
		if !ok || !u.Pending || u.Admin || u.PassHash != "" {
			t.Fatalf("account %+v %v", u, ok)
		}
		if !strings.Contains(f.activity(activity.SignUp), "signed up with Microsoft; waiting for approval") {
			t.Error(f.activity(activity.SignUp))
		}
		// Approved, the same identity signs in.
		AuthApproveUser(f.db, "pat@example.test")
		b = f.browser()
		f.signIn(t, b, pat)
		if f.who(t, b) != "pat@example.test" {
			t.Error("the approved account did not sign in")
		}
	})
	t.Run("let in", func(t *testing.T) {
		f := newSSOFixture(t, oidc.NewPeopleLetIn)
		b := f.browser()
		f.signIn(t, b, pat)
		if f.who(t, b) != "pat@example.test" {
			t.Fatal("not signed in")
		}
		if AuthCheckPassword(f.db, "pat@example.test", "") {
			t.Error("the new account opens with an empty password")
		}
	})
	t.Run("a name that is taken is never matched", func(t *testing.T) {
		f := newSSOFixture(t, oidc.NewPeopleLetIn)
		AuthSetUser(f.db, "pat@example.test", "pw-pat-123", false)
		b := f.browser()
		_, page := f.signIn(t, b, pat)
		if !strings.Contains(page, "already exists here and is not connected") || f.who(t, b) != "" {
			t.Fatalf("signed in an existing account by its name: %q", page)
		}
		if _, ok := oidc.LinkedAccount(f.db, f.identity(pat)); ok {
			t.Error("the identity was linked to the account with its name")
		}
	})
}

func TestASignInIsRefusedWhenItIsNotThisBrowsersOrThisDirectorys(t *testing.T) {
	f := newSSOFixture(t, oidc.NewPeopleLetIn)
	t.Run("finished in another browser", func(t *testing.T) {
		mine, theirs := f.browser(), f.browser()
		callback := f.fake.Authorize(t, f.start(t, mine), pat)
		_, page := f.back(t, theirs, callback)
		if !strings.Contains(page, "started in another browser") || f.who(t, theirs) != "" {
			t.Fatalf("a link someone else started signed this browser in: %q", page)
		}
	})
	t.Run("a state used twice", func(t *testing.T) {
		b := f.browser()
		callback := f.fake.Authorize(t, f.start(t, b), pat)
		f.back(t, b, callback)
		other := f.browser()
		u, _ := url.Parse(callback)
		other.Jar.SetCookies(u, onlySSOCookie(b.Jar.Cookies(u)))
		if _, page := f.back(t, other, callback); !strings.Contains(page, "expired or was already used") {
			t.Errorf("replayed: %q", page)
		}
	})
	t.Run("an expired one", func(t *testing.T) {
		b := f.browser()
		auth := f.start(t, b)
		state := mustQuery(t, auth, "state")
		ssoPendingMu.Lock()
		p := ssoPendingBy[state]
		p.at = time.Now().Add(-ssoPendingLife - time.Minute)
		ssoPendingBy[state] = p
		ssoPendingMu.Unlock()
		if _, page := f.back(t, b, f.fake.Authorize(t, auth, pat)); !strings.Contains(page, "expired") {
			t.Errorf("an old sign-in finished: %q", page)
		}
	})
	t.Run("another directory", func(t *testing.T) {
		b := f.browser()
		u := pat
		u.OID, u.Tenant = "oid-outsider", "99999999-2222-3333-4444-555555555555"
		_, page := f.signIn(t, b, u)
		if f.who(t, b) != "" || !strings.Contains(page, "did not complete") {
			t.Fatalf("an outsider signed in: %q", page)
		}
		if !strings.Contains(f.activity(activity.SignInFailed), "Microsoft sign-in refused") {
			t.Error(f.activity(activity.SignInFailed))
		}
	})
	t.Run("the provider said no", func(t *testing.T) {
		b := f.browser()
		state := mustQuery(t, f.start(t, b), "state")
		_, page := f.back(t, b, f.srv.URL+ssoCallbackPath+"?"+url.Values{"state": {state}, "error": {"access_denied"}, "error_description": {"AADSTS65004: the person declined\r\nTrace ID: 0"}}.Encode())
		if !strings.Contains(page, "AADSTS65004: the person declined") || strings.Contains(page, "Trace ID") {
			t.Errorf("page %q", page)
		}
	})
	t.Run("a linked account waiting for approval", func(t *testing.T) {
		u := oidctest.User{OID: "oid-wait", PreferredUsername: "wait@example.test"}
		AuthSetUser(f.db, "waiting", "pw-wait-123", false)
		var rec AuthUser
		f.db.Get(AuthTable, "user:waiting", &rec)
		rec.Pending = true
		f.db.Set(AuthTable, "user:waiting", rec)
		oidc.SetLink(f.db, f.identity(u), "waiting", time.Now())
		b := f.browser()
		if _, page := f.signIn(t, b, u); !strings.Contains(page, "pending approval") || f.who(t, b) != "" {
			t.Errorf("a pending account signed in: %q", page)
		}
	})
}

// onlySSOCookie is the sign-in's browser cookie alone, without the session.
func onlySSOCookie(cs []*http.Cookie) []*http.Cookie {
	var out []*http.Cookie
	for _, c := range cs {
		if c.Name == ssoCookie {
			out = append(out, c)
		}
	}
	return out
}

func mustQuery(t *testing.T, raw, key string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil || u.Query().Get(key) == "" {
		t.Fatalf("no %s in %s", key, raw)
	}
	return u.Query().Get(key)
}

// passwordSignIn signs a browser in with a password, the way an existing
// account connects Microsoft sign-in.
func (f *ssoFixture) passwordSignIn(t *testing.T, b *http.Client, user, pw string) {
	t.Helper()
	resp, err := b.PostForm(f.srv.URL+"/login", url.Values{"username": {user}, "password": {pw}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if f.who(t, b) != user {
		t.Fatalf("password sign-in as %s failed: %d", user, resp.StatusCode)
	}
}

func (f *ssoFixture) connect(t *testing.T, b *http.Client) string {
	t.Helper()
	resp, err := b.Post(f.srv.URL+"/api/sso/connect", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("connect answered %d", resp.StatusCode)
	}
	return resp.Header.Get("Location")
}

func TestAnAccountConnectsItsOwnIdentityAndOnlyItsOwn(t *testing.T) {
	f := newSSOFixture(t, oidc.NewPeopleRefuse)
	AuthSetUser(f.db, "patricia", "pw-pat-123", false)
	AuthSetUser(f.db, "sam", "pw-sam-123", false)
	b := f.browser()
	f.passwordSignIn(t, b, "patricia", "pw-pat-123")
	resp, _ := f.back(t, b, f.fake.Authorize(t, f.connect(t, b), pat))
	if resp.Header.Get("Location") != "/account/" {
		t.Fatalf("connect ended at %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if l, ok := oidc.LinkedAccount(f.db, f.identity(pat)); !ok || l.Username != "patricia" {
		t.Fatalf("link %+v", l)
	}
	if !strings.Contains(f.activity(activity.Account), "patricia: connected Microsoft sign-in pat@example.test") {
		t.Error(f.activity(activity.Account))
	}
	var me struct {
		Available bool         `json:"available"`
		Links     []ssoLinkRow `json:"links"`
	}
	r, _ := b.Get(f.srv.URL + "/api/sso/me")
	json.NewDecoder(r.Body).Decode(&me)
	r.Body.Close()
	if !me.Available || len(me.Links) != 1 || me.Links[0].ShownAs != "pat@example.test" {
		t.Fatalf("me %+v", me)
	}

	// Signed in with Microsoft from now on.
	fresh := f.browser()
	f.signIn(t, fresh, pat)
	if f.who(t, fresh) != "patricia" {
		t.Error("the connected identity did not sign in its account")
	}

	// Sam cannot take it, nor finish a connect Patricia started.
	s := f.browser()
	f.passwordSignIn(t, s, "sam", "pw-sam-123")
	if _, page := f.back(t, s, f.fake.Authorize(t, f.connect(t, s), pat)); !strings.Contains(page, "already connected to another account") {
		t.Errorf("an identity moved accounts: %q", page)
	}
	other := oidctest.User{OID: "oid-other", PreferredUsername: "other@example.test"}
	callback := f.fake.Authorize(t, f.connect(t, b), other)
	u, _ := url.Parse(callback)
	s.Jar.SetCookies(u, onlySSOCookie(b.Jar.Cookies(u)))
	f.back(t, s, callback)
	if l, ok := oidc.LinkedAccount(f.db, f.identity(other)); ok {
		t.Errorf("a connect finished by another account linked %+v", l)
	}

	// Sam cannot remove Patricia's link; Patricia can.
	unlink := func(c *http.Client, key string) int {
		body, _ := json.Marshal(map[string]string{"key": key})
		r, _ := c.Post(f.srv.URL+"/api/sso/unlink", "application/json", strings.NewReader(string(body)))
		r.Body.Close()
		return r.StatusCode
	}
	if code := unlink(s, me.Links[0].Key); code != http.StatusNotFound {
		t.Errorf("another account's unlink answered %d", code)
	}
	if code := unlink(b, me.Links[0].Key); code != http.StatusOK {
		t.Fatalf("unlink answered %d", code)
	}
	if _, ok := oidc.LinkedAccount(f.db, f.identity(pat)); ok {
		t.Error("still linked")
	}
	if c := f.browser(); f.who(t, c) != "" {
		t.Error("a fresh browser is signed in")
	}
	// Not signed in, connect goes to the login page.
	anon := f.browser()
	r, _ = anon.Post(f.srv.URL+"/api/sso/connect", "application/x-www-form-urlencoded", nil)
	r.Body.Close()
	if r.StatusCode == http.StatusSeeOther && strings.HasPrefix(r.Header.Get("Location"), f.fake.Server.URL) {
		t.Error("a connect started with nobody signed in")
	}
}

func TestDeletingAnAccountDisconnectsItsIdentity(t *testing.T) {
	f := newSSOFixture(t, oidc.NewPeopleLetIn)
	AuthSetUser(f.db, "patricia", "pw-pat-123", false)
	oidc.SetLink(f.db, f.identity(pat), "patricia", time.Now())
	AuthDeleteUser(f.db, "patricia")
	if _, ok := oidc.LinkedAccount(f.db, f.identity(pat)); ok {
		t.Fatal("the link outlived its account")
	}
	// A new account later named patricia does not inherit the identity.
	AuthSetUser(f.db, "patricia", "pw-new-123", false)
	b := f.browser()
	f.signIn(t, b, pat)
	if got := f.who(t, b); got == "patricia" {
		t.Error("the identity signed in the new account with the old name")
	}
}

func TestTheLoginPageOffersMicrosoftOnlyWhenItCanWork(t *testing.T) {
	f := newSSOFixture(t, oidc.NewPeopleRefuse)
	page := func() string {
		r, _ := f.browser().Get(f.srv.URL + "/login")
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		return string(b)
	}
	if !strings.Contains(page(), `action="/login/sso"`) || !strings.Contains(page(), oidc.DefaultLabel) {
		t.Fatal("no button while on")
	}
	WebBaseURL = func() string { return "" }
	if strings.Contains(page(), `action="/login/sso"`) {
		t.Error("a button with no External URL to come back to")
	}
	if _, page := f.back(t, f.browser(), f.srv.URL+ssoCallbackPath+"?state=x&code=y"); !strings.Contains(page, "expired") {
		t.Errorf("callback %q", page)
	}
	WebBaseURL = func() string { return f.srv.URL }
	s := oidc.LoadSettings(f.db)
	s.Enabled, s.Label = false, "Sign in with <Contoso>"
	oidc.SaveSettings(f.db, s)
	if strings.Contains(page(), `action="/login/sso"`) {
		t.Error("a button while off")
	}
	r, _ := f.browser().Get(f.srv.URL + ssoStartPath)
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode == http.StatusSeeOther || !strings.Contains(string(b), "not turned on") {
		t.Errorf("start while off: %d %s", r.StatusCode, b)
	}
	s.Enabled = true
	oidc.SaveSettings(f.db, s)
	if p := page(); !strings.Contains(p, "Sign in with &lt;Contoso&gt;") {
		t.Error("the label is not escaped")
	}
}

// A link left to an account that no longer exists (removed some way that did
// not clear it) signs nobody in, and is dropped.
func TestALinkToAMissingAccountSignsNobodyIn(t *testing.T) {
	f := newSSOFixture(t, oidc.NewPeopleRefuse)
	oidc.SetLink(f.db, f.identity(pat), "ghost", time.Now())
	b := f.browser()
	_, page := f.signIn(t, b, pat)
	if got := f.who(t, b); got != "" {
		t.Fatalf("signed in as %q", got)
	}
	if !strings.Contains(page, "No account here is connected") {
		t.Errorf("page %q", page)
	}
	if _, ok := oidc.LinkedAccount(f.db, f.identity(pat)); ok {
		t.Error("the stale link was kept")
	}
}
