package scribe

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Update from sources reads a guide Source set to Distill in full: what the
// receiving guide's brief says belongs there is filed for its Curator, the
// source is unchanged, and the page is told to ask the Author about the
// Sources left on Consult. A guide only consulted is not read this way.
func TestUpdateFromSourcesDistillsTheGuidesSetToDistill(t *testing.T) {
	fake := &FakeLLM{Turns: []FakeTurn{{Content: `{"findings":[
		{"topic":"Timeouts mean the dependency is down","content":"A timeout from a service usually means a dependency it calls is unreachable, not that the service is slow."},
		{"topic":"Restart in order","content":"Restart dependencies before the services that call them."}]}`}}}
	T := &Scribe{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}, LLM: fake}}
	RegisterDocumentTarget(&guideTarget{app: T})
	udb := UserDB(T.DB, "alice")
	saveGuide(udb, Guide{ID: "g-src", Title: "Billing outage runbook", Owner: "alice", Sections: []Section{
		{ID: "a", Title: "Timeouts", Markdown: "On 3 March billing-api timed out because ledger-db was down.", Order: 1},
		{ID: "b", Title: "Recovery", Markdown: "We restarted ledger-db, then billing-api.", Order: 2}}})
	saveGuide(udb, Guide{ID: "g-ref", Title: "Reference", Owner: "alice", Sections: []Section{{ID: "r", Title: "R", Markdown: "r", Order: 1}}})
	saveGuide(udb, Guide{ID: "g-dst", Title: "Platform basics", Owner: "alice", Brief: "General operating principles for every service, no incident specifics.",
		Sections:    []Section{{ID: "c", Title: "Dependencies", Markdown: "Services call each other.", Order: 1}},
		References:  []ReferenceSelection{{Kind: "guide", ItemID: "g-src"}, {Kind: "guide", ItemID: "g-ref"}, {Kind: "system", ItemID: "box"}},
		DistillFrom: []string{"g-src"}})

	w := httptest.NewRecorder()
	T.handleUpdateFromSources(w, httptest.NewRequest(http.MethodPost, "/scribe/sources/update?id=g-dst", nil), udb, "alice")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"distilling":["Billing outage runbook"]`) || !strings.Contains(w.Body.String(), `"consult":2`) {
		t.Fatalf("the Distill guide is read and two Sources are left for the Author: %d %s", w.Code, w.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	var j publishJob
	for {
		j, _ = currentPublishJob(followJobKey("g-src", "g-dst"))
		if j.Done || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !j.Done || !j.OK || !strings.Contains(j.Message, "2 pieces sent to Platform basics") {
		t.Fatalf("the distill ended %+v", j)
	}
	pending := listPendingFindings(udb)
	if len(pending) != 2 {
		t.Fatalf("%d findings filed, want 2", len(pending))
	}
	for _, f := range pending {
		if f.Document != "g-dst" || f.Origin.SourceKind != "guide" || f.Origin.ItemID != "g-src" {
			t.Errorf("a finding is not for the receiving guide, from the source: %+v", f)
		}
	}
	// The model read the whole source, and was told what the receiving guide is for.
	if p := fake.Prompt(0); !strings.Contains(p, "ledger-db was down") || !strings.Contains(p, "We restarted ledger-db") || !strings.Contains(p, "no incident specifics") {
		t.Errorf("the distill was not given the source and the brief:\n%s", p)
	}
	if g, _ := loadGuide(udb, "g-src"); len(g.Sections) != 2 || g.Sections[0].Markdown != "On 3 March billing-api timed out because ledger-db was down." {
		t.Error("the source guide was changed")
	}

	// Somebody else's guide is not theirs to update.
	w = httptest.NewRecorder()
	T.handleUpdateFromSources(w, httptest.NewRequest(http.MethodPost, "/scribe/sources/update?id=g-dst", nil), UserDB(T.DB, "bob"), "bob")
	if w.Code == http.StatusOK {
		t.Error("another person updated a guide that is not shared with them")
	}
}

// A long guide is read in parts of whole sections, and a reply wrapped in a
// fence or prose still reads.
func TestALongGuideIsDistilledInParts(t *testing.T) {
	var secs []Section
	for i := 0; i < 6; i++ {
		secs = append(secs, Section{ID: itoa(i), Title: "S" + itoa(i), Markdown: strings.Repeat("words ", 900), Order: i})
	}
	parts := distillParts(Guide{Sections: secs})
	if len(parts) < 2 {
		t.Fatalf("%d parts for a guide of %d chars", len(parts), 6*5400)
	}
	n := 0
	for _, p := range parts {
		n += len(p.titles)
		if len(p.text) > distillPartBudget+distillSectionMax {
			t.Errorf("a part is %d chars", len(p.text))
		}
	}
	if n != 6 {
		t.Errorf("sections across parts: %d, want 6", n)
	}
	got, err := parseDistill("Here you go:\n```json\n{\"findings\":[{\"topic\":\"t\",\"content\":\"c\"},{\"topic\":\"empty\",\"content\":\"\"}]}\n```")
	if err != nil || len(got) != 1 || got[0].Content != "c" {
		t.Errorf("parsed %v, %v", got, err)
	}
}
