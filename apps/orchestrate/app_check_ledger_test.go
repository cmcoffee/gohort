package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The Builder's last app_def result said FAIL and "Do NOT tell the user the
// app is ready", and its reply told the user where to open the app. The
// app's standing is now in the ledger the finish check reads, so that reply
// is held back with what failed and what to do instead.
func TestAReplyIsHeldWhileAnAppsLastCheckFailed(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	recordToolVerify(db, "s1", appLedgerPrefix+"weather", false, "its last verify failed (FAIL: 1 problem(s))")
	var shown string
	notice, strike := buildGapsFinishCheck(nil, db, "s1", &shown, nil)("You can access the app at /apps/weather/.")
	if !strings.Contains(notice, "app weather is NOT ready") || !strings.Contains(notice, "do NOT tell the user where to open it") {
		t.Fatalf("notice:\n%s", notice)
	}
	if !strings.Contains(strike, "app weather") {
		t.Fatalf("strike = %q", strike)
	}
	// Shown once: the next reply, having been told, goes out.
	if n, _ := buildGapsFinishCheck(nil, db, "s1", &shown, nil)("It does not work yet: the chart fails."); n != "" {
		t.Fatalf("held twice: %s", n)
	}
	// A passing verify clears it.
	recordToolVerify(db, "s1", appLedgerPrefix+"weather", true, "")
	shown = ""
	if n, _ := buildGapsFinishCheck(nil, db, "s1", &shown, nil)("Ready."); n != "" {
		t.Fatalf("held after a pass: %s", n)
	}
}

// Create records an unverified app; an update that changes only notes leaves
// the standing alone; delete drops it.
func TestAppStandingFollowsTheAppsChanges(t *testing.T) {
	pinRootDB(t)
	db := &DBase{Store: kvlite.MemStore()}
	turn := &chatTurn{user: "u", udb: db, session: &ChatSession{ID: "s1"}}
	sections := []any{map[string]any{"kind": "form", "fields": []any{map[string]any{"name": "city"}}},
		map[string]any{"kind": "table", "empty_text": "None yet.", "columns": []any{map[string]any{"field": "city"}}}}
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"name": "Weather", "sections": sections}, false); err != nil {
		t.Fatal(err)
	}
	standing := func() (bool, string, bool) {
		for _, r := range loadToolVerifications(db, "s1") {
			if r.Tool == appLedgerPrefix+"weather" {
				return r.Passed, r.Reason, true
			}
		}
		return false, "", false
	}
	if passed, reason, ok := standing(); !ok || passed || !strings.Contains(reason, "not yet verified") {
		t.Fatalf("after create: %v %q %v", passed, reason, ok)
	}
	recordToolVerify(db, "s1", appLedgerPrefix+"weather", true, "") // a verify passed
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"id": "weather", "notes": "for the porch"}, true); err != nil {
		t.Fatal(err)
	}
	if passed, _, _ := standing(); !passed {
		t.Fatal("a notes edit demoted a verified app")
	}
	if _, err := turn.appDefDelete(map[string]any{"id": "weather"}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := standing(); ok {
		t.Fatal("a deleted app is still held to a check")
	}
}

// A script that calls a tool without declaring it is told at save, not on the
// first page load.
func TestAnUndeclaredCallToolIsNotedOnSave(t *testing.T) {
	pinRootDB(t)
	spec := AppSpec{Owner: "u", Slug: "wx", DataSources: []AppDataSource{{Name: "now",
		Script: "from gohort import call_tool\nprint(call_tool(\"get_weather\", city=\"Reno\"))\n"}}}
	notes := appToolCapNotes("u", spec)
	if len(notes) != 1 || !strings.Contains(notes[0], `call_tool("get_weather")`) || !strings.Contains(notes[0], "tool:get_weather") {
		t.Fatalf("notes = %q", notes)
	}
}
