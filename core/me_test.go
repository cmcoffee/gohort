package core

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/gohort/core/gettingstarted"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/snugforge/kvlite"
)

// meFixture is a deployment with one account, behind the real middleware,
// and a browser signed in as it.
type meFixture struct {
	db  Database
	srv *httptest.Server
	b   *http.Client
}

func newMeFixture(t *testing.T) *meFixture {
	t.Helper()
	f := &meFixture{db: &DBase{Store: kvlite.MemStore()}}
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return f.db }
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })
	AuthSetUser(f.db, "pat.smith@example.test", "pw-pat-123", true)
	mux := http.NewServeMux()
	mux.HandleFunc(meNamePath, meNameHandler(f.db))
	mux.HandleFunc(meZonePath, meZoneHandler(f.db))
	mux.HandleFunc(meHideStartPath, meHideStartHandler(f.db))
	mux.HandleFunc("/signup", SignupHandler(f.db))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "page "+r.URL.Path) })
	f.srv = httptest.NewServer(AuthMiddleware(f.db, mux))
	t.Cleanup(f.srv.Close)
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(f.srv.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: auth_cookie_name, Value: AuthCreateSession(f.db, "pat.smith@example.test")}})
	f.b = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return f
}

func (f *meFixture) get(t *testing.T, path string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+path, nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	resp, err := f.b.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func (f *meFixture) post(t *testing.T, path, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", f.srv.URL)
	resp, err := f.b.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, string(out)
}

const mePat = "pat.smith@example.test"

// A name is optional: pages open without one, nothing asks first.
func TestAPageOpensWithoutAName(t *testing.T) {
	f := newMeFixture(t)
	if resp, body := f.get(t, "/orchestrate/?agent=a1"); resp.StatusCode != http.StatusOK || body != "page /orchestrate/" {
		t.Errorf("a page without a name: %d %q", resp.StatusCode, body)
	}
}

// The account page sets the name through /api/me/name, under the one rule,
// and a blank clears it, after which everyone reads the username alone.
func TestTheAccountPageSetsAndClearsTheName(t *testing.T) {
	f := newMeFixture(t)
	if _, body := f.get(t, meNamePath); !strings.Contains(body, `"display_name":""`) || !strings.Contains(body, `"username":"`+mePat+`"`) {
		t.Errorf("before: %s", body)
	}
	if resp, _ := f.post(t, meNamePath, `{"display_name":"  Pat   S. "}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("set: %d", resp.StatusCode)
	}
	if got := authDisplayName(f.db, mePat); got != "Pat S." {
		t.Errorf("stored %q", got)
	}
	if resp, _ := f.post(t, meNamePath, `{"display_name":"Pat\u0007"}`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a name the rule refuses: %d", resp.StatusCode)
	}
	if resp, _ := f.post(t, meNamePath, `{"display_name":"  "}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("clear: %d", resp.StatusCode)
	}
	if got := authDisplayName(f.db, mePat); got != "" {
		t.Errorf("a blank left the name %q", got)
	}
	if bar := dashboardAuthBar(mePat); !strings.Contains(bar, `<span class="auth-user">`+mePat+`</span>`) {
		t.Errorf("with the name cleared the dashboard reads: %s", bar)
	}
}

// Signing up may give a name, or not; a name given is held to the rule.
func TestSigningUpMayGiveAName(t *testing.T) {
	f := newMeFixture(t)
	prevSignup := AuthSignupAllowed
	AuthSignupAllowed = func() bool { return true }
	t.Cleanup(func() { AuthSignupAllowed = prevSignup })
	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	signup := func(email, name string) {
		form := url.Values{"display_name": {name}, "email": {email}, "password": {"pw-x-123"}, "confirm": {"pw-x-123"}}
		resp, err := anon.Post(f.srv.URL+"/signup", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	signup("lee@example.test", "")
	if u, ok := AuthGetUser(f.db, "lee@example.test"); !ok || u.DisplayName != "" {
		t.Errorf("without a name: %+v %v", u, ok)
	}
	signup("ana@example.test", " Ana ")
	if u, _ := AuthGetUser(f.db, "ana@example.test"); u.DisplayName != "Ana" {
		t.Errorf("with a name: %q", u.DisplayName)
	}
	signup("bad@example.test", "Bad\u0007")
	if _, ok := AuthGetUser(f.db, "bad@example.test"); ok {
		t.Error("an account was made with a name the rule refuses")
	}
}

// Pickers that name people show each as everyone else reads them: their name
// beside the username, the username alone while they have none.
func TestPickersNamePeopleBesideTheirUsername(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(db, "pat@example.test", "pw-pat-123", false)
	AuthSetUser(db, "lee@example.test", "pw-lee-123", false)
	if err := authSetDisplayName(db, "pat@example.test", "Pat"); err != nil {
		t.Fatal(err)
	}
	got := string(UserCandidatesJSON(db, ""))
	if !strings.Contains(got, `{"value":"pat@example.test","label":"Pat (pat@example.test)"}`) ||
		!strings.Contains(got, `{"value":"lee@example.test","label":"lee@example.test"}`) {
		t.Errorf("candidates: %s", got)
	}
}

// The dashboard shows someone their name beside their username, as everyone
// else reads them.
func TestTheDashboardShowsYourNameBesideYourUsername(t *testing.T) {
	f := newMeFixture(t)
	authSetDisplayName(f.db, mePat, "Pat")
	if bar := dashboardAuthBar(mePat); !strings.Contains(bar, `<span class="auth-user">Pat (`+mePat+`)</span>`) {
		t.Errorf("with a name: %s", bar)
	}
}

// An account that has not chosen a zone follows its browser: the first
// report sets it, a later one moves it and says so, and a zone chosen on the
// account page stays put whatever a browser reports. A zone the browser
// cannot name changes nothing.
func TestTheTimezoneFollowsTheBrowserUntilOneIsChosen(t *testing.T) {
	f := newMeFixture(t)
	prevLocal := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = prevLocal })
	zone := func() (string, bool) {
		u, _ := AuthGetUser(f.db, mePat)
		return u.Timezone, u.TimezoneFromBrowser
	}
	said := func() []string {
		var titles []string
		for _, n := range notices.List(RootDB, mePat) {
			titles = append(titles, n.Title)
		}
		return titles
	}
	if resp, _ := f.post(t, meZonePath, `{"zone":"America/New_York"}`); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("report: %d", resp.StatusCode)
	}
	if tz, fromBrowser := zone(); tz != "America/New_York" || !fromBrowser {
		t.Fatalf("after the first report: %q %v", tz, fromBrowser)
	}
	if got := said(); len(got) != 1 || got[0] != "Your timezone is now America/New_York" {
		t.Errorf("moving off the server's zone was not said: %v", got)
	}
	if n := notices.List(RootDB, mePat); len(n) == 1 && !strings.Contains(n[0].Body, "in place of the server's zone (UTC)") {
		t.Errorf("the notice: %s", n[0].Body)
	}
	f.post(t, meZonePath, `{"zone":"America/New_York"}`)
	if got := said(); len(got) != 1 {
		t.Errorf("the same zone again said something: %v", got)
	}
	f.post(t, meZonePath, `{"zone":"Not/AZone"}`)
	if tz, _ := zone(); tz != "America/New_York" {
		t.Errorf("a zone the browser cannot name moved it to %q", tz)
	}
	f.post(t, meZonePath, `{"zone":"Europe/London"}`)
	if tz, _ := zone(); tz != "Europe/London" || len(said()) != 2 {
		t.Errorf("travelling: %q, %v", tz, said())
	}
	AuthSetUserTimezone(f.db, mePat, "Asia/Tokyo")
	f.post(t, meZonePath, `{"zone":"Europe/Paris"}`)
	if tz, fromBrowser := zone(); tz != "Asia/Tokyo" || fromBrowser {
		t.Errorf("a chosen zone was moved: %q %v", tz, fromBrowser)
	}
	// The first report, matching the server's zone, sets it and says nothing,
	// though the server's zone from the system is named "Local".
	time.Local = time.FixedZone("Local", 0)
	AuthSetUser(f.db, "lee@example.test", "pw-lee-123", false)
	authFollowBrowserZone(f.db, "lee@example.test", "UTC")
	if u, _ := AuthGetUser(f.db, "lee@example.test"); u.Timezone != "UTC" || len(notices.List(RootDB, "lee@example.test")) != 0 {
		t.Errorf("the server's own zone: %q, %d notices", u.Timezone, len(notices.List(RootDB, "lee@example.test")))
	}
}

