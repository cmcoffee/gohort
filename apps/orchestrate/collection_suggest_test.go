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

// suggestFixture stands in for the searches, and clears what earlier tests
// found.
func suggestFixture(t *testing.T, results map[string]string, queries ...string) *dispatchFixture {
	t.Helper()
	df := curateFixture(t, func(context.Context, string, string) (string, error) { return "", nil })
	prevSearch, prevQueries := suggestSearch, suggestQueriesFor
	suggestSearch = func(q string) string { return results[q] }
	suggestQueriesFor = func(context.Context, *OrchestrateApp, Collection, int) []string { return queries }
	suggested.Lock()
	prevFound := suggested.m
	suggested.m = map[string]suggestionSet{}
	suggested.Unlock()
	t.Cleanup(func() {
		suggestSearch, suggestQueriesFor = prevSearch, prevQueries
		suggested.Lock()
		suggested.m = prevFound
		suggested.Unlock()
	})
	return df
}

// Find sources offers what the searches found that is not already in the
// collection, once each, without the sites its rules say to skip, with the
// sites they prefer first, each with its site and the line the search showed.
func TestFindSourcesOffersWhatIsNewByTheCollectionsRules(t *testing.T) {
	df := suggestFixture(t, map[string]string{
		"q1": "1. Blog post\n   https://someblog.example/post\n   A take on it.\n\n" +
			"2. The manual\n   https://docs.vendor.example/manual\n   The official manual.\n\n" +
			"3. Already in\n   https://have.example/page\n   x\n",
		"q2": "1. The manual again\n   https://docs.vendor.example/manual/\n   dup\n\n" +
			"2. Skipped\n   https://www.skipme.example/a\n   no\n\n" +
			"3. Listed already\n   https://listed.example/a\n   no\n",
	}, "q1", "q2")
	c := Collection{ID: "col-s", Name: "Vendor", Owner: "u",
		FilterRules:  "Prefer vendor.example\nSkip skipme.example",
		IngestedURLs: []string{"https://have.example/page"},
		Articles:     []string{"https://listed.example/a"}}
	var notes []string
	out, err := df.app.findSources(context.Background(), c, func(s string) { notes = append(notes, s) })
	if err != nil {
		t.Fatal(err)
	}
	set, ok := suggestionsFor(c)
	if !ok || len(set.sources) != 2 {
		t.Fatalf("offered %+v", set.sources)
	}
	first := set.sources[0]
	if first.URL != "https://docs.vendor.example/manual" || first.Site != "docs.vendor.example" || first.Line != "The official manual." || first.Title != "The manual" {
		t.Errorf("the preferred site should come first, with its site and line: %+v", first)
	}
	if set.sources[1].URL != "https://someblog.example/post" {
		t.Errorf("second: %+v", set.sources[1])
	}
	if !strings.Contains(out, "Found 2 sources") {
		t.Errorf("outcome %q", out)
	}
	if len(notes) == 0 || !strings.Contains(notes[len(notes)-1], "of 2 searches") {
		t.Errorf("no progress reported: %v", notes)
	}

	// Once one is in the collection it is no longer offered.
	c.IngestedURLs = append(c.IngestedURLs, "https://someblog.example/post")
	if set, _ := suggestionsFor(c); len(set.sources) != 1 {
		t.Errorf("a source already in is still offered: %+v", set.sources)
	}

	// A collection nothing can be written for says what to do.
	suggestQueriesFor = func(context.Context, *OrchestrateApp, Collection, int) []string { return nil }
	if _, err := df.app.findSources(context.Background(), c, func(string) {}); err == nil || !strings.Contains(err.Error(), "searches") {
		t.Errorf("no searches: %v", err)
	}
}

// The candidates reach the page as a checklist, each a title with its site and
// line; with none yet the page says how to get some. Ticking adds only what
// the search offered, puts it on the list to fetch, and fetches it in the
// background, with progress and an outcome that says what could not be
// fetched.
func TestTickedSourcesAreListedAndFetched(t *testing.T) {
	df := suggestFixture(t, nil)
	c := Collection{ID: "col-t", Name: "T", Owner: "u", Articles: []string{"an older description"}}
	saveCollection(df.udb, c)

	got, err := df.app.collectionSuggestFields(curateCall(df, http.MethodGet, "u", "col-t"))
	if err != nil {
		t.Fatal(err)
	}
	if f := got.([]ui.FormField); len(f) != 1 || f[0].Type != "header" {
		t.Errorf("with nothing found yet: %+v", f)
	}

	// Loopback: refused before any connection, so nothing leaves the test.
	const offered = "http://127.0.0.1:9/manual"
	suggested.Lock()
	suggested.m["col-t"] = suggestionSet{found: time.Now(), sources: []suggestedSource{{URL: offered, Title: "Manual", Site: "127.0.0.1", Line: "The manual."}}}
	suggested.Unlock()
	got, _ = df.app.collectionSuggestFields(curateCall(df, http.MethodGet, "u", "col-t"))
	f := got.([]ui.FormField)
	if len(f) != 1 || f[0].Type != "checklist" || len(f[0].Options) != 1 || f[0].Options[0].Value != offered || f[0].Options[0].Help != "127.0.0.1: The manual." {
		t.Fatalf("the checklist: %+v", f)
	}

	add := func(picks ...string) (any, error) {
		raw := `["` + strings.Join(picks, `","`) + `"]`
		return df.app.collectionSuggestAdd(curateCall(df, http.MethodPost, "u", "col-t"), struct {
			Pick json.RawMessage `json:"pick"`
		}{Pick: json.RawMessage(raw)})
	}
	if _, err := add("https://not-offered.example/x"); err == nil {
		t.Error("a link the search never offered was added")
	}
	if _, err := add(offered); err != nil {
		t.Fatal(err)
	}
	saved, _ := loadCollection(df.udb, "u", "col-t")
	if len(saved.Articles) != 2 || saved.Articles[1] != offered {
		t.Errorf("the ticked source is not on the list to fetch: %v", saved.Articles)
	}
	var end map[string]any
	for i := 0; i < 400; i++ {
		out, err := df.app.collectionSuggestProgress(curateCall(df, http.MethodGet, "u", "col-t"))
		if err != nil {
			t.Fatal(err)
		}
		if end = out.(map[string]any); end["outcome"] != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if end["outcome"] == nil {
		t.Fatalf("the add never ended: %v", end)
	}
	if o := end["outcome"].(string); !strings.Contains(o, "1 could not be fetched") {
		t.Errorf("outcome %q", o)
	}
}
