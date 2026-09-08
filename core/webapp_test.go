package core

import (
	"context"
	"encoding/json"
	"github.com/cmcoffee/snugforge/kvlite"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The live ribbon is global and untenanted by design — every user sees every
// other user's running work. Its Label, though, is user content: the chat
// message, the research question, the debate topic. MaskedLabel is what keeps
// the ribbon's reach without the ribbon's disclosure.

func TestMaskedLabel_OwnerSeesTheRealThing(t *testing.T) {
	e := LiveEntry{Label: "how do I tell my boss I'm leaving", App: "Gohort", Owner: "craig"}
	if got := e.MaskedLabel("craig"); got != e.Label {
		t.Errorf("owner must see the real label, got %q", got)
	}
}

func TestMaskedLabel_EveryoneElseGetsGeneric(t *testing.T) {
	e := LiveEntry{Label: "how do I tell my boss I'm leaving", App: "Gohort", Owner: "craig"}
	for _, viewer := range []string{"dana", "", "admin"} {
		got := e.MaskedLabel(viewer)
		if got == e.Label {
			t.Errorf("viewer %q must not see the label", viewer)
		}
		if got != "Gohort · craig" {
			t.Errorf("viewer %q: got %q", viewer, got)
		}
	}
}

func TestMaskedLabel_UnknownOwnerFailsClosed(t *testing.T) {
	// A provider that hasn't been taught to set Owner must not leak by
	// default — including to a viewer who happens to be the real owner,
	// since nothing here can tell.
	e := LiveEntry{Label: "quarterly layoff modeling", App: "Deep Research"}
	for _, viewer := range []string{"craig", ""} {
		if got := e.MaskedLabel(viewer); got != "Deep Research · another user" {
			t.Errorf("viewer %q: got %q", viewer, got)
		}
	}
}

func TestMaskedLabel_NoOwnerMatchOnEmptyViewer(t *testing.T) {
	// Both empty must NOT count as a match — an unauthenticated viewer is
	// not the owner of an untagged session.
	e := LiveEntry{Label: "secret", App: "X"}
	if got := e.MaskedLabel(""); got == "secret" {
		t.Error("empty owner and empty viewer must not read as ownership")
	}
}

func TestMaskedLabel_PreservesTreeIndent(t *testing.T) {
	// The nested run view renders depth from the label's own prefix; masking
	// that away would flatten the tree.
	cases := []struct{ label, want string }{
		{"↳ sub-question about severance", "↳ Gohort · craig"},
		{"  ↳ deeper", "  ↳ Gohort · craig"},
		{"    ↳ deeper still", "    ↳ Gohort · craig"},
		{"top level", "Gohort · craig"},
	}
	for _, c := range cases {
		e := LiveEntry{Label: c.label, App: "Gohort", Owner: "craig"}
		if got := e.MaskedLabel("dana"); got != c.want {
			t.Errorf("label %q → %q, want %q", c.label, got, c.want)
		}
	}
}

func TestMaskedLabel_NoAppStillMasks(t *testing.T) {
	e := LiveEntry{Label: "sensitive", Owner: "craig"}
	if got := e.MaskedLabel("dana"); got != "Active session · craig" {
		t.Errorf("got %q", got)
	}
}

func TestSplitLiveIndent(t *testing.T) {
	cases := []struct{ in, indent, rest string }{
		{"plain", "", "plain"},
		{"↳ child", "↳ ", "child"},
		{"  ↳ grandchild", "  ↳ ", "grandchild"},
		{"   leading spaces only", "   ", "leading spaces only"},
		{"", "", ""},
	}
	for _, c := range cases {
		gi, gr := splitLiveIndent(c.in)
		if gi != c.indent || gr != c.rest {
			t.Errorf("splitLiveIndent(%q) = (%q, %q), want (%q, %q)", c.in, gi, gr, c.indent, c.rest)
		}
	}
}

func TestLiveEntry_OwnerNeverSerialized(t *testing.T) {
	// Owner is a server-side masking input, not something the browser needs.
	// If it ever ships in the JSON it becomes a second disclosure channel.
	if !jsonOmitsField(LiveEntry{Owner: "craig", Label: "x"}, "craig") {
		t.Error("Owner must not appear in the serialized entry")
	}
}

func TestSetOwner_NilSafe(t *testing.T) {
	// Register returns nil at the concurrency cap; chaining must not panic.
	var s *LiveSession[string]
	if got := s.SetOwner("craig"); got != nil {
		t.Error("SetOwner on nil must return nil")
	}
}

// jsonOmitsField reports whether v's JSON encoding excludes the given value.
func jsonOmitsField(v any, value string) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	return !strings.Contains(string(b), value)
}

