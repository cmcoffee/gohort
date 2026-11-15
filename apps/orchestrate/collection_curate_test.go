package orchestrate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// curateCall is a request on one collection's curate endpoints, as user.
func curateCall(df *dispatchFixture, method, user, id string) *jsonapi.Call {
	r := httptest.NewRequest(method, "/api/collections/"+id+"/curate", nil)
	r.SetPathValue("id", id)
	return &jsonapi.Call{W: httptest.NewRecorder(), R: r, User: user, UDB: UserDB(df.app.DB, user)}
}

// curateFixture stands in for the agent run and resets the running
// curations, so each test starts with none.
func curateFixture(t *testing.T, run func(ctx context.Context, agentID, brief string) (string, error)) *dispatchFixture {
	t.Helper()
	df := newDispatchFixture(t, FakeTurn{Content: "done", Repeat: true})
	prevRun, prevEvery := curateRunAgent, curateCountEvery
	curateRunAgent = func(_ *OrchestrateApp, ctx context.Context, _, agentID, brief string) (string, error) {
		return run(ctx, agentID, brief)
	}
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
		curateRunAgent, curateCountEvery = prevRun, prevEvery
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

// Curate now runs whoever curates the collection, on a run of its own: the
// page sees it alive and how many documents it has added while it goes, a
// second press joins it rather than starting another, and once it ends the
// page reads how it went, with the curator's own report.
func TestCurateNowRunsTheCuratorWithProgressAndOutcome(t *testing.T) {
	release := make(chan struct{})
	var gotAgent, gotBrief string
	df := curateFixture(t, func(ctx context.Context, agentID, brief string) (string, error) {
		gotAgent, gotBrief = agentID, brief
		PutChunks(VectorDB, []EmbeddedChunk{{ID: "c1", Source: collectionSource("col-a") + "page", ReportID: "r1", Text: "runbook"}})
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return "Added the deploy runbook from the project's own docs.", nil
	})
	saveCollection(df.udb, Collection{ID: "col-a", Name: "Runbooks", Owner: "u", Description: "How we deploy and roll back."})

	// By hand: nothing to press, and a start is refused saying what to do.
	items, err := df.app.collectionCurateItems(curateCall(df, http.MethodGet, "u", "col-a"))
	if err != nil || len(items.([]map[string]any)) != 0 {
		t.Fatalf("a collection curated by hand offered a run: %v %v", items, err)
	}
	if _, err := df.app.collectionCurateStart(curateCall(df, http.MethodPost, "u", "col-a"), struct{}{}); err == nil || !strings.Contains(err.Error(), "Curation") {
		t.Fatalf("starting with nobody curating: %v", err)
	}

	saveCollection(df.udb, Collection{ID: "col-a", Name: "Runbooks", Owner: "u", Description: "How we deploy and roll back.", CuratorAgent: df.agent.ID})
	items, _ = df.app.collectionCurateItems(curateCall(df, http.MethodGet, "u", "col-a"))
	if rows := items.([]map[string]any); len(rows) != 1 || !strings.Contains(rows[0]["desc"].(string), df.agent.Name) {
		t.Fatalf("the row should name the curator: %v", items)
	}

	out, err := df.app.collectionCurateStart(curateCall(df, http.MethodPost, "u", "col-a"), struct{}{})
	if err != nil || out.(map[string]any)["running"] != true {
		t.Fatalf("start: %v %v", out, err)
	}
	waitCurate(t, df, "col-a", func(s map[string]any) bool {
		p, _ := s["progress"].(string)
		return strings.Contains(p, "Curating") && strings.Contains(p, "1 document added")
	})
	again, _ := df.app.collectionCurateStart(curateCall(df, http.MethodPost, "u", "col-a"), struct{}{})
	if !strings.Contains(again.(map[string]any)["message"].(string), "Already") {
		t.Errorf("a second press started another run: %v", again)
	}
	close(release)
	end := waitCurate(t, df, "col-a", func(s map[string]any) bool { return s["outcome"] != nil })
	if o := end["outcome"].(string); !strings.Contains(o, "1 document added") || !strings.Contains(o, "deploy runbook") || end["progress"] != nil {
		t.Errorf("the outcome should say what was added and carry the report: %v", end)
	}
	if gotAgent != df.agent.ID {
		t.Errorf("ran %q, not the curator", gotAgent)
	}
	for _, want := range []string{"Runbooks", "How we deploy and roll back.", "Do not remove anything", "It holds 0 documents"} {
		if !strings.Contains(gotBrief, want) {
			t.Errorf("the brief is missing %q:\n%s", want, gotBrief)
		}
	}
}

// Stop ends the run at the curator's next step and says so; a run that
// fails says why.
func TestCurateNowStopsAndReportsFailure(t *testing.T) {
	fail := false
	df := curateFixture(t, func(ctx context.Context, _, _ string) (string, error) {
		if fail {
			return "", errors.New("the model is unreachable")
		}
		<-ctx.Done()
		return "", ctx.Err()
	})
	saveCollection(df.udb, Collection{ID: "col-b", Name: "B", Owner: "u", CuratorAgent: df.agent.ID})
	if _, err := df.app.collectionCurateStart(curateCall(df, http.MethodPost, "u", "col-b"), struct{}{}); err != nil {
		t.Fatal(err)
	}
	if _, err := df.app.collectionCurateStop(curateCall(df, http.MethodPost, "u", "col-b"), struct{}{}); err != nil {
		t.Fatal(err)
	}
	end := waitCurate(t, df, "col-b", func(s map[string]any) bool { return s["outcome"] != nil })
	if !strings.HasPrefix(end["outcome"].(string), "Stopped") {
		t.Errorf("a stopped run: %v", end)
	}

	fail = true
	if _, err := df.app.collectionCurateStart(curateCall(df, http.MethodPost, "u", "col-b"), struct{}{}); err != nil {
		t.Fatal(err)
	}
	end = waitCurate(t, df, "col-b", func(s map[string]any) bool {
		o, _ := s["outcome"].(string)
		return strings.HasPrefix(o, "Failed")
	})
	if !strings.Contains(end["outcome"].(string), "unreachable") {
		t.Errorf("a failed run should say why: %v", end)
	}
}

// Only the collection's owner runs its curator: somebody else is refused
// before anything starts.
func TestCurateNowIsTheOwners(t *testing.T) {
	ran := false
	df := curateFixture(t, func(context.Context, string, string) (string, error) { ran = true; return "", nil })
	df.app.DB.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	// Saved where a share resolves from too: the collections bucket, which in
	// a running server is the app's own store and in this fixture is not.
	shared := Collection{ID: "col-c", Name: "C", Owner: "u", CuratorAgent: df.agent.ID, AllowedUsers: []string{"bob"}}
	saveCollection(df.udb, shared)
	saveCollection(UserDB(CollectionsDB(), "u"), shared)
	if _, ok := loadCollection(UserDB(df.app.DB, "bob"), "bob", "col-c"); !ok {
		t.Fatal("setup: the collection is not shared with bob")
	}
	if _, err := df.app.collectionCurateStart(curateCall(df, http.MethodPost, "bob", "col-c"), struct{}{}); err == nil {
		t.Error("someone the collection is shared with started its curator")
	}
	if _, err := df.app.collectionCurateItems(curateCall(df, http.MethodGet, "bob", "col-c")); err == nil {
		t.Error("someone the collection is shared with was offered Curate now")
	}
	// The record says who curates it to its owner, for the page header, and
	// to nobody else.
	if rec, err := df.app.collectionGet(curateCall(df, http.MethodGet, "u", "col-c")); err != nil || rec.(map[string]any)["curator_agent"] != df.agent.ID {
		t.Errorf("the owner's record does not say who curates it: %v %v", rec, err)
	}
	if rec, err := df.app.collectionGet(curateCall(df, http.MethodGet, "bob", "col-c")); err != nil || rec.(map[string]any)["curator_agent"] != "" {
		t.Errorf("a reader's record names the owner's curator: %v %v", rec, err)
	}
	time.Sleep(20 * time.Millisecond)
	if ran {
		t.Error("the curator ran for a non-owner")
	}
}

// Auto-Curation is an app's hidden agent set as the curator. Run on the
// collection's behalf it is offered the collection tool, as any curator is,
// and the page reads it as Auto-Curation rather than by an agent's name. With
// no web search to find material, a start says so instead of running.
func TestAHiddenAppCuratorKeepsItsCollection(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "done", Repeat: true})
	const id = "app-test-collection-curator"
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: id, OwningApp: "Test", Name: "Collection curator", Prompt: "You keep a collection current.",
		AllowedTools: []string{"web_search", "fetch_url"}, Hidden: true,
	})
	saveCollection(df.udb, Collection{ID: "col-h", Name: "Kubernetes", Owner: "u", CuratorAgent: id})
	df.run(AgentSyncRun{AgentKey: id, Kind: "scheduled", Message: "Look after the collection."})
	if !df.toolsOffered()["collection"] {
		t.Errorf("the hidden curator was not offered its collection tool: %v", df.toolsOffered())
	}
	c, _ := loadCollection(df.udb, "u", "col-h")
	if line := df.app.stewardStatusLine("u", c); !strings.HasPrefix(line, "Auto-Curation") {
		t.Errorf("status line: %q", line)
	}
	if !WebSearchAvailable() {
		if _, err := df.app.collectionCurateStart(curateCall(df, http.MethodPost, "u", "col-h"), struct{}{}); err == nil || !strings.Contains(err.Error(), "web search") {
			t.Errorf("auto-curation without web search: %v", err)
		}
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
