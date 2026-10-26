package scribe

import (
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A guide that has another among its Sources set to Distill is handed, after
// each Curator pass that changed that one, only the sections the pass changed:
// new or rewritten against the revision from before the pass, not the whole
// guide again. One it only Consults is handed nothing.
func TestAFollowerGetsWhatAPassChanged(t *testing.T) {
	app := &Scribe{}
	app.DB = testStore(t)
	udb := UserDB(app.DB, "u")
	src := saveGuideRev(udb, Guide{ID: "raw", Owner: "u", Title: "Raw troubleshooting", Sections: []Section{
		{ID: "s1", Title: "Timeouts", Markdown: "old", Order: 1},
		{ID: "s2", Title: "Disk", Markdown: "same", Order: 2},
	}}, "seed")
	prior := latestRevisionID(udb, src.ID)
	src.Sections[0].Markdown = "new"
	src.Sections = append(src.Sections, Section{ID: "s3", Title: "Retries", Markdown: "added", Order: 3})
	saveGuideRev(udb, src, "curated")
	fromRaw := []ReferenceSelection{{Kind: "guide", ItemID: "raw"}}
	saveGuide(udb, Guide{ID: "logs", Owner: "u", Title: "Reading the logs", References: fromRaw, DistillFrom: []string{"raw"}, Brief: "Log lines only."})
	// A Source left on Consult is reference on call only; Distill set for a
	// guide that is no longer attached, or a Source of another kind, is never
	// a guide to follow.
	saveGuide(udb, Guide{ID: "ref", Owner: "u", Title: "Reference only", References: fromRaw, CurateOnReport: true})
	saveGuide(udb, Guide{ID: "gone", Owner: "u", Title: "Detached", DistillFrom: []string{"raw"}})
	saveGuide(udb, Guide{ID: "sys", Owner: "u", Title: "System notes", DistillFrom: []string{"raw"}, References: []ReferenceSelection{{Kind: "system", ItemID: "raw"}}})
	saveGuide(udb, Guide{ID: "other", Owner: "u", Title: "Unrelated"})

	run := CuratorRun{Entries: []CuratorEntry{
		{Kind: OutcomePlaced, GuideID: "raw", PriorRev: prior},
		{Kind: OutcomePlaced, GuideID: "raw", PriorRev: "a later one"},
		{Kind: OutcomeDiscarded, GuideID: "other"},
	}}
	got := followTargets(udb, "u", run)
	if len(got) != 1 || got[0].dst.ID != "logs" {
		t.Fatalf("one follower, for the guide the pass changed: %+v", got)
	}
	var titles []string
	for _, s := range got[0].changed.Sections {
		titles = append(titles, s.Title)
	}
	if strings.Join(titles, ",") != "Timeouts,Retries" {
		t.Errorf("only the rewritten and the new section, against the revision from before the pass: %v", titles)
	}
	run.Entries[0].Undone, run.Entries[1].Undone = true, true
	if len(followTargets(udb, "u", run)) != 0 {
		t.Error("an undone change sends nothing")
	}
}

// Two guides that would feed each other, directly or through others, feed
// neither; a chain is fine. The Sources picker refuses the setting that would
// close a loop, and a pass checks again before it distills.
func TestGuidesThatFeedEachOtherFeedNeither(t *testing.T) {
	app := &Scribe{}
	app.DB = testStore(t)
	udb := UserDB(app.DB, "u")
	src := func(id string) []ReferenceSelection { return []ReferenceSelection{{Kind: "guide", ItemID: id}} }
	saveGuide(udb, Guide{ID: "a", Owner: "u", Title: "A"})
	saveGuide(udb, Guide{ID: "b", Owner: "u", Title: "B", References: src("a"), DistillFrom: []string{"a"}})
	saveGuide(udb, Guide{ID: "c", Owner: "u", Title: "C", References: src("b"), DistillFrom: []string{"b"}})
	if followLoops(udb, "c", "b") || followLoops(udb, "b", "a") {
		t.Error("a chain is not a loop")
	}
	if !followLoops(udb, "a", "c") || !followLoops(udb, "a", "b") {
		t.Error("A distilling from B or C would close a loop")
	}

	refs := func(id, body string) *httptest.ResponseRecorder {
		w := as(t, app, "u", httptest.NewRequest("POST", "/references?guide="+id, strings.NewReader(body)))
		return w
	}
	if w := refs("a", `{"references":["guide:c"],"distill":["guide:c"]}`); w.Code != 400 || !strings.Contains(w.Body.String(), "set one of them back to Consult") {
		t.Fatalf("the picker refuses a Distill that closes a loop: %d %s", w.Code, w.Body.String())
	}
	if w := refs("a", `{"references":["guide:c"],"distill":[]}`); w.Code != 200 {
		t.Fatalf("Consult on the same guide is fine: %d %s", w.Code, w.Body.String())
	}

	// Closed behind the picker's back (two saves racing): a pass feeds nothing.
	saveGuide(udb, Guide{ID: "a", Owner: "u", Title: "A", References: src("c"), DistillFrom: []string{"c"}})
	run := CuratorRun{Entries: []CuratorEntry{{Kind: OutcomePlaced, GuideID: "a"}}}
	if got := followTargets(udb, "u", run); len(got) != 0 {
		t.Errorf("a change in a loop feeds nothing: %+v", got)
	}
}

// The Sources picker offers Distill on a guide's pill only, shows what is set,
// keeps Distill only for attached guides, and a detached guide takes its
// Distill with it. One guide can be distilled from while another is consulted.
func TestTheSourcesPickerSetsDistillPerGuide(t *testing.T) {
	app := &Scribe{}
	app.DB = testStore(t)
	udb := UserDB(app.DB, "u")
	RegisterReferenceSource(guideSource{app: app})
	saveGuide(udb, Guide{ID: "raw", Owner: "u", Title: "Raw"})
	saveGuide(udb, Guide{ID: "ref", Owner: "u", Title: "Reference"})
	saveGuide(udb, Guide{ID: "logs", Owner: "u", Title: "Logs"})
	post := func(body string) int {
		w := as(t, app, "u", httptest.NewRequest("POST", "/references?guide=logs", strings.NewReader(body)))
		return w.Code
	}
	if code := post(`{"references":["guide:raw","guide:ref"],"distill":["guide:raw","guide:nowhere","system:box"]}`); code != 200 {
		t.Fatalf("save: %d", code)
	}
	g, _ := loadGuide(udb, "logs")
	if strings.Join(g.DistillFrom, ",") != "raw" || !follows(g, "raw") || follows(g, "ref") {
		t.Fatalf("distill from one guide, consult the other: %+v", g.DistillFrom)
	}
	w := as(t, app, "u", httptest.NewRequest("GET", "/references?guide=logs", nil))
	body := w.Body.String()
	if !strings.Contains(body, `"distill":["guide:raw"]`) || !strings.Contains(body, `"can_distill":true`) {
		t.Errorf("the picker is told what is set and where Distill is offered: %s", body)
	}
	if post(`{"references":["guide:ref"]}`) != 200 {
		t.Fatal("save without the flag")
	}
	if g, _ := loadGuide(udb, "logs"); len(g.DistillFrom) != 0 {
		t.Errorf("detaching a guide takes its Distill with it: %+v", g.DistillFrom)
	}
}

// Two passes while a follow-on distill runs are merged, the later copy of a
// section winning, so the run that waits gets everything both changed.
func TestChangesWaitingForAFollowAreMerged(t *testing.T) {
	a := Guide{ID: "raw", Sections: []Section{{ID: "s1", Markdown: "one"}, {ID: "s2", Markdown: "two"}}}
	b := Guide{ID: "raw", Sections: []Section{{ID: "s2", Markdown: "two, again"}, {ID: "s3", Markdown: "three"}}}
	m := mergeChanged(a, b)
	if len(m.Sections) != 3 || m.Sections[1].Markdown != "two, again" {
		t.Errorf("got %+v", m.Sections)
	}
	if mergeChanged(Guide{}, b).Sections[0].ID != "s2" {
		t.Error("nothing waiting is just the new changes")
	}
}
