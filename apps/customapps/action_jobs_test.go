package customapps

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/gohort/tools/appscript"
)

// A slow action answers that it is running and carries on as a job: its
// progress can be followed, it saves its records when it ends, and its outcome
// stays readable. A quick one answers in the request exactly as before.
func TestASlowActionRunsAsAJob(t *testing.T) {
	T := sharingTestApp(t)
	spec := AppSpec{Slug: "prep", Name: "Case prep", Owner: "alice", RecordKey: "id",
		Actions: []AppAction{{Name: "extract", Script: "x"}, {Name: "quick", Script: "x"}}}
	prevWait := actionInlineWait
	actionInlineWait = 100 * time.Millisecond
	t.Cleanup(func() { actionInlineWait = prevWait })
	prev := runAppScript
	t.Cleanup(func() { runAppScript = prev })
	release := make(chan struct{})
	runAppScript = func(j appscript.Job) (string, error) {
		if j.Name == "quick" {
			return `{"message":"quick done"}`, nil
		}
		if !j.Long || j.Ctx == nil || j.OnProgress == nil {
			t.Error("an action was not run as a long job with a context and a progress line")
			return "", nil
		}
		j.OnProgress("unpacking part 3 of 8")
		select {
		case <-release:
			return `{"message":"CASE-1 extracted","records":[{"case":"CASE-1","state":"ready"}]}`, nil
		case <-j.Ctx.Done():
			return "", j.Ctx.Err()
		}
	}
	post := func(name string) map[string]any {
		w := httptest.NewRecorder()
		T.handleAction(w, httptest.NewRequest(http.MethodPost, "/apps/prep/action/"+name, nil), "alice", "alice", T.recordBase(spec, "alice"), spec, name)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	progress := func(name string) map[string]any {
		w := appAs(t, T, spec, "alice", httptest.NewRequest(http.MethodGet, "/apps/prep/action-progress/"+name, nil))
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}

	if out := post("quick"); out["message"] != "quick done" || out["running"] != nil {
		t.Errorf("a quick action should answer in the request: %v", out)
	}
	started := post("extract")
	if started["running"] != true || started["job"] == nil {
		t.Fatalf("a slow action should answer that it is running: %v", started)
	}
	time.Sleep(50 * time.Millisecond)
	if p := progress("extract"); p["running"] != true || !strings.Contains(p["progress"].(string), "unpacking part 3 of 8") {
		t.Errorf("progress while it runs: %v", p)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	var done map[string]any
	for time.Now().Before(deadline) {
		if done = progress("extract"); done["done"] == true {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if done["outcome"] != "CASE-1 extracted" || done["saved"] != float64(1) {
		t.Errorf("the outcome once it ends: %v", done)
	}
	if recs := appscript.ReadRecords(T.recordBase(spec, "alice"), spec.Slug); len(recs) != 1 {
		t.Errorf("the job's records were not saved: %v", recs)
	}
	// Another person sees none of it.
	w := appAs(t, T, spec, "bob", httptest.NewRequest(http.MethodGet, "/apps/prep/action-progress/extract", nil))
	if strings.TrimSpace(w.Body.String()) != "{}" {
		t.Errorf("another person read the job: %s", w.Body.String())
	}
}

// Stop ends a running action's job, and the outcome says it was stopped.
func TestAnActionJobCanBeStopped(t *testing.T) {
	T := sharingTestApp(t)
	spec := AppSpec{Slug: "prep", Name: "Case prep", Owner: "alice", RecordKey: "id", Actions: []AppAction{{Name: "download", Script: "x"}}}
	prevWait := actionInlineWait
	actionInlineWait = 50 * time.Millisecond
	t.Cleanup(func() { actionInlineWait = prevWait })
	prev := runAppScript
	t.Cleanup(func() { runAppScript = prev })
	runAppScript = func(j appscript.Job) (string, error) {
		<-j.Ctx.Done()
		return "", j.Ctx.Err()
	}
	w := httptest.NewRecorder()
	T.handleAction(w, httptest.NewRequest(http.MethodPost, "/apps/prep/action/download", nil), "alice", "alice", T.recordBase(spec, "alice"), spec, "download")
	w = appAs(t, T, spec, "alice", httptest.NewRequest(http.MethodPost, "/apps/prep/action-cancel/download", nil))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w = appAs(t, T, spec, "alice", httptest.NewRequest(http.MethodGet, "/apps/prep/action-progress/download", nil))
		if strings.Contains(w.Body.String(), `"done":true`) {
			if !strings.Contains(w.Body.String(), "Failed") {
				t.Errorf("a stopped job should not read as a success: %s", w.Body.String())
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("Stop did not end the job")
}

// The app's own pages may reach the job endpoints from inside their sandbox,
// and the actions section follows a running action with progress and Stop.
func TestAppPagesReachTheJobEndpoints(t *testing.T) {
	joined := strings.Join(appOwnPaths, " ")
	for _, p := range []string{"action-progress/", "action-cancel/", "work"} {
		if !strings.Contains(joined, p) {
			t.Errorf("%s is not among the paths an app page may fetch", p)
		}
	}
	for _, want := range []string{`follow("action-progress/"+e+q({job:d.job}),o,"action")`, `cancelAction:function(n)`, `o.onProgress(p)`, `fetch("work")`, `follow("work/"+encodeURIComponent(d.job),o,"ask")`} {
		if !strings.Contains(appPageHelper, want) {
			t.Errorf("the page helper lost %s", want)
		}
	}
}

// A long action prints progress before its reply; the reply is the last JSON
// object, on one line or several, and output with none still reads as the
// error it was.
func TestAnActionsReplyIsTheLastJSONItPrinted(t *testing.T) {
	for out, want := range map[string]string{
		`{"message":"ok"}`: `{"message":"ok"}`,
		"downloading 10%\ndownloading 100%\nextracting {part 1}\n{\"message\":\"done\"}": `{"message":"done"}`,
		"step 1\n{\n  \"message\": \"pretty\"\n}":                                        "{\n  \"message\": \"pretty\"\n}",
		"Traceback: boom": "Traceback: boom",
	} {
		if got := lastJSONObject(out); got != want {
			t.Errorf("lastJSONObject(%q) = %q, want %q", out, got, want)
		}
	}
	db := sharingTestApp(t).recordBase(AppSpec{Slug: "prep", Owner: "alice"}, "alice")
	spec := AppSpec{Slug: "prep", Owner: "alice", RecordKey: "id", Actions: []AppAction{{Name: "x", Script: "x"}}}
	prev := runAppScript
	t.Cleanup(func() { runAppScript = prev })
	runAppScript = func(appscript.Job) (string, error) {
		return "fetching dump\nunpacking 3 of 3\n{\"message\":\"CASE-1 ready\"}", nil
	}
	res, err := runActionAndPersist("alice", db, db, spec, spec.Actions[0], map[string]any{}, "alice", scriptRun{})
	if err != nil || res.Message != "CASE-1 ready" {
		t.Errorf("progress lines before the reply broke it: %+v %v", res, err)
	}
}

// A running action is in the live pill, labelled with the app, the work and
// its latest line, owned by the person it runs for, with a Stop only they can
// use; it leaves the pill when it ends.
func TestARunningActionIsInTheLivePill(t *testing.T) {
	T := sharingTestApp(t)
	spec := AppSpec{Slug: "prep", Name: "Case prep", Owner: "alice", RecordKey: "id",
		Actions: []AppAction{{Name: "extract", Label: "Extract", Script: "x"}}}
	prevWait := actionInlineWait
	actionInlineWait = 50 * time.Millisecond
	t.Cleanup(func() { actionInlineWait = prevWait })
	prev := runAppScript
	t.Cleanup(func() { runAppScript = prev })
	runAppScript = func(j appscript.Job) (string, error) {
		j.OnProgress("unpacking part 3 of 8")
		<-j.Ctx.Done()
		return "", j.Ctx.Err()
	}
	w := httptest.NewRecorder()
	T.handleAction(w, httptest.NewRequest(http.MethodPost, "/apps/prep/action/extract", nil), "alice", "bob", T.recordBase(spec, "bob"), spec, "extract")

	var entry *LiveEntry
	for _, e := range T.liveAppWork() {
		if e.Owner == "bob" && strings.HasPrefix(e.Label, "Case prep: Extract") {
			e := e
			entry = &e
		}
	}
	if entry == nil {
		t.Fatalf("the running action is not in the pill: %+v", T.liveAppWork())
	}
	if !strings.Contains(entry.Label, "unpacking part 3 of 8") || !strings.HasPrefix(entry.Status, "action - ") ||
		!strings.HasSuffix(entry.URL, "/prep/") || !strings.Contains(entry.CancelURL, "/_app/job-cancel?id=") {
		t.Errorf("entry: %+v", entry)
	}
	if strings.Contains(entry.Status, "unpacking") {
		t.Error("the script's line is in the status, which is shown to everyone unmasked")
	}
	id := entry.CancelURL[strings.Index(entry.CancelURL, "id=")+3:]
	w = as(t, T, "mallory", httptest.NewRequest(http.MethodPost, "/apps/_app/job-cancel?id="+id, nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("another person stopped the job: %d", w.Code)
	}
	w = as(t, T, "bob", httptest.NewRequest(http.MethodPost, "/apps/_app/job-cancel?id="+id, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("the owner's Stop: %d %s", w.Code, w.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		gone := true
		for _, e := range T.liveAppWork() {
			if e.ID == entry.ID {
				gone = false
			}
		}
		if gone {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("a stopped job stayed in the pill")
}

// The same action called again while it runs, with the same body, joins that
// run, as a reopened page or a second press does; another body starts its
// own. The person's work lists both, newest first, and reads one by its job;
// someone else's work in the app shows none of it.
func TestTheSameActionAgainJoinsItsRunAndWorkListsIt(t *testing.T) {
	freshJobs(t)
	T := sharingTestApp(t)
	spec := AppSpec{Slug: "prep", Name: "Case prep", Owner: "alice", RecordKey: "id", Actions: []AppAction{{Name: "fetch", Script: "x"}}}
	prevWait := actionInlineWait
	actionInlineWait = 50 * time.Millisecond
	t.Cleanup(func() { actionInlineWait = prevWait })
	prev := runAppScript
	t.Cleanup(func() { runAppScript = prev })
	release := make(chan struct{})
	runs := 0
	var mu sync.Mutex
	runAppScript = func(j appscript.Job) (string, error) {
		mu.Lock()
		runs++
		mu.Unlock()
		select {
		case <-release:
			return `{"message":"fetched"}`, nil
		case <-j.Ctx.Done():
			return "", j.Ctx.Err()
		}
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	post := func(body string) string {
		w := httptest.NewRecorder()
		T.handleAction(w, httptest.NewRequest(http.MethodPost, "/apps/prep/action/fetch", strings.NewReader(body)), "alice", "alice", T.recordBase(spec, "alice"), spec, "fetch")
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		id, _ := out["job"].(string)
		return id
	}
	first := post(`{"item":"1"}`)
	again := post(`{"item":"1"}`)
	other := post(`{"item":"2"}`)
	if first == "" || again != first {
		t.Errorf("the same call again did not join its run: %q then %q", first, again)
	}
	if other == "" || other == first {
		t.Errorf("a call with another body joined the first: %q", other)
	}
	mu.Lock()
	n := runs
	mu.Unlock()
	if n != 2 {
		t.Errorf("%d runs started, want 2", n)
	}
	list := func(user string) []map[string]any {
		w := appAs(t, T, spec, user, httptest.NewRequest(http.MethodGet, "/apps/prep/work", nil))
		var out struct {
			Items []map[string]any `json:"items"`
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		return out.Items
	}
	items := list("alice")
	if len(items) != 2 || items[0]["job"] != other || items[0]["kind"] != "action" || items[0]["name"] != "fetch" || items[0]["running"] != true {
		t.Errorf("alice's work: %v", items)
	}
	if got := list("bob"); len(got) != 0 {
		t.Errorf("bob sees alice's work: %v", got)
	}
	if w := appAs(t, T, spec, "bob", httptest.NewRequest(http.MethodGet, "/apps/prep/work/"+first, nil)); strings.TrimSpace(w.Body.String()) != "{}" {
		t.Errorf("bob read alice's job: %s", w.Body.String())
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w := appAs(t, T, spec, "alice", httptest.NewRequest(http.MethodGet, "/apps/prep/work/"+first, nil))
		if strings.Contains(w.Body.String(), `"done":true`) {
			if !strings.Contains(w.Body.String(), `"outcome":"fetched"`) {
				t.Errorf("the ended job: %s", w.Body.String())
			}
			// Pressed again once it has ended, it runs again.
			post(`{"item":"1"}`)
			mu.Lock()
			n := runs
			mu.Unlock()
			if n != 3 {
				t.Errorf("a call after the run ended did not run again: %d runs, want 3", n)
			}
			found := false
			for _, p := range appOwnPaths {
				found = found || p == "work"
			}
			if !found {
				t.Error("the page may not reach work")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the job did not end")
}

// freshJobs starts the test with no app jobs, the earlier tests' put back
// after it.
func freshJobs(t *testing.T) {
	t.Helper()
	actionJobs.Lock()
	byID, byKey := actionJobs.byID, actionJobs.byKey
	actionJobs.byID, actionJobs.byKey = map[string]*actionJob{}, map[string]*actionJob{}
	actionJobs.Unlock()
	t.Cleanup(func() {
		actionJobs.Lock()
		actionJobs.byID, actionJobs.byKey = byID, byKey
		actionJobs.Unlock()
	})
}

// Work started from the page that ends with nobody watching tells the person
// how it went, as a native app's run does; work a page was following, work
// the run told them about itself, and work they stopped, do not.
func TestWorkThatEndsWhileNobodyWatchesTellsThePerson(t *testing.T) {
	freshJobs(t)
	T := sharingTestApp(t)
	spec := AppSpec{Slug: "prep", Name: "Case prep", Owner: "alice", RecordKey: "id",
		Actions: []AppAction{{Name: "extract", Label: "Extract", Script: "x"}, {Name: "own", Label: "Own notice", Script: "x"},
			{Name: "watched", Label: "Watched", Script: "x"}, {Name: "stop", Label: "Stopped", Script: "x"},
			{Name: "quick", Label: "Quick", Script: "x"}}}
	SaveAppSpec(spec)
	prevWait, prevAfter := actionInlineWait, unwatchedAfter
	actionInlineWait, unwatchedAfter = 30*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { actionInlineWait, unwatchedAfter = prevWait, prevAfter })
	prev := runAppScript
	t.Cleanup(func() { runAppScript = prev })
	gates := map[string]chan struct{}{"extract": make(chan struct{}), "own": make(chan struct{}), "watched": make(chan struct{}), "stop": make(chan struct{})}
	runAppScript = func(j appscript.Job) (string, error) {
		if j.Name == "quick" {
			return `{"message":"done at once"}`, nil
		}
		select {
		case <-gates[j.Name]:
		case <-j.Ctx.Done():
			return "", j.Ctx.Err()
		}
		if j.Name == "own" && j.OnNotify != nil {
			j.OnNotify()
		}
		return `{"message":"CASE-1 extracted"}`, nil
	}
	start := func(name string) string {
		w := httptest.NewRecorder()
		T.handleAction(w, httptest.NewRequest(http.MethodPost, "/apps/prep/action/"+name, nil), "alice", "alice", T.recordBase(spec, "alice"), spec, name)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		id, _ := out["job"].(string)
		if id == "" {
			t.Fatalf("%s did not run as a job: %s", name, w.Body.String())
		}
		return id
	}
	ended := func(job string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			actionJobs.Lock()
			j := actionJobs.byID[job]
			actionJobs.Unlock()
			j.mu.Lock()
			done := !j.ended.IsZero()
			j.mu.Unlock()
			if done {
				time.Sleep(20 * time.Millisecond)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("the job did not end")
	}
	titles := func() []string {
		var out []string
		for _, n := range notices.List(RootDB, "alice") {
			out = append(out, n.Title)
		}
		return out
	}

	// One that answers while the page waits for it: watched the whole time.
	w := httptest.NewRecorder()
	T.handleAction(w, httptest.NewRequest(http.MethodPost, "/apps/prep/action/quick", nil), "alice", "alice", T.recordBase(spec, "alice"), spec, "quick")
	if !strings.Contains(w.Body.String(), "done at once") {
		t.Fatalf("the quick action: %s", w.Body.String())
	}
	start("extract")
	start("own")
	watched := start("watched")
	stopped := start("stop")
	time.Sleep(250 * time.Millisecond) // past unwatchedAfter: everyone has left
	close(gates["extract"])
	close(gates["own"])
	appAs(t, T, spec, "alice", httptest.NewRequest(http.MethodPost, "/apps/prep/action-cancel/stop?job="+stopped, nil))
	ended(stopped)
	// A page follows the last one until it ends.
	appAs(t, T, spec, "alice", httptest.NewRequest(http.MethodGet, "/apps/prep/work/"+watched, nil))
	close(gates["watched"])
	for i := 0; i < 100 && !strings.Contains(appAs(t, T, spec, "alice", httptest.NewRequest(http.MethodGet, "/apps/prep/work/"+watched, nil)).Body.String(), `"done":true`); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	got := strings.Join(titles(), " | ")
	if got != "Extract finished" {
		t.Errorf("notices: %q, want only the unwatched one's", got)
	}
	if ns := notices.List(RootDB, "alice"); len(ns) == 1 && !strings.Contains(ns[0].Body, "CASE-1 extracted") {
		t.Errorf("the notice does not say how it went: %q", ns[0].Body)
	}
}
