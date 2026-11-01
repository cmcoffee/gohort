package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A search's results are settled the same way for every engine before its
// check: tags and entities out, a relative link or a bare DOI made whole, a
// result with no title titled by its link, and one with no link left out
// when others have one; when none has one, the check still refuses them.
func TestSearchResultsAreTidied(t *testing.T) {
	r := Result{"results": Value{Text: `[
		{"title":"<b>Drones</b> &amp; the law","url":"/opinion/12/","snippet":"Court <mark>ruled</mark> on drones."},
		{"title":"A paper","url":"10.1000/xyz123","snippet":""},
		{"title":"","url":"//c.example/x","snippet":"untitled"},
		{"title":"No link","url":"","snippet":"dropped"},
		{"title":"","url":"","snippet":"nothing"}]`}}
	got, _ := SearchResults(tidyResults(r, "https://api.example.org/v3/search"))
	want := []SearchResult{
		{Title: "Drones & the law", URL: "https://api.example.org/opinion/12/", Snippet: "Court ruled on drones."},
		{Title: "A paper", URL: "https://doi.org/10.1000/xyz123"},
		{Title: "https://c.example/x", URL: "https://c.example/x", Snippet: "untitled"},
	}
	if len(got) != len(want) {
		t.Fatalf("results: %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: %+v, want %+v", i, got[i], want[i])
		}
	}
	none := tidyResults(Result{"results": Value{Text: `[{"title":"A","url":"","snippet":"x"}]`}}, "https://api.example.org")
	if err := checkResults("results")(none); err == nil {
		t.Error("results read from the wrong field passed")
	}
}

// A plain search takes a query's site: filters out before it is sent; any
// other leaves the query as asked. Only a search may be plain.
func TestAPlainSearchDropsSiteFilters(t *testing.T) {
	world(t)
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Query().Get("q"))
		w.Write([]byte(`{"items":[{"t":"A","u":"https://a.example/1"}]}`))
	}))
	defer srv.Close()
	for _, plain := range []bool{true, false} {
		install(t, "finder", map[string]any{
			"provider": map[string]any{"transport": "http", "base_url": srv.URL, "auth": map[string]any{"none": true},
				"operations": map[string]any{"search": map[string]any{"path": "/s?q={query}"}}},
			"provides": []any{map[string]any{"capability": "web.search", "plain": plain, "steps": []any{map[string]any{"call": "search"}},
				"output": map[string]any{"results": map[string]any{"path": "items", "fields": map[string]any{"title": "t", "url": "u"}}}}},
		})
		if _, err := Search(context.Background(), "finder", "drone law site:courts.example.gov rulings"); err != nil {
			t.Fatal(err)
		}
	}
	if len(asked) != 2 || asked[0] != "drone law rulings" || asked[1] != "drone law site:courts.example.gov rulings" {
		t.Errorf("asked: %q", asked)
	}
	bad := templateSpec(t, "gemini-images", srv.URL)
	bad["provides"].([]any)[0].(map[string]any)["plain"] = true
	if err := validateSpec(t, bad); err == nil || !strings.Contains(err.Error(), "plain") {
		t.Errorf("a plain image maker: %v", err)
	}
}
