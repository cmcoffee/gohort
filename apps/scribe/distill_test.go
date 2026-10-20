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

// A guide is distilled into another: what the receiving guide's brief says
// belongs there is filed for its Curator, the source is unchanged, and a
// receiving guide that is not the person's own, or is the source, is refused.
func TestAGuideIsDistilledIntoAnother(t *testing.T) {
	fake := &FakeLLM{Turns: []FakeTurn{{Content: `{"findings":[
		{"topic":"Timeouts mean the dependency is down","content":"A timeout from a service usually means a dependency it calls is unreachable, not that the service is slow."},
		{"topic":"Restart in order","content":"Restart dependencies before the services that call them."}]}`}}}
	T := &Scribe{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}, LLM: fake}}
	RegisterDocumentTarget(&guideTarget{app: T})
	udb := UserDB(T.DB, "alice")
	saveGuide(udb, Guide{ID: "g-src", Title: "Billing outage runbook", Owner: "alice", Sections: []Section{
		{ID: "a", Title: "Timeouts", Markdown: "On 3 March billing-api timed out because ledger-db was down.", Order: 1},
		{ID: "b", Title: "Recovery", Markdown: "We restarted ledger-db, then billing-api.", Order: 2}}})
	saveGuide(udb, Guide{ID: "g-dst", Title: "Platform basics", Owner: "alice", Brief: "General operating principles for every service, no incident specifics.",
		Sections: []Section{{ID: "c", Title: "Dependencies", Markdown: "Services call each other.", Order: 1}}})
	saveGuide(UserDB(T.DB, "bob"), Guide{ID: "g-bob", Title: "Bob's notes", Owner: "bob"})

	start := func(to string) int {
		w := httptest.NewRecorder()
		T.handleDistill(w, httptest.NewRequest(http.MethodPost, "/scribe/distill?id=g-src", strings.NewReader(`{"to":"`+to+`","curate_now":false}`)), udb, "alice")
		return w.Code
	}
	if code := start("g-src"); code == http.StatusOK {
		t.Error("a guide was distilled into itself")
	}
	if code := start("g-bob"); code == http.StatusOK {
		t.Error("a guide was distilled into somebody else's")
	}
	if code := start("g-dst"); code != http.StatusOK {
		t.Fatalf("distill answered %d", code)
	}
	deadline := time.Now().Add(5 * time.Second)
	var j publishJob
	for {
		j, _ = currentPublishJob(distillJobPrefix + "g-src")
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
	// The model read the source, and was told what the receiving guide is for.
	if p := fake.Prompt(0); !strings.Contains(p, "ledger-db was down") || !strings.Contains(p, "no incident specifics") {
		t.Errorf("the distill was not given the source and the brief:\n%s", p)
	}
	if g, _ := loadGuide(udb, "g-src"); len(g.Sections) != 2 || g.Sections[0].Markdown != "On 3 March billing-api timed out because ledger-db was down." {
		t.Error("the source guide was changed")
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