// A session id is not a capability.
//
// Every /api/live payload carries the ids of every running session, because the
// live ribbon is global and untenanted on purpose. The events behind an id are
// not public in the same way: they are the research question asked, the debate
// topic, the command run on somebody's box. MayView is the check that keeps
// those two facts apart, and these tests pin it — including the parts that are
// deliberately unhelpful, like refusing an untagged session to everyone.
type ownEvent struct {
	Text string `json:"text"`
}

// ownershipMap builds a map holding one session per (id, owner) pair given.
func ownershipMap(t *testing.T, sessions map[string]string) *LiveSessionMap[ownEvent] {
	t.Helper()
	m := NewLiveSessionMap[ownEvent](0)
	for id, owner := range sessions {
		s := m.Register(id, "how do I tell my boss I am leaving", func() {})
		if owner != "" {
			s.SetOwner(owner)
		}
		m.AppendEvent(id, ownEvent{Text: "secret transcript"}, false)
	}
	return m
}

// withUsers points AuthDB at a store holding one user, so MayView takes the
// multi-tenant branch rather than the "auth disabled" one. Restores the
// previous hook on cleanup.
func withUsers(t *testing.T) {
	t.Helper()
	prev := AuthDB
	db := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(db, "craig", "pw", true)
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })
}

func TestMayViewOwnerOnly(t *testing.T) {
	withUsers(t)
	m := ownershipMap(t, map[string]string{"s1": "craig"})

	if !m.MayView("craig", "s1") {
		t.Error("the owner must be able to view their own session")
	}
	if m.MayView("dana", "s1") {
		t.Error("another user must not view it")
	}
	if m.MayView("", "s1") {
		t.Error("an unauthenticated viewer must not view it")
	}
}

func TestMayViewUntaggedSessionIsNobodys(t *testing.T) {
	// Fail closed, the direction MaskedLabel already chose: a provider that has
	// not been taught to call SetOwner must not hand a transcript to everyone
	// just because it named no one.
	withUsers(t)
	m := ownershipMap(t, map[string]string{"orphan": ""})

	for _, viewer := range []string{"craig", "dana", ""} {
		if m.MayView(viewer, "orphan") {
			t.Errorf("viewer %q reached an untagged session", viewer)
		}
	}
}

func TestMayViewMissingSessionAnswersLikeNotYours(t *testing.T) {
	// One answer for "no such session" and "not yours", so probing cannot tell
	// whether somebody else is running something.
	withUsers(t)
	m := ownershipMap(t, map[string]string{"s1": "craig"})

	if m.MayView("craig", "nope") {
		t.Error("a missing session must not be viewable")
	}
}

func TestMayViewSingleTenantDeploymentAllowsAll(t *testing.T) {
	// No users configured means no one to be protected from, which is what
	// AuthMiddleware and RequestIsAdmin already assume in that state.
	prev := AuthDB
	AuthDB = nil
	t.Cleanup(func() { AuthDB = prev })

	m := NewLiveSessionMap[ownEvent](0)
	m.Register("s1", "topic", func() {})
	if !m.MayView("", "s1") {
		t.Error("with auth disabled every session must stay reachable")
	}
}

