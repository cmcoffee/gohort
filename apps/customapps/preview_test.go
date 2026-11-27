package customapps

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appdraft"
	"github.com/cmcoffee/gohort/tools/appscript"
)

// previewTestApp is alice's installed app wx, holding one record of hers,
// shared with bob, and her working copy of it, renamed.
func previewTestApp(t *testing.T) (T *CustomApps, installed, working AppSpec) {
	t.Helper()
	T = sharingTestApp(t)
	prev := hostApp
	hostApp = T
	t.Cleanup(func() { hostApp = prev })
	installed = SaveAppSpec(AppSpec{Owner: "alice", Slug: "wx", Name: "Weather", RecordKey: "id",
		Page: json.RawMessage(`{"title":"Weather","sections":[]}`)})
	T.recordBase(installed, "alice").Set(appscript.RecordsTable("wx"), "r1", map[string]any{"id": "r1", "city": "Oslo"})
	working = installed
	working.Name = "Weather v2"
	working.Page = json.RawMessage(`{"title":"Weather v2","sections":[]}`)
	return T, installed, working
}

// previewCookieFrom follows a preview's link and returns the cookie it set.
func previewCookieFrom(t *testing.T, T *CustomApps, user, link string) *http.Cookie {
	t.Helper()
	w := as(t, T, user, httptest.NewRequest(http.MethodGet, link, nil))
	if w.Code != http.StatusFound {
		t.Fatalf("the preview link: %d %s", w.Code, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == previewCookie {
			if c.Path != "/apps/wx/" || !c.HttpOnly {
				t.Errorf("the preview cookie reaches past its app or into scripts: %+v", c)
			}
			return c
		}
	}
	t.Fatal("the preview link set no cookie")
	return nil
}

func withCookie(r *http.Request, c *http.Cookie) *http.Request {
	if c != nil {
		r.AddCookie(c)
	}
	return r
}

// The working copy is the owner's to see, at the app's own address, with the
// way back; without the cookie, and for anyone else even with it, the app is
// the installed one.
func TestAPreviewIsTheEditorsAlone(t *testing.T) {
	T, installed, working := previewTestApp(t)
	link, err := StartAppPreview("alice", working, false)
	if err != nil {
		t.Fatal(err)
	}
	c := previewCookieFrom(t, T, "alice", link)
	page := as(t, T, "alice", withCookie(httptest.NewRequest(http.MethodGet, "/apps/wx/", nil), c)).Body.String()
	if !strings.Contains(page, "Weather v2") || !strings.Contains(page, "Your working copy, on a copy of your records") || !strings.Contains(page, "/apps/wx/_preview/stop") {
		t.Errorf("alice's preview: %s", page)
	}
	if page := as(t, T, "alice", httptest.NewRequest(http.MethodGet, "/apps/wx/", nil)).Body.String(); strings.Contains(page, "Weather v2") {
		t.Error("without the cookie alice saw the working copy")
	}
	appAs(t, T, installed, "bob", httptest.NewRequest(http.MethodGet, "/apps/wx/", nil))
	if page := as(t, T, "bob", withCookie(httptest.NewRequest(http.MethodGet, "/apps/wx/", nil), c)).Body.String(); strings.Contains(page, "Weather v2") || !strings.Contains(page, "Weather") {
		t.Errorf("bob, carrying alice's preview cookie, saw: %s", page)
	}
	if w := as(t, T, "bob", httptest.NewRequest(http.MethodGet, link, nil)); w.Code != http.StatusNotFound {
		t.Errorf("bob followed alice's preview link: %d", w.Code)
	}
	if _, err := StartAppPreview("bob", working, false); err == nil {
		t.Error("someone other than the owner started a preview")
	}
	// Leaving takes the cookie away.
	w := as(t, T, "alice", httptest.NewRequest(http.MethodGet, "/apps/wx/_preview/stop", nil))
	if w.Code != http.StatusFound || !strings.Contains(w.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Errorf("leaving the preview: %d %q", w.Code, w.Header().Get("Set-Cookie"))
	}
}

// On a copy of the records, the preview reads the owner's records and writes
// none of them; the parts of the app a copy cannot hold are refused.
func TestAPreviewWritesACopyOfTheRecords(t *testing.T) {
	T, installed, working := previewTestApp(t)
	link, _ := StartAppPreview("alice", working, false)
	c := previewCookieFrom(t, T, "alice", link)
	if body := as(t, T, "alice", withCookie(httptest.NewRequest(http.MethodGet, "/apps/wx/records", nil), c)).Body.String(); !strings.Contains(body, "Oslo") {
		t.Errorf("the preview did not read a copy of alice's records: %s", body)
	}
	w := as(t, T, "alice", withCookie(httptest.NewRequest(http.MethodPost, "/apps/wx/records", strings.NewReader(`{"id":"r2","city":"Rome"}`)), c))
	if w.Code != http.StatusOK {
		t.Fatalf("saving in the preview: %d %s", w.Code, w.Body.String())
	}
	var real map[string]any
	if T.recordBase(installed, "alice").Get(appscript.RecordsTable("wx"), "r2", &real) {
		t.Error("a record saved in the preview reached alice's real records")
	}
	if body := as(t, T, "alice", withCookie(httptest.NewRequest(http.MethodGet, "/apps/wx/records", nil), c)).Body.String(); !strings.Contains(body, "Rome") {
		t.Errorf("the preview lost what it saved: %s", body)
	}
	if w := as(t, T, "alice", withCookie(httptest.NewRequest(http.MethodGet, "/apps/wx/file", nil), c)); w.Code != http.StatusForbidden {
		t.Errorf("a part of the app a copy cannot hold: %d", w.Code)
	}
	// A refresh of the same preview keeps its address and its copy.
	if again, _ := StartAppPreview("alice", working, false); again != link {
		t.Errorf("a refresh moved the preview: %s, was %s", again, link)
	}
}

// On real records, by choice, the preview's saves are real.
func TestAPreviewOnRealRecordsWritesThem(t *testing.T) {
	T, installed, working := previewTestApp(t)
	link, _ := StartAppPreview("alice", working, true)
	c := previewCookieFrom(t, T, "alice", link)
	if page := as(t, T, "alice", withCookie(httptest.NewRequest(http.MethodGet, "/apps/wx/", nil), c)).Body.String(); !strings.Contains(page, "Your working copy, not installed") {
		t.Errorf("the banner on real records: %s", page)
	}
	as(t, T, "alice", withCookie(httptest.NewRequest(http.MethodPost, "/apps/wx/records", strings.NewReader(`{"id":"r3","city":"Lima"}`)), c))
	var real map[string]any
	if !T.recordBase(installed, "alice").Get(appscript.RecordsTable("wx"), "r3", &real) {
		t.Error("a preview on real records did not write them")
	}
	EndAppPreview("alice", "wx")
	if page := as(t, T, "alice", withCookie(httptest.NewRequest(http.MethodGet, "/apps/wx/", nil), c)).Body.String(); strings.Contains(page, "Weather v2") {
		t.Error("an ended preview was still served")
	}
}

// The app's shared collections (a leaderboard) are copied into the preview
// too, so its page reads what the installed app's does.
func TestAPreviewReadsACopyOfTheSharedCollections(t *testing.T) {
	T, installed, working := previewTestApp(t)
	installed.SharedCollections = []string{"scores"}
	SaveAppSpecAs(installed, AppSaveNoHistory)
	T.recordBase(installed, "alice").Set(appscript.SharedTable("wx", "scores"), "s1", map[string]any{"name": "ada", "score": 9})
	working.SharedCollections = []string{"scores"}
	link, _ := StartAppPreview("alice", working, false)
	c := previewCookieFrom(t, T, "alice", link)
	if body := as(t, T, "alice", withCookie(httptest.NewRequest(http.MethodGet, "/apps/wx/shared/scores", nil), c)).Body.String(); !strings.Contains(body, "ada") {
		t.Errorf("the preview's shared collection: %s", body)
	}
}

// A draft's preview (core/appdraft, the work in progress Builder tests): a
// maintainer changing the app previews their draft too, and nobody else
// does; a browser check carries the cookie the draft hook hands it, which
// shows the draft at the app's address; and a page check run on the preview
// reads its own seeded records, not the preview's copy.
func TestADraftsPreviewServesItsEditorAndItsChecks(t *testing.T) {
	T, installed, working := previewTestApp(t)
	installed.Maintainers = []string{"bob"}
	installed = SaveAppSpec(installed)
	working.Maintainers = installed.Maintainers
	if _, err := StartAppPreview("bob", working, false); err != nil {
		t.Errorf("a maintainer could not preview their draft: %v", err)
	}
	if _, err := StartAppPreview("carol", working, false); err == nil {
		t.Error("someone who neither owns nor maintains the app previewed it")
	}

	c, ok := appdraft.PreviewCookie("alice", working)
	if !ok || c.Name != previewCookie || c.Path != "/apps/wx/" || !c.HTTPOnly {
		t.Fatalf("the draft's check cookie: %+v %v", c, ok)
	}
	cookie := &http.Cookie{Name: c.Name, Value: c.Value}
	if page := as(t, T, "alice", withCookie(httptest.NewRequest(http.MethodGet, "/apps/wx/", nil), cookie)).Body.String(); !strings.Contains(page, "Weather v2") {
		t.Errorf("the check cookie did not show the draft: %s", page)
	}
	if link := appdraft.PreviewLink("alice", working); !strings.Contains(link, "/apps/wx/_preview?token="+c.Value) {
		t.Errorf("the preview link for a reply: %q", link)
	}

	token, end := appscript.BeginCheckScratch("alice", "wx", "id", []map[string]any{{"id": "seed", "city": "Lima"}}, nil)
	defer end()
	r := withCookie(httptest.NewRequest(http.MethodGet, "/apps/wx/records", nil), cookie)
	r.AddCookie(&http.Cookie{Name: appscript.CheckScratchCookie, Value: token})
	body := as(t, T, "alice", r).Body.String()
	if !strings.Contains(body, "Lima") || strings.Contains(body, "Oslo") {
		t.Errorf("a page check on the preview did not read its own records: %s", body)
	}

	appdraft.PreviewEnd("alice", "wx")
	if page := as(t, T, "alice", withCookie(httptest.NewRequest(http.MethodGet, "/apps/wx/", nil), cookie)).Body.String(); strings.Contains(page, "Weather v2") {
		t.Error("an ended preview still shows the draft")
	}
}

// A preview made for one load (an assistant looking at its working copy) sits
// beside the person's own: it neither replaces nor ends theirs, even one on
// real records, is not counted as theirs, writes only its own copy of the
// records, and once ended serves nothing.
func TestACheckPreviewLeavesThePersonsPreviewAlone(t *testing.T) {
	T, installed, working := previewTestApp(t)
	link, err := StartAppPreview("alice", working, true)
	if err != nil {
		t.Fatal(err)
	}
	mine := previewCookieFrom(t, T, "alice", link)
	serves := func(c *http.Cookie) bool {
		return strings.Contains(as(t, T, "alice", withCookie(httptest.NewRequest(http.MethodGet, "/apps/wx/", nil), c)).Body.String(), "Weather v2")
	}
	load := func() (*http.Cookie, func()) {
		c, end, err := CheckPreview("alice", working, true)
		if err != nil {
			t.Fatal(err)
		}
		if c.Path != "/apps/wx/" || !c.HTTPOnly {
			t.Errorf("the load's cookie: %+v", c)
		}
		return &http.Cookie{Name: c.Name, Value: c.Value, Path: c.Path}, end
	}
	if _, _, err := CheckPreview("bob", working, true); err == nil {
		t.Error("someone other than the owner got a preview")
	}

	c, end := load()
	if again, _ := StartAppPreview("alice", working, true); again != link {
		t.Errorf("the load's preview moved the person's: %s, was %s", again, link)
	}
	w := as(t, T, "alice", withCookie(httptest.NewRequest(http.MethodPost, "/apps/wx/records", strings.NewReader(`{"id":"r3","city":"Lima"}`)), c))
	if w.Code != http.StatusOK {
		t.Fatalf("saving in the load's preview: %d %s", w.Code, w.Body.String())
	}
	var real map[string]any
	if T.recordBase(installed, "alice").Get(appscript.RecordsTable("wx"), "r3", &real) {
		t.Error("the load's preview wrote alice's real records")
	}
	end()
	if serves(c) {
		t.Error("an ended load's preview still served the working copy")
	}
	if !serves(mine) {
		t.Error("ending the load's preview ended the person's")
	}

	c, end = load()
	defer end()
	EndAppPreview("alice", "wx")
	if !serves(c) || HasAppPreview("alice", "wx") {
		t.Error("ending the person's preview reached the load's, or the load's counts as theirs")
	}
}
