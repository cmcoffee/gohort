package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The research-source templates each install a spec that saves.
func TestTheResearchSourceTemplatesAreValid(t *testing.T) {
	for _, id := range []string{"westlaw-search", "courtlistener-search", "semantic-scholar-search", "core-search",
		"europe-pmc-search", "pubmed-search", "openalex-search", "edgar-search"} {
		spec := templateSpec(t, id, "https://api.example.org")
		if err := validateSpec(t, spec); err != nil {
			t.Errorf("%s: %v", id, err)
		}
		b := spec["provides"].([]any)[0].(map[string]any)
		if b["plain"] != true || b["research"] != nil {
			t.Errorf("%s: %v", id, b)
		}
	}
}

// runSource installs template id at the fake srv and searches it.
func runSource(t *testing.T, id string, srv *httptest.Server, query string) string {
	t.Helper()
	install(t, id, templateSpec(t, id, srv.URL))
	out, err := Search(context.Background(), id, query)
	if err != nil {
		t.Fatalf("%s: %v", id, err)
	}
	return out
}

// PubMed's built-in search asks for the most relevant ids, then the
// articles, and keeps them in the order the first call ranked them.
func TestPubMedFindsThenReadsInRankOrder(t *testing.T) {
	world(t)
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path+"?"+r.URL.RawQuery)
		switch r.URL.Path {
		case "/entrez/eutils/esearch.fcgi":
			w.Write([]byte(`{"esearchresult":{"idlist":["222","111"]}}`))
		case "/entrez/eutils/esummary.fcgi":
			w.Write([]byte(`{"result":{"uids":["111","222"],"111":{"title":"Second","source":"Lancet","pubdate":"2024 Jan"},"222":{"title":"First","source":"BMJ","pubdate":"2025 Mar 2"}}}`))
		}
	}))
	defer srv.Close()
	out := runSource(t, "pubmed-search", srv, "statins site:nih.gov elderly")
	if !strings.HasPrefix(out, "1. First\n   https://pubmed.ncbi.nlm.nih.gov/222/\n   [Published 2025-03-02] Published in: BMJ") || !strings.Contains(out, "2. Second\n   https://pubmed.ncbi.nlm.nih.gov/111/") {
		t.Errorf("results:\n%s", out)
	}
	if len(asked) != 2 || !strings.Contains(asked[0], "retmax=5") || !strings.Contains(asked[0], "term=statins+elderly") || !strings.Contains(asked[1], "id=222,111") {
		t.Errorf("asked: %q", asked)
	}
}

// OpenAlex's built-in search rebuilds an abstract from its word positions
// and links a work to its open copy, else its DOI.
func TestOpenAlexRebuildsAbstracts(t *testing.T) {
	world(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[
			{"title":"Open one","doi":"https://doi.org/10.1/a","publication_year":2023,"open_access":{"oa_url":"https://oa.example/a.pdf"},"abstract_inverted_index":{"agents":[1],"Small":[0],"work":[2]}},
			{"title":"Closed one","doi":"https://doi.org/10.1/b","publication_year":0,"open_access":{"oa_url":""}},
			{"title":"No link","doi":"","open_access":{"oa_url":""}}]}`))
	}))
	defer srv.Close()
	out := runSource(t, "openalex-search", srv, `"small agents"`)
	if !strings.HasPrefix(out, "1. Open one (2023)\n   https://oa.example/a.pdf\n   Small agents work") || !strings.Contains(out, "2. Closed one\n   https://doi.org/10.1/b") || strings.Contains(out, "No link") {
		t.Errorf("results:\n%s", out)
	}
}

// EDGAR's built-in search takes the field tags out of a model's query,
// names the deployment in its User-Agent, and links each filing.
func TestEDGARCleansTheQueryAndLinksFilings(t *testing.T) {
	world(t)
	var q, ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, ua = r.URL.Query().Get("q"), r.Header.Get("User-Agent")
		w.Write([]byte(`{"hits":{"hits":[{"_id":"0001-23-000045:acme-10k.htm","_source":{"display_names":["Acme Corp (CIK 0000123)"],"ciks":["0000123"],"form":"10-K","file_date":"2026-02-01","adsh":"0001-23-000045","period_ending":"2025-12-31","file_description":"Annual report"}}]}}`))
	}))
	defer srv.Close()
	out := runSource(t, "edgar-search", srv, `company:"Acme" AND form type:"10-K" AND`)
	if q != `"Acme" AND "10-K"` || !strings.HasPrefix(ua, "Gohort/") {
		t.Errorf("asked q=%q ua=%q", q, ua)
	}
	if !strings.HasPrefix(out, "1. Acme Corp: 10-K (Annual report)\n   https://www.sec.gov/Archives/edgar/data/123/000123000045/acme-10k.htm\n   [Published 2026-02-01] Period ending: 2025-12-31") {
		t.Errorf("results:\n%s", out)
	}
	if got := edgarQuery("ANDROID OR"); got != "ANDROID" {
		t.Errorf("a word starting with AND: %q", got)
	}
}

// A built-in search is named, not described, and gives only a search's
// results.
func TestABuiltinSearchIsNamedNotDescribed(t *testing.T) {
	spec := templateSpec(t, "pubmed-search", "https://api.example.org")
	ops := spec["provider"].(map[string]any)["operations"].(map[string]any)
	ops["search"] = map[string]any{"builtin": "bing"}
	if err := validateSpec(t, spec); err == nil || !strings.Contains(err.Error(), "edgar, openalex, pubmed") {
		t.Errorf("an unknown one: %v", err)
	}
	ops["search"] = map[string]any{"builtin": "pubmed", "path": "/x"}
	if err := validateSpec(t, spec); err == nil || !strings.Contains(err.Error(), "makes its own requests") {
		t.Errorf("with a path: %v", err)
	}
	ops["search"] = map[string]any{"builtin": "pubmed"}
	b := spec["provides"].([]any)[0].(map[string]any)
	b["capability"], b["research"], b["plain"] = "image.search", nil, false
	if err := validateSpec(t, spec); err == nil || !strings.Contains(err.Error(), "built-in search") {
		t.Errorf("for pictures: %v", err)
	}
}
