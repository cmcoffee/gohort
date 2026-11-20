package orchestrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// refreshFixture is curateFixture with the scheduler's store up and no
// refresh tasks left from before, or after.
func refreshFixture(t *testing.T) *dispatchFixture {
	t.Helper()
	df := curateFixture(t)
	PreInitScheduler()
	clear := func() {
		for _, task := range ListScheduledTasks(collectionRefreshKind) {
			UnscheduleTask(task.ID)
		}
	}
	clear()
	t.Cleanup(clear)
	return df
}

// A schedule is one task per collection: set, it is due an interval from
// now; saved again at the same interval it keeps its date; changed, it is
// replaced; Never removes it. The page reads when the next is due.
func TestARefreshIsOneScheduledTask(t *testing.T) {
	refreshFixture(t)
	if err := setRefresh("u", "col-1", "week"); err != nil {
		t.Fatal(err)
	}
	p, id, due, ok := refreshScheduled("u", "col-1")
	if !ok || p.Every != "week" || due.Sub(time.Now()) < 6*24*time.Hour {
		t.Fatalf("a weekly refresh: %+v due %v", p, due)
	}
	if line := refreshLine("u", "col-1"); !strings.HasPrefix(line, "Refreshes every week: next on ") {
		t.Errorf("the page's line: %q", line)
	}
	setRefresh("u", "col-1", "week")
	if _, again, _, _ := refreshScheduled("u", "col-1"); again != id {
		t.Error("saving the same interval moved the next refresh")
	}
	setRefresh("u", "col-1", "month")
	if p, _, due, _ := refreshScheduled("u", "col-1"); p.Every != "month" || due.Sub(time.Now()) < 29*24*time.Hour {
		t.Errorf("changed to monthly: %+v due %v", p, due)
	}
	if n := len(ListScheduledTasks(collectionRefreshKind)); n != 1 {
		t.Errorf("%d refresh tasks for one collection", n)
	}
	if setRefresh("u", "col-1", "daily") == nil {
		t.Error("an interval it does not offer was taken")
	}
	setRefresh("u", "col-1", "never")
	if _, _, _, ok := refreshScheduled("u", "col-1"); ok || refreshLine("u", "col-1") != "" {
		t.Error("Never left a refresh scheduled")
	}
}

// The collection page's Auto-Curation setting is the schedule: set through
// the steward endpoint, read back, and turned off.
func TestTheAutoCurationSettingIsTheSchedule(t *testing.T) {
	df := refreshFixture(t)
	saveCollection(df.udb, Collection{ID: "col-s", Name: "S", Owner: "u", SearchTerms: []string{"pg releases"}})
	save := func(every string) {
		r := httptest.NewRequest(http.MethodPost, "/api/collections/col-s/steward", nil)
		r.SetPathValue("id", "col-s")
		if _, err := df.app.collectionStewardSave(&jsonapi.Call{W: httptest.NewRecorder(), R: r, User: "u", UDB: df.udb}, struct {
			Refresh string `json:"refresh"`
		}{every}); err != nil {
			t.Fatal(err)
		}
	}
	read := func() map[string]any {
		out, err := df.app.collectionSteward(curateCall(df, http.MethodGet, "u", "col-s"))
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]any)
	}
	if got := read(); got["refresh"] != "never" || !strings.Contains(got["status"].(string), "only when you press Run now, adding up to 20 documents a run") {
		t.Errorf("a new collection: %v", got)
	}
	save("week")
	if got := read(); got["refresh"] != "week" || !strings.Contains(got["status"].(string), "every week") {
		t.Fatalf("weekly: %v", got)
	}
	save("never")
	if _, _, _, ok := refreshScheduled("u", "col-s"); ok || read()["refresh"] != "never" {
		t.Error("Off left the collection refreshing")
	}
	// With no plan, the line says nothing is searched for, not a schedule
	// that has nothing to run.
	saveCollection(df.udb, Collection{ID: "col-s", Name: "S", Owner: "u"})
	if got := read()["status"].(string); !strings.Contains(got, "no plan") {
		t.Errorf("no plan: %q", got)
	}
}

// A refresh firing schedules the next before it runs, runs the plan, and
// writes what it did onto the next one, which is what the page shows. A
// collection that is gone ends the chain.
func TestARefreshRunsThePlanAndRemembersIt(t *testing.T) {
	// Loopback: refused before any connection, so nothing leaves the test.
	df := planFixture(t, map[string]string{"pg releases": "1. Notes\n   http://127.0.0.1:9/notes\n   x\n"})
	PreInitScheduler()
	clear := func() {
		for _, task := range ListScheduledTasks(collectionRefreshKind) {
			UnscheduleTask(task.ID)
		}
	}
	clear()
	t.Cleanup(clear)
	saveCollection(df.udb, Collection{ID: "col-r", Name: "PG", Owner: "u", SearchTerms: []string{"pg releases"}})
	raw, _ := json.Marshal(refreshTask{Collection: "col-r", Owner: "u", Every: "week"})
	df.app.handleCollectionRefresh(context.Background(), raw)

	p, _, due, ok := refreshScheduled("u", "col-r")
	if !ok || due.Sub(time.Now()) < 6*24*time.Hour {
		t.Fatalf("the next refresh was not scheduled: %+v %v", p, due)
	}
	if p.LastRan == "" || !strings.Contains(p.LastNote, "1 page could not be used") {
		t.Errorf("what the refresh did is not kept: %+v", p)
	}
	if line := refreshLine("u", "col-r"); !strings.Contains(line, "Last on ") {
		t.Errorf("the page's line: %q", line)
	}

	// Run again after a restart cut it off: the next refresh is already
	// queued, and is kept rather than a second chain started beside it.
	_, nextID, nextDue, _ := refreshScheduled("u", "col-r")
	df.app.handleCollectionRefresh(context.Background(), raw)
	if n := len(ListScheduledTasks(collectionRefreshKind)); n != 1 {
		t.Errorf("a refresh run again left %d refreshes queued, want 1", n)
	}
	if _, id, due, _ := refreshScheduled("u", "col-r"); id != nextID || !due.Equal(nextDue) {
		t.Errorf("a refresh run again replaced its next one: %s at %v, was %s at %v", id, due, nextID, nextDue)
	}
	if p, _, _, _ := refreshScheduled("u", "col-r"); p.LastRan == "" {
		t.Error("a refresh run again did not write what it did onto the next one")
	}

	// Gone: the firing ends the chain instead of running.
	clear()
	raw, _ = json.Marshal(refreshTask{Collection: "col-gone", Owner: "u", Every: "week"})
	df.app.handleCollectionRefresh(context.Background(), raw)
	if _, _, _, ok := refreshScheduled("u", "col-gone"); ok {
		t.Error("a collection that is gone was rescheduled")
	}
}
