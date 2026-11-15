package orchestrate

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// fillFixture stands in for the searches written and run, and clears plans
// from earlier tests.
func fillFixture(t *testing.T, queries []string, results map[string]string) *dispatchFixture {
	t.Helper()
	df := suggestFixture(t, results, queries...)
	fillPlans.Lock()
	prev := fillPlans.m
	fillPlans.m = map[string]fillPlan{}
	fillPlans.Unlock()
	t.Cleanup(func() {
		fillPlans.Lock()
		fillPlans.m = prev
		fillPlans.Unlock()
	})
	return df
}

func waitFill(t *testing.T, df *dispatchFixture, id string, done func(map[string]any) bool) map[string]any {
	t.Helper()
	var last map[string]any
	for i := 0; i < 400; i++ {
		out, err := df.app.collectionFillProgress(curateCall(df, http.MethodGet, "u", id))
		if err != nil {
			t.Fatal(err)
		}
		if last = out.(map[string]any); done(last) {
			return last
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("never got there: %v", last)
	return nil
}

// A drafted plan is the searches written from the collection and a number of
// documents, shown with what that comes to, before anything runs. The page
// gets it as a form to edit; with no plan, a line saying how to get one.
func TestAPlanIsShownWithItsSizeBeforeItRuns(t *testing.T) {
	df := fillFixture(t, []string{"postgres replication setup", "postgres backup tools"}, nil)
	c := Collection{ID: "col-f", Name: "PG", Owner: "u", Description: "PostgreSQL administration."}
	saveCollection(df.udb, c)

	got, _ := df.app.collectionFillFields(curateCall(df, http.MethodGet, "u", "col-f"))
	if f := got.([]ui.FormField); len(f) != 1 || f[0].Type != "header" {
		t.Errorf("before a plan: %+v", f)
	}
	out, err := df.app.draftPlan(context.Background(), c, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "2 searches, up to 20 documents") {
		t.Errorf("the plan's outcome should say its size: %q", out)
	}
	got, _ = df.app.collectionFillFields(curateCall(df, http.MethodGet, "u", "col-f"))
	f := got.([]ui.FormField)
	if len(f) != 3 || f[0].Help == "" || f[1].Field != "searches" || f[1].Type != "rules" || f[2].Field != "documents" {
		t.Fatalf("the plan's form: %+v", f)
	}
	vals, _ := df.app.collectionFillPlan(curateCall(df, http.MethodGet, "u", "col-f"))
	if v := vals.(map[string]any); v["searches"] != "postgres replication setup\npostgres backup tools" || v["documents"] != 20 {
		t.Errorf("the plan's values: %v", v)
	}

	collectionQueriesFor = func(context.Context, *OrchestrateApp, Collection, int) []string { return nil }
	if _, err := df.app.draftPlan(context.Background(), c, func(string) {}); err == nil {
		t.Error("a plan with no searches was kept")
	}
}

// Run as edited: the searches run, what they find is fetched, and the run
// says how far it has got and how it ended. A plan with no searches, or more
// than a run may make, is refused; the count is held to the cap.
func TestAPlanRunsAsEdited(t *testing.T) {
	// Loopback: refused before any connection, so nothing leaves the test.
	df := fillFixture(t, nil, map[string]string{
		"edited search": "1. A page\n   http://127.0.0.1:9/a\n   line\n\n2. Another\n   http://127.0.0.1:9/b\n   line\n",
	})
	saveCollection(df.udb, Collection{ID: "col-r", Name: "R", Owner: "u"})
	run := func(searches string, docs any) error {
		s, _ := json.Marshal(searches)
		d, _ := json.Marshal(docs)
		_, err := df.app.collectionFillRun(curateCall(df, http.MethodPost, "u", "col-r"), struct {
			Searches  json.RawMessage `json:"searches"`
			Documents json.RawMessage `json:"documents"`
		}{Searches: s, Documents: d})
		return err
	}
	if run("", 5) == nil {
		t.Error("a plan with no searches ran")
	}
	if run(strings.Repeat("x\n", autofillMaxQueries()+1), 5) == nil {
		t.Error("more searches than a run may make were run")
	}
	if run("edited search", 0) == nil {
		t.Error("a plan adding no documents ran")
	}
	if err := run("edited search", "100000"); err != nil {
		t.Fatal(err)
	}
	if p, _ := planFor("col-r"); p.Documents != autofillMaxDocs() || len(p.Searches) != 1 {
		t.Errorf("the plan as run: %+v", p)
	}
	end := waitFill(t, df, "col-r", func(s map[string]any) bool { return s["outcome"] != nil })
	if o := end["outcome"].(string); !strings.Contains(o, "2 pages could not be used") {
		t.Errorf("outcome %q", o)
	}
}
