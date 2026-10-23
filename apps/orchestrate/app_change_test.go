package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A save says what it changed: the page, which actions, which data sources,
// and when the page is unchanged that it will look the same and where the
// change shows instead. A change to an action's script used to report
// "Updated", the person opened the page, saw nothing new, and read the change
// as not taken.
func TestASaveSaysWhatItChanged(t *testing.T) {
	page := []byte(`{"title":"x"}`)
	prior := AppSpec{Slug: "cases", Page: page,
		Actions:     []AppAction{{Name: "gather_enc", Script: "print(1)"}, {Name: "reset", Script: "print(2)"}},
		DataSources: []AppDataSource{{Name: "list", Script: "print('[]')"}}}

	saved := prior
	saved.Actions = []AppAction{{Name: "gather_enc", Script: "print(3)"}, {Name: "reset", Script: "print(2)"}}
	c := appChangeOf(&prior, saved)
	if c.page || len(c.dataSources) != 0 || len(c.actions) != 1 || c.actions[0] != "gather_enc" {
		t.Fatalf("one action changed: %+v", c)
	}
	s := c.summary("cases")
	for _, want := range []string{"What this save changed: action gather_enc.", "The page itself is unchanged, so it looks the same", "has NOT run since this change", `app_def(action=\"test\", id=\"cases\")`} {
		if !strings.Contains(s, strings.ReplaceAll(want, `\"`, `"`)) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}

	paged := prior
	paged.Page = []byte(`{"title":"y"}`)
	if c := appChangeOf(&prior, paged); !c.page || len(c.actions) != 0 || strings.Contains(c.summary("cases"), "unchanged") {
		t.Errorf("a page change says so, and does not say the page is unchanged: %+v", c)
	}
	if s := appChangeOf(&prior, prior).summary("cases"); !strings.Contains(s, "nothing the app shows or runs") {
		t.Errorf("an edit that changes nothing it runs says so: %s", s)
	}
	if c := appChangeOf(nil, prior); !c.page || len(c.actions) != 2 || len(c.dataSources) != 1 {
		t.Errorf("a new app has everything new: %+v", c)
	}
}

// A changed action holds the app back from "done" until it has run. The
// save's own check never runs an action, so the app stood ready with the
// changed one never run, and the Builder told the person it was done before
// testing it. The finish check now holds that reply; a clean test clears it,
// and only it: a test does not load the page, so it cannot clear a page
// failure the app also stands on.
func TestAChangedActionHoldsTheAppUntilItRuns(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	const sid = "sess-app"
	turn := &chatTurn{udb: db, session: &ChatSession{ID: sid}}

	turn.noteActionsUnrun("cases", []string{"gather_enc"})
	un := unverifiedTools(db, sid)
	if len(un) != 1 || un[0].Tool != "app:cases" || !strings.HasPrefix(un[0].Reason, appActionsUnrunPrefix) {
		t.Fatalf("the app stands not ready on its unrun action: %+v", un)
	}
	shown := ""
	notice, _ := buildGapsFinishCheck(nil, db, sid, &shown)("Done, the change is published.")
	if !strings.Contains(notice, "app cases is NOT ready") || !strings.Contains(notice, "gather_enc") {
		t.Errorf("a reply calling it done is held, naming the action:\n%s", notice)
	}
	turn.clearActionsUnrun("cases")
	if len(unverifiedTools(db, sid)) != 0 {
		t.Error("a clean test run clears it")
	}

	// A page failure stays: the action note is added beside it, and a test
	// does not clear the pair.
	turn.noteAppStanding("cases", false, "its page fails in a real browser: x is not defined")
	turn.noteActionsUnrun("cases", []string{"gather_enc"})
	un = unverifiedTools(db, sid)
	if len(un) != 1 || !strings.Contains(un[0].Reason, "page fails") || !strings.Contains(un[0].Reason, "gather_enc") {
		t.Fatalf("both reasons stand: %+v", un)
	}
	turn.clearActionsUnrun("cases")
	if len(unverifiedTools(db, sid)) != 1 {
		t.Error("a test does not clear a page failure")
	}
}

// The whole update path: changing one action's script reports the change and
// leaves the app not ready.
func TestUpdatingAnActionReportsItAndHoldsTheApp(t *testing.T) {
	pinRootDB(t)
	db := &DBase{Store: kvlite.MemStore()}
	script := "import json\nprint(json.dumps({\"message\": \"ok\"}))\n"
	SaveAppSpecAs(AppSpec{Slug: "cases", Name: "Cases", Owner: "u", RecordKey: "id",
		Actions: []AppAction{{Name: "gather-enc", Script: script}, {Name: "reset", Script: script}}}, "create")
	turn := &chatTurn{user: "u", udb: db, session: &ChatSession{ID: "sess-up"}}
	out, err := turn.appDefCreateOrUpdate(map[string]any{"id": "cases", "actions": []any{
		map[string]any{"name": "gather-enc", "script": script + "# skip ._ files\n"},
		map[string]any{"name": "reset", "script": script},
	}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "What this save changed: action gather-enc.") || !strings.Contains(out, "page itself is unchanged") {
		t.Errorf("the update says what changed:\n%s", out)
	}
	if un := unverifiedTools(db, "sess-up"); len(un) != 1 || !strings.Contains(un[0].Reason, "gather-enc") {
		t.Errorf("the app is held until gather-enc runs: %+v", un)
	}
}
