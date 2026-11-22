package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/userprofile"
	"github.com/cmcoffee/gohort/core/desktopsignin"
)

// TestAPIKeyUserAcceptsBothEnvelopes: gohort's own clients send X-API-Key, but
// most third-party integrations send "Authorization: Bearer" and some can send
// nothing else — an OpenAI-compatible client library, a voice platform's
// custom-LLM config. Same token, different envelope; both must resolve.
func TestAPIKeyUserAcceptsBothEnvelopes(t *testing.T) {
	const good = "tok-good"
	prev := apiKeyValidators
	apiKeyValidators = nil
	RegisterAPIKeyValidator(func(k string) (string, bool) {
		if k == good {
			return "alice", true
		}
		return "", false
	})
	t.Cleanup(func() { apiKeyValidators = prev })

	cases := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{"x-api-key", map[string]string{"X-API-Key": good}, "alice"},
		{"bearer", map[string]string{"Authorization": "Bearer " + good}, "alice"},
		{"bearer lowercase scheme", map[string]string{"Authorization": "bearer " + good}, "alice"},
		{"bearer with padding", map[string]string{"Authorization": "Bearer   " + good + "  "}, "alice"},
		// X-API-Key wins when both are present — gohort's own clients keep
		// their path even behind a proxy that adds an Authorization header.
		{"both", map[string]string{"X-API-Key": good, "Authorization": "Bearer nope"}, "alice"},
		{"unknown token", map[string]string{"Authorization": "Bearer nope"}, ""},
		{"no credential", nil, ""},
		// A non-Bearer scheme is not a token — Basic auth must not be read as one.
		{"basic auth ignored", map[string]string{"Authorization": "Basic " + good}, ""},
		{"empty bearer", map[string]string{"Authorization": "Bearer "}, ""},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		if got := APIKeyUser(r); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// The desktop bridge publishes its tools straight into the LLM catalog, so the
// names it mints have to survive the same check llm.go applies to every catalog
// it builds. They did not: the prefix was "from_client." and a dot is outside
// the accepted class, so dropInvalidlyNamedTools removed the ENTIRE desktop
// surface from every request — silently, since dropping is per-tool and the
// rest of the catalog still worked.
func TestClientToolNamesSurviveTheLLMNameGuard(t *testing.T) {
	// Left: what a desktop announces. Right: what the model must see.
	for raw, want := range map[string]string{
		"contacts_lookup":            "from_client_contacts_lookup",
		"filesystem_read_local_file": "from_client_filesystem_read_local_file",
		// Legacy dotted announces (a desktop that hasn't been rebuilt) have to
		// keep working — the server sanitizes, it doesn't require an upgrade.
		"contacts.lookup":            "from_client_contacts_lookup",
		"filesystem.read_local_file": "from_client_filesystem_read_local_file",
	} {
		got := ClientToolName(raw)
		if got != want {
			t.Errorf("ClientToolName(%q) = %q, want %q", raw, got, want)
		}
		if !validLLMToolName(got) {
			t.Errorf("ClientToolName(%q) = %q, which the model APIs reject", raw, got)
		}
	}
}

// A name that sanitizes to nothing can't be addressed by the model, so it must
// not be offered at all rather than published as a bare prefix.
func TestClientToolNameRejectsUnusableInput(t *testing.T) {
	for _, raw := range []string{"", "   ", "...", "///"} {
		if got := ClientToolName(raw); got != "" {
			t.Errorf("ClientToolName(%q) = %q, want \"\"", raw, got)
		}
	}
}

// Agent allow-lists persisted under the dotted spelling are still on disk. The
// gates that ask "is this a client-bridge tool?" have to recognize both, or a
// saved entry stops resolving and self-heal strips it.
func TestIsClientToolNameAcceptsBothSpellings(t *testing.T) {
	for _, name := range []string{
		"from_client_contacts_lookup",
		"from_client.contacts.lookup",
	} {
		if !IsClientToolName(name) {
			t.Errorf("IsClientToolName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "fetch_url", "from_clientele_tool"} {
		if IsClientToolName(name) {
			t.Errorf("IsClientToolName(%q) = true, want false", name)
		}
	}
}

// A desktop that upgrades announces "contacts_lookup" while the persisted set
// still holds "contacts.lookup" from its last run. Both sanitize to one exposed
// name, and the catalog must carry it ONCE — a duplicate name in the tool list
// is its own provider error.
func TestLocalToolsForUserDedupsAcrossTheRename(t *testing.T) {
	const user = "dedup-test-user"
	live := &desktopClient{user: user, tools: []DesktopToolDescriptor{
		{Name: "contacts_lookup", Desc: "new spelling"},
	}}
	stale := &desktopClient{user: user, tools: []DesktopToolDescriptor{
		{Name: "contacts.lookup", Desc: "old spelling"},
	}}
	desktopReg.add(stale)
	desktopReg.add(live) // newest-connected wins
	defer func() { desktopReg.remove(live); desktopReg.remove(stale) }()

	tools := LocalToolsForUser(user)
	if len(tools) != 1 {
		names := make([]string, 0, len(tools))
		for _, tl := range tools {
			names = append(names, tl.Name())
		}
		t.Fatalf("got %d tools %v, want 1", len(tools), names)
	}
	if got := tools[0].Name(); got != "from_client_contacts_lookup" {
		t.Errorf("exposed name = %q, want from_client_contacts_lookup", got)
	}
}

// A user profile shapes the desktop app: its start page or only its apps,
// under its name; the bridge lends only its tools; and /api/desktop/profile
// says which it is. A browser keeps the ordinary dashboard until the profile
// says it shapes a browser too. An administrator is never shaped by one.
func TestAUserProfileShapesTheDesktopAppAndABrowser(t *testing.T) {
	acctFixture(t)
	prevAuth := AuthDB
	AuthDB = func() Database { return RootDB }
	t.Cleanup(func() { AuthDB = prevAuth })
	AuthSetUser(RootDB, "erin", "pw", false)
	AuthSetUser(RootDB, "dana", "pw", false)
	AuthSetUser(RootDB, "root", "pw", true)
	for _, u := range []string{"erin", "dana"} {
		AuthSetUserApps(RootDB, u, []string{"/notes", "/apps"})
	}
	userprofile.SetStore(RootDB)
	t.Cleanup(func() { userprofile.SetStore(nil) })
	host := dashboardHost{apps: []dashApp{
		{name: "Notes", desc: "notes", path: "/notes", app: pinApp{}},
		{name: "My Apps", desc: "apps", path: "/apps", app: pinApp{}},
	}}
	page := func(user string, desktop bool) *httptest.ResponseRecorder {
		r := asUser(t, "/", user)
		if desktop {
			key, _ := MintDesktopKey(user)
			r.Header.Set("X-Gohort-Desktop-Client-Key", key)
		}
		w := httptest.NewRecorder()
		host.handleRoot(w, r)
		return w
	}
	if err := userprofile.Save(userprofile.Profile{ID: "desk", Name: "Front Desk", Users: []string{"erin", "root"}, Apps: []string{"/notes"}, Tools: []string{"contacts_search"}}); err != nil {
		t.Fatal(err)
	}
	if b := page("erin", false).Body.String(); !strings.Contains(b, "My Apps") || strings.Contains(b, "Front Desk") {
		t.Error("a browser got the profile's dashboard")
	}
	b := page("erin", true).Body.String()
	if strings.Contains(b, ">My Apps<") || !strings.Contains(b, "Notes") || !strings.Contains(b, "<title>Front Desk</title>") {
		t.Errorf("the desktop app's dashboard is not the profile's")
	}
	if b := page("root", true).Body.String(); strings.Contains(b, "<title>Front Desk</title>") {
		t.Error("an administrator's desktop app was shaped by a profile")
	}

	persistDesktopKnownTools("erin", []DesktopToolDescriptor{{Name: "filesystem_read"}, {Name: "contacts_search"}})
	var lent []string
	for _, tool := range LocalToolsForUser("erin") {
		lent = append(lent, tool.Name())
	}
	if len(lent) != 1 || !strings.Contains(lent[0], "contacts_search") {
		t.Errorf("the bridge lends %v, want only contacts_search", lent)
	}

	profileOf := func(user string) string {
		w := httptest.NewRecorder()
		handleDesktopProfile(w, asUser(t, "/api/desktop/profile", user))
		return strings.TrimSpace(w.Body.String())
	}
	if got := profileOf("erin"); !strings.Contains(got, `"name":"Front Desk"`) {
		t.Errorf("erin's profile: %s", got)
	}
	if got := profileOf("dana"); got != "{}" {
		t.Errorf("someone no profile names: %s", got)
	}

	userprofile.Save(userprofile.Profile{ID: "desk", Name: "Front Desk", Users: []string{"erin"}, StartPath: "/notes/"})
	if w := page("erin", true); w.Code != http.StatusFound || w.Header().Get("Location") != "/notes/" {
		t.Errorf("the desktop app did not open the start page: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := page("erin", false); w.Code != http.StatusOK {
		t.Errorf("a browser was sent to the start page: %d", w.Code)
	}

	// In a browser too.
	userprofile.Save(userprofile.Profile{ID: "desk", Name: "Front Desk", Users: []string{"erin"}, Apps: []string{"/notes"}, AlsoWeb: true})
	if b := page("erin", false).Body.String(); strings.Contains(b, ">My Apps<") || !strings.Contains(b, "<title>Front Desk</title>") {
		t.Error("a profile that shapes a browser too did not")
	}
	if b := page("dana", false).Body.String(); !strings.Contains(b, "My Apps") {
		t.Error("someone the profile does not name lost the dashboard")
	}
}

// What a user profile takes away holds wherever its people reach gohort
// from: a feature it removes is denied as Feature Access denies one, and
// their API keys stop working while it takes keys away, kept for when it
// stops. A team's profile reaches its members; an administrator's keys and
// features are never taken.
func TestAUserProfileTakesAwayWhatItRemoves(t *testing.T) {
	acctFixture(t)
	prevAuth := AuthDB
	AuthDB = func() Database { return RootDB }
	t.Cleanup(func() { AuthDB = prevAuth })
	AuthSetUser(RootDB, "erin", "pw", false)
	AuthSetUser(RootDB, "root", "pw", true)
	team, err := SaveAppGroup(RootDB, AppGroup{Name: "Front desk"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { DeleteAppGroup(RootDB, team.ID) }) // teams are cached past the test
	AuthSetUserGroups(RootDB, "erin", []string{team.ID})
	userprofile.SetStore(RootDB)
	t.Cleanup(func() { userprofile.SetStore(nil) })
	RegisterShareableFeature(ShareableFeature{Key: "unit_profile_feat", Label: "Unit"})

	erinKey := MintAccountToken("erin", "laptop")
	rootKey := MintAccountToken("root", "laptop")
	if u, ok := lookupAccountTokenOwner(erinKey.Token); !ok || u != "erin" || !FeatureAllowedForUser(RootDB, "unit_profile_feat", "erin") {
		t.Fatalf("before any profile: %q %v", u, ok)
	}
	if err := userprofile.Save(userprofile.Profile{ID: "desk", Name: "Desk", Teams: []string{team.ID},
		Removes: []string{userprofile.RemoveAPIKeys, userprofile.FeaturePrefix + "unit_profile_feat"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := lookupAccountTokenOwner(erinKey.Token); ok {
		t.Error("a key of someone whose profile takes keys away still works")
	}
	r := httptest.NewRequest("GET", "/v1/models", nil)
	r.Header.Set("X-API-Key", erinKey.Token)
	if AccountTokenFromRequest(r) != nil {
		t.Error("the key still reads off a request")
	}
	if FeatureAllowedForUser(RootDB, "unit_profile_feat", "erin") {
		t.Error("a feature the profile removes is still allowed")
	}
	if len(ListAccountTokens("erin")) != 1 {
		t.Error("the keys a profile takes away were deleted, not kept")
	}

	AuthSetUserGroups(RootDB, "root", []string{team.ID})
	if _, ok := lookupAccountTokenOwner(rootKey.Token); !ok || !FeatureAllowedForUser(RootDB, "unit_profile_feat", "root") {
		t.Error("an administrator on the team lost a key or a feature")
	}

	userprofile.Delete("desk")
	if u, ok := lookupAccountTokenOwner(erinKey.Token); !ok || u != "erin" {
		t.Error("the key did not come back with the profile gone")
	}
}

// Signing the desktop app in from the person's browser: the app starts with
// the hash of its secret; the page, signed out, keeps the code and sends them
// to sign in, and the dashboard brings them back; signed in, it shows the
// code to compare and confirms only from this site; the app's claim waits
// until then and returns a session for them, once.
func TestTheDesktopAppSignsInFromTheBrowser(t *testing.T) {
	acctFixture(t)
	prevAuth := AuthDB
	AuthDB = func() Database { return RootDB }
	t.Cleanup(func() { AuthDB = prevAuth })
	AuthSetUser(RootDB, "dana", "pw", false)

	post := func(h http.HandlerFunc, target, body string, r *http.Request) *httptest.ResponseRecorder {
		if r == nil {
			r = httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		}
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}
	w := post(handleDesktopSigninStart, "/api/desktop/signin/start", `{"challenge":"`+desktopsignin.Challenge("app-secret")+`"}`, nil)
	var started struct{ Code, Phrase, URL string }
	json.Unmarshal(w.Body.Bytes(), &started)
	if w.Code != http.StatusOK || started.Code == "" || !strings.HasPrefix(started.URL, "/desktop/signin?code=") {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	claim := func(verifier string) *httptest.ResponseRecorder {
		return post(handleDesktopSigninClaim, "/api/desktop/signin/claim", `{"code":"`+started.Code+`","verifier":"`+verifier+`"}`, nil)
	}
	if w := claim("app-secret"); w.Code != http.StatusAccepted {
		t.Errorf("before confirming the claim got %d, want 202", w.Code)
	}

	// Signed out: off to sign in, the code kept for the way back.
	w = httptest.NewRecorder()
	handleDesktopSigninPage(w, httptest.NewRequest(http.MethodGet, started.URL, nil))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/login" || !strings.Contains(w.Header().Get("Set-Cookie"), desktopSigninCookie+"="+started.Code) {
		t.Fatalf("signed out: %d %v", w.Code, w.Header())
	}
	r := asUser(t, "/", "dana")
	r.AddCookie(&http.Cookie{Name: desktopSigninCookie, Value: started.Code})
	w = httptest.NewRecorder()
	(&dashboardHost{}).handleRoot(w, r)
	if w.Code != http.StatusFound || w.Header().Get("Location") != started.URL {
		t.Errorf("after signing in the dashboard did not send them back: %d %s", w.Code, w.Header().Get("Location"))
	}

	w = httptest.NewRecorder()
	handleDesktopSigninPage(w, asUser(t, started.URL, "dana"))
	if b := w.Body.String(); !strings.Contains(b, started.Phrase) || !strings.Contains(b, "as dana") {
		t.Errorf("the page does not show the code to compare, for dana")
	}

	confirm := func(r *http.Request) int {
		r.Method = http.MethodPost
		w := httptest.NewRecorder()
		handleDesktopSigninConfirm(w, r)
		return w.Code
	}
	cross := asUser(t, "/desktop/signin/confirm?code="+started.Code, "dana")
	cross.Header.Set("Sec-Fetch-Site", "cross-site")
	if got := confirm(cross); got != http.StatusForbidden {
		t.Errorf("a page elsewhere confirmed: %d", got)
	}
	if got := confirm(httptest.NewRequest(http.MethodPost, "/desktop/signin/confirm?code="+started.Code, nil)); got != http.StatusUnauthorized {
		t.Errorf("confirmed signed out: %d", got)
	}
	if got := confirm(asUser(t, "/desktop/signin/confirm?code="+started.Code, "dana")); got != http.StatusOK {
		t.Fatalf("dana's confirm: %d", got)
	}

	w = claim("app-secret")
	var got struct {
		Cookie struct{ Name, Value string }
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != http.StatusOK || got.Cookie.Name != auth_cookie_name {
		t.Fatalf("claim: %d %s", w.Code, w.Body.String())
	}
	if user, ok := AuthValidateSession(RootDB, got.Cookie.Value); !ok || user != "dana" {
		t.Errorf("the session is %q %v, want dana's", user, ok)
	}
	if w := claim("app-secret"); w.Code != http.StatusGone {
		t.Errorf("a second claim got %d, want 410", w.Code)
	}
}
