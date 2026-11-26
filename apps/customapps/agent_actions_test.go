package customapps

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/appscript"
)

// The app's agent gets each action as a tool: asked for exactly the inputs
// the action declares, run as the person chatting the way their click runs
// it (same records, saved into their store), shown on the button while it
// runs, and asking first where the button asks first.
func TestTheAppsAgentRunsItsActions(t *testing.T) {
	T := sharingTestApp(t)
	spec := AppSpec{Slug: "prep", Name: "Case prep", Owner: "alice", RecordKey: "id",
		Actions: []AppAction{
			{Name: "fetch", Label: "Fetch logs", Desc: "Download a case's logs into its folder.", Script: "x",
				Inputs: map[string]string{"case": "The case id, e.g. CASE-1"}},
			{Name: "wipe", Script: "x", Confirm: "Delete every case folder?"},
			{Name: "empty"},
		}}
	prev := runAppScript
	t.Cleanup(func() { runAppScript = prev })
	release := make(chan struct{})
	var gotArgs map[string]any
	runAppScript = func(j appscript.Job) (string, error) {
		gotArgs = j.Args
		if j.Caller != "bob" || !j.Long || j.Ctx == nil {
			t.Errorf("not run as bob's click: caller %q long %v", j.Caller, j.Long)
		}
		j.OnProgress("downloading 3 of 8")
		<-release
		return `{"message":"CASE-1 fetched","records":[{"id":"CASE-1","state":"raw"}],"result":{"files":8}}`, nil
	}
	udb := T.recordBase(spec, "bob")
	tools := T.actionTools("alice", "bob", udb, spec, "", "")
	if len(tools) != 2 {
		t.Fatalf("one tool per action with a script, got %d", len(tools))
	}
	fetch, wipe := tools[0], tools[1]
	if fetch.Tool.Name != "prep_fetch" || len(fetch.Tool.Parameters) != 1 || fetch.Tool.Parameters["case"].Description == "" ||
		!strings.Contains(fetch.Tool.Description, "Fetch logs") || !strings.Contains(fetch.Tool.Description, "Download a case's logs") {
		t.Errorf("the fetch tool: %+v", fetch.Tool)
	}
	if fetch.Confirmation != nil || wipe.Confirmation == nil || wipe.Confirmation.Prompt != "Delete every case folder?" {
		t.Error("only the action with a confirm prompt asks first")
	}

	done := make(chan string, 1)
	go func() {
		out, err := fetch.Handler(context.Background(), map[string]any{"case": "CASE-1", "smuggled": "x"})
		if err != nil {
			t.Error(err)
		}
		done <- out
	}()
	// While it runs, the button the person would have clicked shows it.
	deadline := time.Now().Add(5 * time.Second)
	var p map[string]any
	for time.Now().Before(deadline) {
		w := appAs(t, T, spec, "bob", httptest.NewRequest(http.MethodGet, "/apps/prep/action-progress/fetch", nil))
		json.Unmarshal(w.Body.Bytes(), &p)
		if p["running"] == true && strings.Contains(p["progress"].(string), "downloading 3 of 8") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p["running"] != true {
		t.Errorf("the button does not show the agent's run: %v", p)
	}
	close(release)
	out := <-done
	if !strings.Contains(out, "CASE-1 fetched") || !strings.Contains(out, `"files":8`) {
		t.Errorf("the agent's answer: %s", out)
	}
	if gotArgs["case"] != "CASE-1" || gotArgs["smuggled"] != nil || gotArgs["records"] == nil {
		t.Errorf("the script's args: %v", gotArgs)
	}
	if recs := appscript.ReadRecords(udb, spec.Slug); len(recs) != 1 || recs[0]["state"] != "raw" {
		t.Errorf("the records were not saved into the person's store: %v", recs)
	}
}

// Calls of one action that overlap each run, with their own inputs, and each
// gets its own answer: an agent's or an MCP client's call is not a second
// press of the button, so it never joins another call's run.
func TestOverlappingCallsOfAnActionEachRunWithTheirOwnInputs(t *testing.T) {
	freshJobs(t)
	T := sharingTestApp(t)
	spec := AppSpec{Slug: "prep", Name: "Case prep", Owner: "alice", RecordKey: "id",
		Actions: []AppAction{{Name: "echo", Script: "x", Inputs: map[string]string{"text": "what to echo"}}}}
	prev := runAppScript
	t.Cleanup(func() { runAppScript = prev })
	release := make(chan struct{})
	runAppScript = func(j appscript.Job) (string, error) {
		<-release
		b, _ := json.Marshal(map[string]any{"message": "echo:" + fmt.Sprint(j.Args["text"])})
		return string(b), nil
	}
	tool := T.actionTools("alice", "alice", T.recordBase(spec, "alice"), spec, "", "")[0]
	const n = 5
	got := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, err := tool.Handler(context.Background(), map[string]any{"text": fmt.Sprint("m", i)})
			if err != nil {
				out = "error: " + err.Error()
			}
			got[i] = out
		}(i)
	}
	time.Sleep(100 * time.Millisecond) // every call is waiting on its run
	close(release)
	wg.Wait()
	for i, out := range got {
		if want := fmt.Sprintf(`"echo:m%d"`, i); !strings.Contains(out, want) {
			t.Errorf("call %d answered %q, want its own %s", i, out, want)
		}
	}
}

