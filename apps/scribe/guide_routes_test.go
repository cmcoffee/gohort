package scribe

// What the guide routes change, read back from the store: a new guide's
// subtitle, the settings, a section saved, added and removed, the knowledge
// attached, the header removed, the open guide; that the whole-guide markdown
// goes only to someone who may edit it; and that an unknown guide's history
// is an empty list.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestTheGuideRoutesChangeWhatTheySay(t *testing.T) {
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prevRoot })
	T := &Scribe{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	udb := UserDB(T.DB, "alice")
	g := saveGuideRev(udb, Guide{ID: "g1", Title: "Runbook", Owner: "alice", ImageURL: "https://img.example/x.png", Sections: []Section{
		{ID: "s1", Title: "One", Markdown: "first", Order: 1}, {ID: "s2", Title: "Two", Markdown: "second", Order: 2}}}, "Created")
	g.applyShare("alice", guideShare{AllowedUsers: []string{"carol"}})
	saveGuide(udb, g)
	indexGuideShares(T.DB, "alice", g.ID, g.Shared, g.AllowedUsers)
	do := func(user, method, path, body string) (int, string) {
		t.Helper()
		w := as(t, T, user, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w.Code, w.Body.String()
	}
	ok := func(code int, body, what string) {
		t.Helper()
		if code >= 300 {
			t.Fatalf("%s: %d %s", what, code, body)
		}
	}
	stored := func() Guide {
		t.Helper()
		g, _ := loadGuide(udb, "g1")
		return g
	}

	code, body := do("alice", "POST", "/new", `{"title":"Fresh","subtitle":"a start"}`)
	ok(code, body, "new")
	var made struct{ ID string }
	json.Unmarshal([]byte(body), &made)
	if fresh, _ := loadGuide(udb, made.ID); fresh.Subtitle != "a start" {
		t.Errorf("the new guide's subtitle: %q", fresh.Subtitle)
	}

	ok2 := func(method, path, body, what string) { code, b := do("alice", method, path, body); ok(code, b, what) }
	ok2("POST", "/settings?id=g1", `{"title":"Runbook","private":true}`, "settings")
	if !stored().Private {
		t.Error("Private was not saved")
	}
	ok2("POST", "/section?guide=g1&section=s2", `{"title":"Two","markdown":"second, edited"}`, "section save")
	if s := stored().Sections; s[sectionIdx(stored(), "s2")].Markdown != "second, edited" {
		t.Errorf("the section's markdown: %+v", s)
	}
	ok2("POST", "/section/add?guide=g1", `{"title":"Three","markdown":"third"}`, "section add")
	if s := stored().sorted(); len(s) != 3 || s[2].Markdown != "third" {
		t.Errorf("the added section: %+v", s)
	}
	ok2("DELETE", "/section?guide=g1&section=s1", ``, "section remove")
	if sectionIdx(stored(), "s1") >= 0 {
		t.Error("the removed section is still there")
	}
	ok2("POST", "/collections?guide=g1", `{"collections":["c1"]}`, "collections")
	if c := stored().Collections; len(c) != 1 || c[0] != "c1" {
		t.Errorf("collections: %v", c)
	}
	ok2("DELETE", "/image?id=g1", ``, "image remove")
	if stored().ImageURL != "" {
		t.Error("the header is still set")
	}
	ok2("POST", "/chat/active", `{"id":"g1"}`, "set active")
	if activeGuideID(udb) != "g1" {
		t.Errorf("the open guide: %q", activeGuideID(udb))
	}

	_, body = do("alice", "GET", "/guide?id=g1", "")
	if !strings.Contains(body, `"markdown"`) {
		t.Error("the owner was not sent the guide to edit as one page")
	}
	_, body = do("carol", "GET", "/guide?id=g1", "")
	if strings.Contains(body, `"markdown"`) {
		t.Error("a reader was sent the guide to edit as one page")
	}
	if code, body := do("alice", "GET", "/revisions?id=nope", ""); code != http.StatusOK || strings.TrimSpace(body) != "[]" {
		t.Errorf("an unknown guide's history: %d %s", code, body)
	}
}