func TestOwnerOfDistinguishesMissingFromUntagged(t *testing.T) {
	m := ownershipMap(t, map[string]string{"tagged": "craig", "orphan": ""})

	if owner, ok := m.OwnerOf("tagged"); !ok || owner != "craig" {
		t.Errorf("tagged session: got (%q, %v)", owner, ok)
	}
	if owner, ok := m.OwnerOf("orphan"); !ok || owner != "" {
		t.Errorf("untagged session must exist with an empty owner, got (%q, %v)", owner, ok)
	}
	if _, ok := m.OwnerOf("nope"); ok {
		t.Error("a missing session must report not-found")
	}
}

// --- the handlers ------------------------------------------------------------

// asUser builds a request carrying a valid session cookie for username.
func asUser(t *testing.T, target, username string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	if username != "" {
		token := AuthCreateSession(AuthDB(), username)
		t.Cleanup(func() { AuthDestroySession(AuthDB(), token) })
		r.AddCookie(&http.Cookie{Name: auth_cookie_name, Value: token})
	}
	return r
}

func TestHandleEventsRefusesAnotherUsersSession(t *testing.T) {
	withUsers(t)
	AuthSetUser(AuthDB(), "dana", "pw", false)
	m := ownershipMap(t, map[string]string{"s1": "craig"})
	h := m.HandleEvents()

	w := httptest.NewRecorder()
	h(w, asUser(t, "/api/events?id=s1", "dana"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("dana must get 404, got %d", w.Code)
	}
	if b := w.Body.String(); strings.Contains(b, "secret transcript") {
		t.Fatal("the transcript leaked in the refusal body")
	}

	w = httptest.NewRecorder()
	h(w, asUser(t, "/api/events?id=s1", "craig"))
	if w.Code != http.StatusOK {
		t.Fatalf("the owner must get their own events, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "secret transcript") {
		t.Error("the owner did not receive their own events")
	}
}

func TestHandleCancelRefusesAnotherUsersSession(t *testing.T) {
	withUsers(t)
	AuthSetUser(AuthDB(), "dana", "pw", false)

	m := NewLiveSessionMap[ownEvent](0)
	_, cancel := context.WithCancel(context.Background())
	cancelled := false
	m.Register("s1", "topic", func() { cancelled = true; cancel() }).SetOwner("craig")

	w := httptest.NewRecorder()
	m.HandleCancel("test")(w, asUser(t, "/api/cancel?id=s1", "dana"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("dana must get 404, got %d", w.Code)
	}
	if cancelled {
		t.Fatal("dana cancelled craig's run")
	}

	w = httptest.NewRecorder()
	m.HandleCancel("test")(w, asUser(t, "/api/cancel?id=s1", "craig"))
	if w.Code != http.StatusOK {
		t.Fatalf("the owner must be able to cancel, got %d", w.Code)
	}
	if !cancelled {
		t.Error("the owner's cancel did not take effect")
	}
}

func TestHandleLiveMasksOtherUsersLabels(t *testing.T) {
	// The per-app ribbon must not be a way around the masking the global
	// /api/live applies to the very same entries.
	withUsers(t)
	AuthSetUser(AuthDB(), "dana", "pw", false)
	m := ownershipMap(t, map[string]string{"s1": "craig"})

	w := httptest.NewRecorder()
	m.HandleLive()(w, asUser(t, "/api/live", "dana"))

	var entries []LiveEntry
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the entry should still be listed, just masked")
	}
	for _, e := range entries {
		if strings.Contains(e.Label, "boss") {
			t.Errorf("dana read craig's label: %q", e.Label)
		}
	}

	w = httptest.NewRecorder()
	m.HandleLive()(w, asUser(t, "/api/live", "craig"))
	_ = json.Unmarshal(w.Body.Bytes(), &entries)
	found := false
	for _, e := range entries {
		if strings.Contains(e.Label, "boss") {
			found = true
		}
	}
	if !found {
		t.Error("the owner must still see their own label")
	}
}

// --- app availability: the admin switch that takes an app off this deployment

func TestAppEnabledDefaultsOn(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}

	// Never touched → every app is on. An app added in a later build must
	// ship enabled, not invisible until somebody notices it missing.
	if !AppEnabled(db, "/servitor") {
		t.Error("an app with no stored record must read as enabled")
	}
	if got := DisabledApps(db); len(got) != 0 {
		t.Errorf("untouched deployment must disable nothing, got %v", got)
	}

	if err := SetAppEnabled(db, "/servitor", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if AppEnabled(db, "/servitor") {
		t.Error("switched off, must read as disabled")
	}
	if !AppEnabled(db, "/guides") {
		t.Error("disabling one app must not touch another")
	}

	// Every spelling of the same mount names the same app: callers hold the
	// bare name, the trailing slash, and full request paths.
	for _, spelling := range []string{"servitor", "/servitor/", "/servitor/api/x"} {
		if AppEnabled(db, spelling) {
			t.Errorf("%q names the disabled app, must read as disabled", spelling)
		}
	}

	if err := SetAppEnabled(db, "/servitor", true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	if !AppEnabled(db, "/servitor") {
		t.Error("switched back on, must read as enabled")
	}
	if got := DisabledApps(db); len(got) != 0 {
		t.Errorf("re-enabling must leave the disabled set empty, got %v", got)
	}
}

func TestAppEnabledAdminPanelCannotBeDisabled(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}

	// The only surface that can re-enable anything must not be reachable by
	// the switch — including by a hand-written record.
	if err := SetAppEnabled(db, "/admin", false); err == nil {
		t.Error("disabling the admin panel must be refused")
	}
	db.Set(WebTable, disabledAppsKey, []string{"/admin", "/guides"})
	if !AppEnabled(db, "/admin") {
		t.Error("a stored record naming /admin must still read as enabled")
	}
	if got := DisabledApps(db); len(got) != 1 || got[0] != "/guides" {
		t.Errorf("DisabledApps must drop /admin, got %v", got)
	}
}

func TestAppPathOf(t *testing.T) {
	cases := map[string]string{
		"/servitor":              "/servitor",
		"/servitor/":             "/servitor",
		"/servitor/api/terminal": "/servitor",
		"/":                      "",
		"":                       "",
		"/login":                 "",
		"/logout":                "",
		"/signup":                "",
		"/forgot":                "",
		"/reset":                 "",
		"/api/live":              "",
		"/api/access":            "",
		// The shared runtime every page loads. Treat it as an app and a
		// gated user's every page renders blank.
		"/_ui/ui.js":  "",
		"/_ui/ui.css": "",
	}
	for in, want := range cases {
		if got := appPathOf(in); got != want {
			t.Errorf("appPathOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAppAvailabilityMiddleware(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return db }
	defer func() { AuthDB = prev }()

	reached := false
	h := AppAvailabilityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	get := func(path, accept string) *httptest.ResponseRecorder {
		reached = false
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := get("/guides/", ""); rec.Code != http.StatusOK || !reached {
		t.Fatalf("enabled app must pass through, got %d reached=%v", rec.Code, reached)
	}

	if err := SetAppEnabled(db, "/guides", false); err != nil {
		t.Fatalf("disable: %v", err)
	}

	// 503, not 404: the app exists and will answer again the moment the
	// switch goes back — a 404 sends the admin who just flipped it hunting
	// for a routing bug that isn't there.
	rec := get("/guides/", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("disabled app page = %d, want 503", rec.Code)
	}
	if reached {
		t.Error("disabled app must not reach its handler")
	}
	if rec := get("/guides/api/list", "application/json"); rec.Code != http.StatusServiceUnavailable || reached {
		t.Errorf("disabled app API = %d reached=%v, want 503 and no handler", rec.Code, reached)
	}

	// Everything that is not this app is untouched — including the paths the
	// gate must never claim.
	for _, path := range []string{"/", "/login", "/api/live", "/_ui/ui.js", "/knowledge/"} {
		if rec := get(path, ""); rec.Code != http.StatusOK || !reached {
			t.Errorf("%s = %d reached=%v, want 200 and a handler call", path, rec.Code, reached)
		}
	}
}

func TestAppEnabledHereFailsOpenWithoutAuthDB(t *testing.T) {
	prev := AuthDB
	AuthDB = nil
	defer func() { AuthDB = prev }()
	if !AppEnabledHere("/guides") {
		t.Error("no auth database wired: an app that cannot be switched off has not been")
	}
}
