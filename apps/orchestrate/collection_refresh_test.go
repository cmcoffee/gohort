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
func refreshFixture(t *testing.T, run func(ctx context.Context, agentID, brief string) (string, error)) *dispatchFixture {
	t.Helper()
	df := curateFixture(t, run)
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
	refreshFixture(t, func(context.Context, string, string) (string, error) { return "", nil })
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

// The collection page sets it through the steward endpoint, reads it back,
// and choosing Hand Curated ends it.
func TestTheCurationFormSetsAndEndsTheRefresh(t *testing.T) {
	df := refreshFixture(t, func(context.Context, string, string) (string, error) { return "", nil })
	saveCollection(df.udb, Collection{ID: "col-s", Name: "S", Owner: "u"})
	save := func(body map[string]any) {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(http.MethodPost, "/api/collections/col-s/steward", strings.NewReader(string(raw)))
		r.SetPathValue("id", "col-s")
		r.Header.Set("Content-Type", "application/json")
		var b struct {
			Curation     string `json:"curation"`
			CuratorAgent string `json:"curator_agent"`
			Refresh      string `json:"refresh"`
		}
		json.Unmarshal(raw, &b)
		if _, err := df.app.collectionStewardSave(&jsonapi.Call{W: httptest.NewRecorder(), R: r, User: "u", UDB: df.udb}, b); err != nil {
			t.Fatal(err)
		}
	}
	read := func() string {
		out, err := df.app.collectionSteward(curateCall(df, http.MethodGet, "u", "col-s"))
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]any)["refresh"].(string)
	}
	save(map[string]any{"curation": "agent", "curator_agent": df.agent.ID, "refresh": "week"})
	if got := read(); got != "week" {
		t.Fatalf("refresh read back as %q", got)
	}
	save(map[string]any{"curation": "hand", "refresh": "week"})
	if _, _, _, ok := refreshScheduled("u", "col-s"); ok || read() != "never" {
		t.Error("Hand Curated left the collection refreshing")
	}
}

// A refresh firing schedules the next before it runs, runs the curator told
// it is a refresh, and writes what it did onto the next one, which is what the
// page shows. A collection no longer curated ends the chain.
func TestARefreshRunsTheCuratorAndRemembersIt(t *testing.T) {
	var brief string
	df := refreshFixture(t, func(_ context.Context, _ string, b string) (string, error) {
		brief = b
		return "Added the 17.2 release notes.", nil
	})
	saveCollection(df.udb, Collection{ID: "col-r", Name: "PG", Owner: "u", CuratorAgent: df.agent.ID})
	raw, _ := json.Marshal(refreshTask{Collection: "col-r", Owner: "u", Every: "week"})
	df.app.handleCollectionRefresh(context.Background(), raw)

	p, _, due, ok := refreshScheduled("u", "col-r")
	if !ok || due.Sub(time.Now()) < 6*24*time.Hour {
		t.Fatalf("the next refresh was not scheduled: %+v %v", p, due)
	}
	if p.LastRan == "" || !strings.Contains(p.LastNote, "Added the 17.2 release notes.") {
		t.Errorf("what the refresh did is not kept: %+v", p)
	}
	if !strings.Contains(brief, "scheduled refresh") || !strings.Contains(brief, "Look after the knowledge collection") {
		t.Errorf("the curator was not told it is a refresh:\n%s", brief)
	}
	if line := refreshLine("u", "col-r"); !strings.Contains(line, "Last on ") || !strings.Contains(line, "17.2 release notes") {
		t.Errorf("the page's line: %q", line)
	}

	// Hand curated now: the firing ends the chain instead of running.
	for _, task := range ListScheduledTasks(collectionRefreshKind) {
		UnscheduleTask(task.ID)
	}
	saveCollection(df.udb, Collection{ID: "col-r", Name: "PG", Owner: "u"})
	brief = ""
	df.app.handleCollectionRefresh(context.Background(), raw)
	if _, _, _, ok := refreshScheduled("u", "col-r"); ok || brief != "" {
		t.Error("a hand curated collection was refreshed or rescheduled")
	}
}

// An Auto-Curation collection's refresh is its plan, run on the schedule the
// same way Run now runs it: no agent, and what it did is kept for the page.
func TestAnAutoCuratedRefreshRunsThePlan(t *testing.T) {
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
	var agentRan bool
	curateRunAgent = func(*OrchestrateApp, context.Context, string, string, string) (string, error) {
		agentRan = true
		return "", nil
	}
	saveCollection(df.udb, Collection{ID: "col-p", Name: "PG", Owner: "u", CuratorAgent: collectionAutoCurator, SearchTerms: []string{"pg releases"}})
	raw, _ := json.Marshal(refreshTask{Collection: "col-p", Owner: "u", Every: "week"})
	df.app.handleCollectionRefresh(context.Background(), raw)
	p, _, _, ok := refreshScheduled("u", "col-p")
	if !ok || !strings.Contains(p.LastNote, "1 page could not be used") {
		t.Errorf("the plan's run is not what the refresh kept: %+v", p)
	}
	if agentRan {
		t.Error("an agent ran for an Auto-Curation refresh")
	}
}
