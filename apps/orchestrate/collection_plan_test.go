package orchestrate

// A collection's plan (collection_plan.go): drafted from the description and
// kept on the collection, edited, and carried out the same way whether it
// runs once (Run now) or on a schedule; Curation by Agent hands the same plan
// to the agent.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// planFixture is suggestFixture with search reported available.
func planFixture(t *testing.T, results map[string]string, queries ...string) *dispatchFixture {
	t.Helper()
	df := suggestFixture(t, results, queries...)
	prev := planSearchReady
	planSearchReady = func() bool { return true }
	t.Cleanup(func() { planSearchReady = prev })
	return df
}

func waitRun(t *testing.T, df *dispatchFixture, id string) map[string]any {
	t.Helper()
	var last map[string]any
	for i := 0; i < 400; i++ {
		out, err := df.app.collectionCurateProgress(curateCall(df, http.MethodGet, "u", id))
		if err != nil {
			t.Fatal(err)
		}
		if last = out.(map[string]any); last["outcome"] != nil {
			return last
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the run never ended: %v", last)
	return nil
}

// A drafted plan is kept on the collection, not in memory: its search terms
// and documents per run, read back for the page with what that comes to.
// Drafting again is Redraft. A draft that writes nothing changes nothing.
func TestAPlanIsDraftedAndKeptOnTheCollection(t *testing.T) {
	df := planFixture(t, nil, "postgres replication setup", "postgres backup tools")
	c := Collection{ID: "col-f", Name: "PG", Owner: "u", Description: "PostgreSQL administration."}
	saveCollection(df.udb, c)

	items, _ := df.app.collectionPlanDraftItems(curateCall(df, http.MethodGet, "u", "col-f"))
	if it := items.([]map[string]any); len(it) != 1 || it[0]["item"] != "Draft a plan" {
		t.Errorf("before a plan: %v", it)
	}
	out, err := df.app.draftPlan(context.Background(), df.udb, c, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "2 search terms, up to 20 documents a run") {
		t.Errorf("the draft's outcome should say its size: %q", out)
	}
	saved, _ := loadCollection(df.udb, "u", "col-f")
	if len(saved.SearchTerms) != 2 || saved.DocsPerRun != 20 {
		t.Fatalf("the plan is not on the collection: %+v", saved)
	}
	vals, _ := df.app.collectionPlan(curateCall(df, http.MethodGet, "u", "col-f"))
	if v := vals.(map[string]any); v["search_terms"] != "postgres replication setup\npostgres backup tools" || v["docs_per_run"] != 20 {
		t.Errorf("the plan's values: %v", v)
	}
	items, _ = df.app.collectionPlanDraftItems(curateCall(df, http.MethodGet, "u", "col-f"))
	if it := items.([]map[string]any); it[0]["item"] != "Redraft" {
		t.Errorf("with a plan, drafting again is Redraft: %v", it)
	}

	collectionQueriesFor = func(context.Context, *OrchestrateApp, Collection, int) []string { return nil }
	if _, err := df.app.draftPlan(context.Background(), df.udb, saved, func(string) {}); err == nil {
		t.Error("a draft with no search terms succeeded")
	}
	if again, _ := loadCollection(df.udb, "u", "col-f"); len(again.SearchTerms) != 2 {
		t.Errorf("a failed draft changed the plan: %v", again.SearchTerms)
	}
}

// The plan is saved as edited: its search terms, its rules (the collection's
// filter rules), and documents per run held to the cap. More search terms
// than a run may make are refused.
func TestThePlanIsSavedAsEdited(t *testing.T) {
	df := planFixture(t, nil)
	saveCollection(df.udb, Collection{ID: "col-e", Name: "E", Owner: "u"})
	save := func(terms, rules any, docs any) error {
		tj, _ := json.Marshal(terms)
		rj, _ := json.Marshal(rules)
		dj, _ := json.Marshal(docs)
		_, err := df.app.collectionPlanSave(curateCall(df, http.MethodPost, "u", "col-e"), struct {
			SearchTerms json.RawMessage `json:"search_terms"`
			FilterRules json.RawMessage `json:"filter_rules"`
			DocsPerRun  json.RawMessage `json:"docs_per_run"`
		}{tj, rj, dj})
		return err
	}
	if err := save("first term\nsecond term", []string{"Prefer docs.vendor.example", "Skip forums"}, "100000"); err != nil {
		t.Fatal(err)
	}
	c, _ := loadCollection(df.udb, "u", "col-e")
	if len(c.SearchTerms) != 2 || c.FilterRules != "Prefer docs.vendor.example\nSkip forums" || c.DocsPerRun != autofillMaxDocs() {
		t.Errorf("saved: %+v", c)
	}
	var many []string
	for i := 0; i <= autofillMaxQueries(); i++ {
		many = append(many, "term number "+string(rune('a'+i)))
	}
	if save(strings.Join(many, "\n"), "", 5) == nil {
		t.Error("more search terms than a run may make were saved")
	}
	if err := save("one", "", 0); err != nil {
		t.Fatal(err)
	}
	if c, _ := loadCollection(df.udb, "u", "col-e"); c.DocsPerRun != planDocsDefault {
		t.Errorf("no count should mean the default: %d", c.DocsPerRun)
	}
}

// Run now carries out the plan the same way whatever the collection stored
// for curation before (nothing, Auto-Curation's marker, or an agent once in
// charge): the search terms run, what they find is fetched, and the run
// reports how it ended. No agent runs.
func TestRunNowCarriesOutThePlan(t *testing.T) {
	// Loopback: refused before any connection, so nothing leaves the test.
	df := planFixture(t, map[string]string{
		"edited search": "1. A page\n   http://127.0.0.1:9/a\n   line\n\n2. Another\n   http://127.0.0.1:9/b\n   line\n",
	})
	for i, curator := range []string{"", collectionAutoCurator, df.agent.ID} {
		id := "col-r" + string(rune('0'+i))
		saveCollection(df.udb, Collection{ID: id, Name: "R", Owner: "u", CuratorAgent: curator, SearchTerms: []string{"edited search"}, DocsPerRun: 5})
		if _, err := df.app.collectionCurateStart(curateCall(df, http.MethodPost, "u", id), struct{}{}); err != nil {
			t.Fatal(err)
		}
		end := waitRun(t, df, id)
		if o := end["outcome"].(string); !strings.Contains(o, "2 pages could not be used") {
			t.Errorf("curator %q: outcome %q", curator, o)
		}
	}
	if n := df.llm.Calls(); n != 0 {
		t.Errorf("a model was asked %d times: no agent runs the plan, and these pages never reached the check", n)
	}
}

// A run takes the links agents suggested through the same check, adds what
// passes and closes it; a suggestion with no link waits for the owner. A page
// that supersedes a document is added, and the run names the old one.
func TestARunChecksSuggestedLinksAndNamesWhatItSupersedes(t *testing.T) {
	const good, newer = "https://docs.vendor.example/a", "https://docs.vendor.example/v4"
	f := vetFixture(t, map[string]string{good: "Backups go to a dated archive.", newer: "As of version 4 the audit log is on by default."},
		FakeTurn{Content: vetReply("adds", 0, "")},
		FakeTurn{Content: vetReply("supersedes", 1, "v4 on, v3 off")})
	prevSearch := collectionWebSearch
	collectionWebSearch = func(string) string { return "1. Newer\n   " + newer + "\n   x\n" }
	t.Cleanup(func() { collectionWebSearch = prevSearch })
	f.T.addSuggestion(f.c, collectionSuggestion{Text: "add the backup page", Source: good})
	f.T.addSuggestion(f.c, collectionSuggestion{Text: "mention that restores need a stopped service"})
	f.c.SearchTerms = []string{"audit log default"}

	out, err := f.T.runPlan(context.Background(), f.store("alice"), f.c, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := loadCollection(f.store("alice"), "alice", "col-v")
	if !collectionHasURL(saved, good) || !collectionHasURL(saved, newer) {
		t.Errorf("added: %v", saved.IngestedURLs)
	}
	if left := f.T.listSuggestions(f.c); len(left) != 1 || left[0].Source != "" {
		t.Errorf("the linked suggestion should be closed, the other left: %+v", left)
	}
	if !strings.Contains(out, `Newer versions were found of "Configuration reference"`) {
		t.Errorf("the run should name what it supersedes: %q", out)
	}
}

// Auto-fill's own searching is the plan now: the old endpoint does Fetch
// these only, and anything else is told where it went.
func TestAutofillOnlyFetchesTheListedArticles(t *testing.T) {
	df := planFixture(t, nil)
	saveCollection(df.udb, Collection{ID: "col-x", Name: "X", Owner: "u", Articles: []string{"http://127.0.0.1:9/listed"}})
	if _, err := df.app.collectionAutofill(curateCall(df, http.MethodPost, "u", "col-x"), &collectionAutofillBody{MaxDocs: 5}); err == nil || !strings.Contains(err.Error(), "plan") {
		t.Errorf("a search through the old endpoint: %v", err)
	}
	out, err := df.app.collectionAutofill(curateCall(df, http.MethodPost, "u", "col-x"), &collectionAutofillBody{ArticlesOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if m := out.(map[string]any); m["failed"] != 1 {
		t.Errorf("Fetch these only should try the listed article: %v", m)
	}
}
