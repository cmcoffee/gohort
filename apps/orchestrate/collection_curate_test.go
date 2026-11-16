package orchestrate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// curateCall is a request on one collection's curate endpoints, as user.
func curateCall(df *dispatchFixture, method, user, id string) *jsonapi.Call {
	r := httptest.NewRequest(method, "/api/collections/"+id+"/curate", nil)
	r.SetPathValue("id", id)
	return &jsonapi.Call{W: httptest.NewRecorder(), R: r, User: user, UDB: UserDB(df.app.DB, user)}
}

// curateFixture resets the running curations, so each test starts with none,
// and has a running one recount its documents often.
func curateFixture(t *testing.T) *dispatchFixture {
	t.Helper()
	df := newDispatchFixture(t, FakeTurn{Content: "done", Repeat: true})
	prevEvery := curateCountEvery
	curateCountEvery = 5 * time.Millisecond
	curations.mu.Lock()
	prevRuns := curations.runs
	curations.runs = map[string]*curateRun{}
	curations.mu.Unlock()
	t.Cleanup(func() {
		// Every run ends before the stores it reads are put back.
		curations.mu.Lock()
		running := curations.runs
		curations.runs = prevRuns
		curations.mu.Unlock()
		for _, run := range running {
			run.cancel()
			<-run.done
		}
		curateCountEvery = prevEvery
	})
	return df
}

// curateStatus is what the progress endpoint answers now.
func curateStatus(t *testing.T, df *dispatchFixture, id string) map[string]any {
	t.Helper()
	out, err := df.app.collectionCurateProgress(curateCall(df, http.MethodGet, "u", id))
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)
}

