package servitor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// fakeGuides is a guide target with sources, standing in for Scribe.
type fakeGuides struct {
	refs     map[string][]string // guide id -> system ids
	titles   map[string]string
	appended map[string]int
}

func (f *fakeGuides) Kind() string  { return "guide" }
func (f *fakeGuides) Label() string { return "Guides" }
func (f *fakeGuides) List(user string) []DocItem {
	var out []DocItem
	for id, t := range f.titles {
		out = append(out, DocItem{ID: id, Title: t})
	}
	return out
}
func (f *fakeGuides) ListReferencing(user, kind, item string) []DocItem {
	var out []DocItem
	for id, refs := range f.refs {
		for _, r := range refs {
			if r == item {
				out = append(out, DocItem{ID: id, Title: f.titles[id]})
			}
		}
	}
	return out
}
func (f *fakeGuides) Append(_ context.Context, user, docID, newTitle, section, md string) (string, error) {
	if docID == "" {
		docID = fmt.Sprintf("g%d", len(f.titles)+1)
		f.titles[docID] = newTitle
	}
	f.appended[docID]++
	return docID, nil
}
func (f *fakeGuides) AttachReference(user, docID, kind, item string) error {
	f.refs[docID] = append(f.refs[docID], item)
	return nil
}

// A system could only be linked to a guide from the guide's Sources picker,
// so a system with no guide yet showed the ↗ Guide button and nowhere to send
// it. The button now offers the person's other guides, linking the system as
// it writes, and a new guide made for it. Only a system the person can reach
// is linked, and a guide not linked is still refused without link=true.
func TestAReplyCanLinkItsSystemToAGuide(t *testing.T) {
	twoUserAuth(t)
	app := &Servitor{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	UserDB(app.DB, "craig").Set(applianceTable, "box-1", Appliance{ID: "box-1", Name: "node-7", Owner: "craig"})
	f := &fakeGuides{refs: map[string][]string{}, titles: map[string]string{"g-a": "Runbook"}, appended: map[string]int{}}
	RegisterDocumentTarget(f)

	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		routed(app)(w, reqAs(t, http.MethodPost, "/api/push-to-guide", "craig", body))
		return w
	}

	w := httptest.NewRecorder()
	routed(app)(w, reqAs(t, http.MethodGet, "/api/push-to-guide?appliance_id=box-1", "craig", ""))
	var got struct{ Guides, Others []DocItem }
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Guides) != 0 || len(got.Others) != 1 || got.Others[0].ID != "g-a" {
		t.Fatalf("an unlinked guide is offered under others: %s", w.Body.String())
	}

	if w := post(`{"appliance_id":"box-1","guide_id":"g-a","text":"x"}`); w.Code != http.StatusBadRequest || f.appended["g-a"] != 0 {
		t.Fatalf("a guide not linked is refused without link: %d", w.Code)
	}
	if w := post(`{"appliance_id":"box-1","guide_id":"g-a","text":"x","link":true}`); w.Code != http.StatusOK {
		t.Fatalf("link=true links and writes: %d %s", w.Code, w.Body.String())
	}
	if len(f.refs["g-a"]) != 1 || f.refs["g-a"][0] != "box-1" || f.appended["g-a"] != 1 {
		t.Fatalf("the system is on the guide's sources and the reply in it: %v %v", f.refs, f.appended)
	}

	if w := post(`{"appliance_id":"box-1","new_title":"node-7 notes","text":"x"}`); w.Code != http.StatusOK {
		t.Fatalf("a new guide is made: %d %s", w.Code, w.Body.String())
	}
	var made string
	for id, title := range f.titles {
		if title == "node-7 notes" {
			made = id
		}
	}
	if made == "" || len(f.refs[made]) != 1 || f.refs[made][0] != "box-1" {
		t.Fatalf("the new guide has the system as its source: %v", f.refs)
	}

	if w := post(`{"appliance_id":"not-mine","guide_id":"g-a","text":"x","link":true}`); w.Code != http.StatusNotFound {
		t.Fatalf("a system the person cannot reach is never linked: %d", w.Code)
	}
	if w := post(`{"appliance_id":"box-1","guide_id":"g-other","text":"x","link":true}`); w.Code != http.StatusNotFound {
		t.Fatalf("only the person's own guides are linked: %d", w.Code)
	}
}

// The Guide button always opens its picker, even with one linked guide and no
// others: skipping it there left no way to start a new guide from the button.
func TestTheGuideButtonAlwaysOffersANewGuide(t *testing.T) {
	raw, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	start := strings.Index(src, "var gBtn = makeBtn('↗ Guide'")
	end := strings.Index(src, "row.appendChild(gBtn);")
	if start < 0 || end < start {
		t.Fatal("the Guide button code was not found")
	}
	btn := src[start:end]
	if strings.Contains(btn, "guides.length === 1") {
		t.Error("one linked guide must not skip the picker")
	}
	open := strings.Index(btn, "window.uiOpenSimpleModal({title: 'Add to which guide?'")
	if open < 0 || !strings.Contains(btn[open:], "choice('New guide…'") {
		t.Error("the picker must always end with New guide")
	}
}