// The getting-started card lists the steps that apply to whoever is looking,
// done ones ticked, and goes once every step is done or they hide it.
func TestTheGettingStartedCardShowsWhatAppliesUntilDone(t *testing.T) {
	f := newMeFixture(t)
	only := false
	gettingstarted.Register(gettingstarted.Step{ID: "zz-test-only", Order: 99, Title: "Only for some", Link: "/x/",
		Applies: func(*http.Request, string) bool { return only },
		Done:    func(string) bool { return false }})
	t.Cleanup(func() {
		gettingstarted.Register(gettingstarted.Step{ID: "zz-test-only", Applies: func(*http.Request, string) bool { return false }, Done: func(string) bool { return true }})
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: auth_cookie_name, Value: AuthCreateSession(f.db, mePat)})
	card := gettingStartedCard(req)
	if !strings.Contains(card, `<a href="/account/">Tell us your name</a>`) || strings.Contains(card, "Only for some") {
		t.Fatalf("the card: %s", card)
	}
	only = true
	if card := gettingStartedCard(req); !strings.Contains(card, "Only for some") {
		t.Error("a step that applies is missing")
	}
	only = false
	authSetDisplayName(f.db, mePat, "Pat")
	if card := gettingStartedCard(req); card != "" {
		t.Errorf("with every step done the card stays: %s", card)
	}
	authSetDisplayName(f.db, mePat, "")
	if card := gettingStartedCard(req); card == "" {
		t.Fatal("with a step to do there is no card")
	}
	if resp, _ := f.post(t, meHideStartPath, ``); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("hide: %d", resp.StatusCode)
	}
	if card := gettingStartedCard(req); card != "" {
		t.Errorf("hidden, the card stays: %s", card)
	}
}