// waitCurate polls the progress endpoint until ok says it is what was
// expected, or fails with the last answer.
func waitCurate(t *testing.T, df *dispatchFixture, id string, ok func(map[string]any) bool) map[string]any {
	t.Helper()
	var last map[string]any
	for i := 0; i < 400; i++ {
		if last = curateStatus(t, df, id); ok(last) {
			return last
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("curate status never got there: %v", last)
	return nil
}

// blockingSearch has the plan's searches wait for release, so a test can see
// a run while it goes.
func blockingSearch(t *testing.T) chan struct{} {
	t.Helper()
	release := make(chan struct{})
	prevSearch, prevReady := collectionWebSearch, planSearchReady
	collectionWebSearch = func(string) string { <-release; return "" }
	planSearchReady = func() bool { return true }
	t.Cleanup(func() { collectionWebSearch, planSearchReady = prevSearch, prevReady })
	return release
}

// Run now carries out the plan on a run of its own: the page sees it alive
// while it goes, a second press joins it rather than starting another, and
// once it ends the page reads how it went. With no plan it is refused saying
// what to do.
func TestRunNowRunsThePlanWithProgressAndOutcome(t *testing.T) {
	df := curateFixture(t)
	release := blockingSearch(t)
	saveCollection(df.udb, Collection{ID: "col-n", Name: "N", Owner: "u"})
	if _, err := df.app.collectionCurateStart(curateCall(df, http.MethodPost, "u", "col-n"), struct{}{}); err == nil || !strings.Contains(err.Error(), "Draft a plan") {
		t.Fatalf("a collection with no plan: %v", err)
	}

	saveCollection(df.udb, Collection{ID: "col-a", Name: "Runbooks", Owner: "u", SearchTerms: []string{"deploy runbook"}})
	items, err := df.app.collectionCurateItems(curateCall(df, http.MethodGet, "u", "col-a"))
	if err != nil || items.([]map[string]any)[0]["item"] != "Run now" || !strings.Contains(items.([]map[string]any)[0]["history"].(string), "1 search term") {
		t.Fatalf("the Run now row: %v %v", items, err)
	}
	out, err := df.app.collectionCurateStart(curateCall(df, http.MethodPost, "u", "col-a"), struct{}{})
	if err != nil || out.(map[string]any)["running"] != true {
		t.Fatalf("start: %v %v", out, err)
	}
	waitCurate(t, df, "col-a", func(s map[string]any) bool {
		p, _ := s["progress"].(string)
		return strings.Contains(p, "Running the plan")
	})
	again, _ := df.app.collectionCurateStart(curateCall(df, http.MethodPost, "u", "col-a"), struct{}{})
	if !strings.Contains(again.(map[string]any)["message"].(string), "Already running") {
		t.Errorf("a second press should join the run: %v", again)
	}
	close(release)
	end := waitCurate(t, df, "col-a", func(s map[string]any) bool { return s["outcome"] != nil })
	if o := end["outcome"].(string); !strings.HasPrefix(o, "Done in") || !strings.Contains(o, "0 documents added") {
		t.Errorf("outcome %q", o)
	}
}

// Stop ends the run and says so.
func TestRunNowStops(t *testing.T) {
	df := curateFixture(t)
	release := blockingSearch(t)
	saveCollection(df.udb, Collection{ID: "col-s", Name: "S", Owner: "u", SearchTerms: []string{"one", "two"}})
	if _, err := df.app.collectionCurateStart(curateCall(df, http.MethodPost, "u", "col-s"), struct{}{}); err != nil {
		t.Fatal(err)
	}
	waitCurate(t, df, "col-s", func(s map[string]any) bool { return s["progress"] != nil })
	if _, err := df.app.collectionCurateStop(curateCall(df, http.MethodPost, "u", "col-s"), struct{}{}); err != nil {
		t.Fatal(err)
	}
	close(release)
	end := waitCurate(t, df, "col-s", func(s map[string]any) bool { return s["outcome"] != nil })
	if o := end["outcome"].(string); !strings.HasPrefix(o, "Stopped") {
		t.Errorf("outcome %q", o)
	}
}

// Only the collection's owner runs its plan: somebody it is shared with is
// refused before anything starts, and their copy of the record leaves out its
// schedule.
func TestRunNowIsTheOwners(t *testing.T) {
	df := curateFixture(t)
	release := blockingSearch(t)
	close(release)
	df.app.DB.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	// Saved where a share resolves from too: the collections bucket, which in
	// a running server is the app's own store and in this fixture is not.
	shared := Collection{ID: "col-c", Name: "C", Owner: "u", AllowedUsers: []string{"bob"}, SearchTerms: []string{"x"}}
	saveCollection(df.udb, shared)
	saveCollection(UserDB(CollectionsDB(), "u"), shared)
	if _, ok := loadCollection(UserDB(df.app.DB, "bob"), "bob", "col-c"); !ok {
		t.Fatal("setup: the collection is not shared with bob")
	}
	if _, err := df.app.collectionCurateStart(curateCall(df, http.MethodPost, "bob", "col-c"), struct{}{}); err == nil {
		t.Error("someone the collection is shared with started its plan")
	}
	if _, err := df.app.collectionCurateItems(curateCall(df, http.MethodGet, "bob", "col-c")); err == nil {
		t.Error("someone the collection is shared with was offered Run now")
	}
	if rec, err := df.app.collectionGet(curateCall(df, http.MethodGet, "u", "col-c")); err != nil || rec.(map[string]any)["auto_curation"] != "never" {
		t.Errorf("the owner's record does not say its schedule: %v %v", rec, err)
	}
	if rec, err := df.app.collectionGet(curateCall(df, http.MethodGet, "bob", "col-c")); err != nil || rec.(map[string]any)["auto_curation"] != "" {
		t.Errorf("a reader's record carries the owner's schedule: %v %v", rec, err)
	}
	if _, ok := curations.get("col-c"); ok {
		t.Error("a run started for a non-owner")
	}
}

// Times on the page read as words: a limit in minutes, an elapsed run in
// seconds, then minutes and seconds.
func TestCurateTimesReadAsWords(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Minute: "10 minutes",
		time.Minute:      "1 minute",
		90 * time.Second: "1m 30s",
	} {
		if got := curateLimit(d); got != want {
			t.Errorf("curateLimit(%s) = %q, want %q", d, got, want)
		}
	}
	if got := curateElapsed(41 * time.Second); got != "41s" {
		t.Errorf("curateElapsed(41s) = %q", got)
	}
}

