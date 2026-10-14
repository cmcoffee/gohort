package customapps

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
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
		w := httptest.NewRecorder()
		T.handleActionProgress(w, httptest.NewRequest(http.MethodGet, "/apps/prep/action-progress/"+name, nil), "alice", "alice", spec, name)
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
	w := httptest.NewRecorder()
	T.handleActionProgress(w, httptest.NewRequest(http.MethodGet, "/apps/prep/action-progress/extract", nil), "alice", "bob", spec, "extract")
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
	w = httptest.NewRecorder()
	T.handleActionCancel(w, httptest.NewRequest(http.MethodPost, "/apps/prep/action-cancel/download", nil), "alice", "alice", spec, "download")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w = httptest.NewRecorder()
		T.handleActionProgress(w, httptest.NewRequest(http.MethodGet, "/apps/prep/action-progress/download", nil), "alice", "alice", spec, "download")
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
	for _, p := range []string{"action-progress/", "action-cancel/"} {
		if !strings.Contains(joined, p) {
			t.Errorf("%s is not among the paths an app page may fetch", p)
		}
	}
	for _, want := range []string{`fetch("action-progress/"+e+q({job:d.job}))`, `cancelAction:function(n)`, `o.onProgress(p)`} {
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
	w = httptest.NewRecorder()
	handleJobCancel(w, httptest.NewRequest(http.MethodPost, "/apps/_app/job-cancel?id="+id, nil), "mallory")
	if w.Code != http.StatusNotFound {
		t.Errorf("another person stopped the job: %d", w.Code)
	}
	w = httptest.NewRecorder()
	handleJobCancel(w, httptest.NewRequest(http.MethodPost, "/apps/_app/job-cancel?id="+id, nil), "bob")
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
