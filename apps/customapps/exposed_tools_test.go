package customapps

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/appscript"
	"github.com/cmcoffee/snugforge/kvlite"
)

// What an app marks expose, and only that, is a tool of its owner's: listed
// under the app's name, run as the owner's click would be, asking first
// where the button asks first. Nothing of someone else's app, and nothing of
// an app switched off.
func TestAnAppOffersWhatItExposes(t *testing.T) {
	T := sharingTestApp(t)
	SaveAppSpec(AppSpec{Slug: "books", Name: "Reading List", Owner: "alice", RecordKey: "id",
		Actions: []AppAction{
			{Name: "add", Label: "Add book", Script: "x", Expose: true, Inputs: map[string]string{"title": "The book's title"}},
			{Name: "wipe", Script: "x", Expose: true, Confirm: "Delete every book?"},
			{Name: "internal", Script: "x"},
		},
		DataSources: []AppDataSource{
			{Name: "unread", Script: "x", Expose: true},
			{Name: "stats", Script: "x"},
		},
		PipelineID: "p1", ExposePipeline: true})
	SaveAppSpec(AppSpec{Slug: "off", Name: "Off", Owner: "alice", Disabled: true,
		Actions: []AppAction{{Name: "go", Script: "x", Expose: true}}})
	SaveAppSpec(AppSpec{Slug: "bobs", Name: "Bob's", Owner: "bob",
		Actions: []AppAction{{Name: "go", Script: "x", Expose: true}}})

	names := map[string]AgentToolDef{}
	for _, d := range T.exposedTools(nil, "alice") {
		names[d.Tool.Name] = d
	}
	for _, want := range []string{"books_add", "books_wipe", "read_books_unread", "books_run_pipeline"} {
		d, ok := names[want]
		if !ok {
			t.Errorf("%s is not offered: %v", want, keysOf(names))
			continue
		}
		if d.Tool.Category != "App: Reading List" {
			t.Errorf("%s is listed under %q", want, d.Tool.Category)
		}
	}
	for _, not := range []string{"books_internal", "read_books_stats", "off_go", "bobs_go"} {
		if _, ok := names[not]; ok {
			t.Errorf("%s is offered though it is not exposed, its app is off, or it is someone else's", not)
		}
	}
	if len(names) != 4 {
		t.Errorf("offered: %v", keysOf(names))
	}
	if names["books_wipe"].Confirmation == nil || names["books_add"].Confirmation != nil {
		t.Error("only the action with a confirm prompt asks first")
	}

	// A call is the owner's click: their records, their caller id.
	prev := runAppScript
	t.Cleanup(func() { runAppScript = prev })
	runAppScript = func(j appscript.Job) (string, error) {
		if j.Caller != "alice" || j.Args["title"] != "Dune" {
			t.Errorf("not run as alice's click with the title: caller %q args %v", j.Caller, j.Args)
		}
		return `{"message":"added","records":[{"id":"b1","title":"Dune"}]}`, nil
	}
	out, err := names["books_add"].Handler(context.Background(), map[string]any{"title": "Dune"})
	if err != nil || !strings.Contains(out, "added") {
		t.Fatalf("the call: %q %v", out, err)
	}
	if recs := appscript.ReadRecords(T.recordBase(AppSpec{Slug: "books", Owner: "alice"}, "alice"), "books"); len(recs) != 1 {
		t.Errorf("the record was not saved in alice's store: %v", recs)
	}

	// The pipeline runs to its end on the input given.
	prevPipe := appscript.AppRunPipeline
	t.Cleanup(func() { appscript.AppRunPipeline = prevPipe })
	appscript.AppRunPipeline = func(_ context.Context, spec AppSpec, caller, pipeline, input string) (string, error) {
		return "ran " + spec.Slug + " for " + caller + " on " + input, nil
	}
	if out, err := names["books_run_pipeline"].Handler(context.Background(), map[string]any{"input": "sci-fi"}); err != nil || out != "ran books for alice on sci-fi" {
		t.Errorf("the pipeline: %q %v", out, err)
	}
}

func keysOf(m map[string]AgentToolDef) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// An app's tools stop with its owner's access to apps.
func TestAppToolsStopWithTheOwnersAccessToApps(t *testing.T) {
	T := sharingTestApp(t)
	SaveAppSpec(AppSpec{Slug: "books", Name: "Reading List", Owner: "alice",
		Actions: []AppAction{{Name: "add", Script: "x", Expose: true}}})
	auth := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { AuthDB = prev })
	auth.Set(AuthTable, "user:alice", AuthUser{Username: "alice", Apps: []string{"/chat"}})
	auth.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	if got := T.exposedTools(nil, "alice"); len(got) != 0 {
		t.Errorf("someone without apps was offered app tools: %d", len(got))
	}
	auth.Set(AuthTable, "user:alice", AuthUser{Username: "alice", Apps: []string{"/chat", "/apps"}})
	if got := T.exposedTools(nil, "alice"); len(got) != 1 {
		t.Errorf("granted apps, offered %d tool(s), want 1", len(got))
	}
}