// Stopping the agent's call stops the action it started.
func TestStoppingTheAgentsCallStopsTheAction(t *testing.T) {
	T := sharingTestApp(t)
	spec := AppSpec{Slug: "prep", Name: "Case prep", Owner: "alice", RecordKey: "id",
		Actions: []AppAction{{Name: "extract", Script: "x"}}}
	prev := runAppScript
	t.Cleanup(func() { runAppScript = prev })
	stopped := make(chan struct{})
	runAppScript = func(j appscript.Job) (string, error) {
		<-j.Ctx.Done()
		close(stopped)
		return "", j.Ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	tool := T.actionTools("alice", "alice", T.recordBase(spec, "alice"), spec, "", "")[0]
	errc := make(chan error, 1)
	go func() { _, err := tool.Handler(ctx, nil); errc <- err }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the action kept running after the call was stopped")
	}
	if err := <-errc; err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Errorf("the call should say it was stopped: %v", err)
	}
}

// The agent sees what the page shows: an action running now with its latest
// line, how one that ended went, the newest records, and a data source's JSON
// computed with the inputs it declares.
func TestTheAppsAgentSeesItsStatus(t *testing.T) {
	T := sharingTestApp(t)
	// Jobs are process-wide: start from none, so another test's do not show.
	actionJobs.Lock()
	actionJobs.byID, actionJobs.byKey = map[string]*actionJob{}, map[string]*actionJob{}
	actionJobs.Unlock()
	spec := AppSpec{Slug: "prep", Name: "Case prep", Owner: "alice", RecordKey: "id",
		Actions:     []AppAction{{Name: "status", Script: "x"}, {Name: "extract", Script: "x"}, {Name: "quick", Script: "x"}},
		DataSources: []AppDataSource{{Name: "cases", Script: "x", Inputs: map[string]string{"case": "Which case"}}}}
	prev := runAppScript
	t.Cleanup(func() { runAppScript = prev })
	release := make(chan struct{})
	runAppScript = func(j appscript.Job) (string, error) {
		switch j.Name {
		case "cases":
			return `{"case":"` + j.Args["case"].(string) + `","extracted":0.4}`, nil
		case "quick":
			return `{"message":"quick done","records":[{"id":"CASE-1","state":"raw"}]}`, nil
		}
		j.OnProgress("unpacking part 3 of 8")
		<-release
		return `{"message":"done"}`, nil
	}
	t.Cleanup(func() { close(release) })
	udb := T.recordBase(spec, "bob")
	tools := T.agentToolsFor("alice", "bob", udb, spec, "", "")
	byName := map[string]AgentToolDef{}
	for _, td := range tools {
		byName[td.Tool.Name] = td
	}
	// An action called "status" keeps its name; the status tool steps aside.
	status, ok := byName["prep_app_status"]
	if !ok || byName["prep_status"].Handler == nil {
		t.Fatalf("tools: %v", func() (n []string) {
			for k := range byName {
				n = append(n, k)
			}
			return
		}())
	}
	if _, err := byName["prep_quick"].Handler(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	go byName["prep_extract"].Handler(context.Background(), nil)
	var got string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, _ = status.Handler(context.Background(), nil)
		if strings.Contains(got, "unpacking part 3 of 8") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var st struct {
		Running []map[string]any `json:"running"`
		Ended   []map[string]any `json:"ended"`
		Count   int              `json:"record_count"`
		Newest  []map[string]any `json:"newest_records"`
	}
	if err := json.Unmarshal([]byte(got), &st); err != nil {
		t.Fatalf("%v: %s", err, got)
	}
	if len(st.Running) != 1 || st.Running[0]["action"] != "extract" || st.Running[0]["latest"] != "unpacking part 3 of 8" {
		t.Errorf("running: %v", st.Running)
	}
	if len(st.Ended) != 1 || st.Ended[0]["outcome"] != "quick done" {
		t.Errorf("ended: %v", st.Ended)
	}
	if st.Count != 1 || st.Newest[0]["state"] != "raw" {
		t.Errorf("records: %d %v", st.Count, st.Newest)
	}
	read := byName["read_prep_cases"]
	if read.Tool.Parameters["case"].Description != "Which case" {
		t.Errorf("the data tool's inputs: %+v", read.Tool)
	}
	if out, err := read.Handler(context.Background(), map[string]any{"case": "CASE-1"}); err != nil || !strings.Contains(out, `"extracted":0.4`) || !strings.Contains(out, "CASE-1") {
		t.Errorf("the data tool: %s %v", out, err)
	}
}

// A data tool answers its source's JSON reply alone, as the page takes it: a
// source that printed no JSON failed (what it printed instead, a sandbox or
// Python error, is not handed over as the data), a warning printed beside its
// JSON is set aside, and a run that printed no JSON is not cached, so the
// next call runs the source again.
func TestADataToolFailsWhenItsSourcePrintsNoJSON(t *testing.T) {
	T := sharingTestApp(t)
	spec := SaveAppSpec(AppSpec{Slug: "nojson", Name: "Shelf", Owner: "alice",
		DataSources: []AppDataSource{{Name: "shelf", Script: "x", Expose: true}}})
	prev := runAppScript
	t.Cleanup(func() { runAppScript = prev })
	reply, runs := "", 0
	runAppScript = func(appscript.Job) (string, error) { runs++; return reply, nil }
	tools := T.dataSourceTools("alice", "alice", T.recordBase(spec, "alice"), spec)
	if len(tools) != 1 {
		t.Fatalf("data tools: %d", len(tools))
	}
	read := tools[0].Handler

	reply = "bwrap: Can't chdir to /srv/ws/alice: No such file or directory"
	out, err := read(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "printed none") || strings.Contains(err.Error()+out, "bwrap") {
		t.Errorf("a source that printed no JSON: %q %v", out, err)
	}
	read(context.Background(), nil)
	if runs != 2 {
		t.Errorf("a run that printed no JSON was cached: %d runs for 2 calls", runs)
	}

	reply = "warning: something is deprecated\n[{\"id\": \"1\"}]"
	out, err = read(context.Background(), map[string]any{"fresh": "1"})
	if err != nil || strings.TrimSpace(out) != `[{"id": "1"}]` {
		t.Errorf("a warning beside the JSON: %q %v", out, err)
	}
}
