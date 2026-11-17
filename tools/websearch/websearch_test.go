package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/outbound"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A service connector that searches is a search provider: Web Search names
// it as service:<name>, the web_search tool asks it, and its results come
// back in the numbered shape the tool reads.
func TestAConnectorIsTheSearchEngine(t *testing.T) {
	savedRoot, savedAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	auth := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { RootDB, AuthDB = savedRoot, savedAuth })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "solar panels" {
			t.Errorf("the query went as %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"hits":[{"name":"Panels compared","href":"https://example.org/panels","text":"What each one costs."}]}`))
	}))
	defer srv.Close()
	spec := `{"provider":{"transport":"http","base_url":"` + srv.URL + `","auth":{"none":true},
		"operations":{"search":{"url":"/find?q={query}"}}},
		"provides":[{"capability":"web.search","steps":[{"call":"search"}],
		"output":{"results":{"path":"hits","fields":{"title":"name","url":"href","snippet":"text"}}}}]}`
	if err := SaveConnector(RootDB, Connector{Name: "finder", Kind: "service", Spec: []byte(spec)}); err != nil {
		t.Fatal(err)
	}
	ApproveConnector(RootDB, "finder")
	out, err := SearchWithProvider(SearchRequest{Query: "solar panels", Provider: "finder"})
	src := ParseSearchResults(out)
	if err != nil || len(src) != 1 || src[0].Title != "Panels compared" || src[0].URL != "https://example.org/panels" || src[0].Snippet != "What each one costs." {
		t.Fatalf("want the result as a search result: %q %v -> %+v", out, err, src)
	}
	if _, err := SearchWithProvider(SearchRequest{Query: "x", Provider: "elsewhere"}); err == nil || !strings.Contains(err.Error(), "not an engine here") {
		t.Errorf("an unknown provider: %v", err)
	}
	// A connector whose address is inside this network is not where a query
	// leaves: a private one to it is neither refused nor recorded.
	outbound.SetStore(&DBase{Store: kvlite.MemStore()})
	t.Cleanup(func() { outbound.SetStore(nil) })
	outbound.SetQueryMode(outbound.ModeRefuse)
	ctx := outbound.WithPrivateTerms(context.Background(), "solar panels")
	if _, err := SearchWithProvider(SearchRequest{Query: "solar panels", Provider: "finder", Ctx: ctx}); err != nil || len(outbound.Events(0, nil)) != 0 {
		t.Errorf("a private query to a connector inside the network: %v, %+v", err, outbound.Events(0, nil))
	}
}

// Since 2026-10-30 Google lets only the first few result links of a search be
// opened, and search APIs serving its results give the rest as
// google.com/goto links. Every provider's results are marked where that
// happens: the title and snippet stay for an agent to read, the link says why
// it is missing, and research, which reads only a web address there, passes
// over the result.
func TestGoogleRedirectLinksAreMarked(t *testing.T) {
	savedRoot, savedAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	auth := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { RootDB, AuthDB = savedRoot, savedAuth })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"organic":[
			{"title":"First","link":"https://example.org/one","snippet":"Opens."},
			{"title":"Fourth","link":"https://www.google.com/goto?id=abc123","snippet":"Was a goto."},
			{"title":"Fifth","link":"https://google.co.uk/url?q=https://example.org/five","snippet":"Was a url redirect."},
			{"title":"Sixth","link":"https://www.googleblog.com/goto","snippet":"Not Google's redirect."}]}`))
	}))
	defer srv.Close()
	spec := `{"provider":{"transport":"http","base_url":"` + srv.URL + `","auth":{"none":true},
		"operations":{"search":{"method":"POST","path":"/search","body":{"q":"{query}"}}}},
		"provides":[{"capability":"web.search","steps":[{"call":"search"}],
		"output":{"results":{"path":"organic","fields":{"title":"title","url":"link","snippet":"snippet"}}}}]}`
	if err := SaveConnector(RootDB, Connector{Name: "serpish", Kind: "service", Spec: []byte(spec)}); err != nil {
		t.Fatal(err)
	}
	ApproveConnector(RootDB, "serpish")
	out, err := SearchWithProvider(SearchRequest{Query: "x", Provider: "serpish"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "google.com/goto") || strings.Contains(out, "google.co.uk/url") {
		t.Errorf("a redirect link was handed on:\n%s", out)
	}
	if !strings.Contains(out, "2. Fourth\n   "+noUsableLink+"\n   Was a goto.") || !strings.Contains(out, "3. Fifth\n   "+noUsableLink) {
		t.Errorf("the redirected results lost their place, title or snippet:\n%s", out)
	}
	src := ParseSearchResults(out)
	if len(src) != 2 || src[0].URL != "https://example.org/one" || src[1].URL != "https://www.googleblog.com/goto" {
		t.Errorf("research reads only the links that open: %+v", src)
	}
	if got := service.MarkUnopenable("No results found."); got != "No results found." {
		t.Errorf("no results: %q", got)
	}
}

// noUsableLink is what a marked result's link line says (service.MarkUnopenable).
const noUsableLink = "(no usable link: Google gave a redirect that cannot be opened after the search)"

// The engines that were written in Go are refused by name, saying what to
// choose; SearXNG's shape is taken only from a peer.
func TestOldEnginesAreRefused(t *testing.T) {
	for _, req := range []SearchRequest{
		{Query: "x", Provider: "brave", APIKey: "k"},
		{Query: "x", Provider: "serper", APIKey: "k"},
		{Query: "x", Provider: "google", APIKey: "k:cx"},
		{Query: "x", Provider: "searxng", Endpoint: "http://127.0.0.1:1"},
		{Query: "x", Provider: "searxng", Endpoint: "http://127.0.0.1:1", Source: "local"},
	} {
		if _, err := SearchWithProvider(req); err == nil || !strings.Contains(err.Error(), "not an engine here") {
			t.Errorf("%+v: %v", req, err)
		}
	}
	// From a peer it is searched (here, an address nothing answers at).
	if _, err := SearchWithProvider(SearchRequest{Query: "x", Provider: "searxng", Endpoint: "http://127.0.0.1:1", Source: "peer:den"}); err == nil || strings.Contains(err.Error(), "not an engine here") {
		t.Errorf("a peer's search: %v", err)
	}
}

// Disabled under Web Search withdraws web_search from every catalog, and a
// search asked anyway is told it is off, never sent to some other engine.
func TestADisabledWebSearchIsWithdrawn(t *testing.T) {
	saved := LoadWebSearchConfigFunc
	t.Cleanup(func() { LoadWebSearchConfigFunc = saved })
	provider := "none"
	LoadWebSearchConfigFunc = func() WebSearchConfig { return WebSearchConfig{Provider: provider} }
	tool := &WebSearchTool{}
	if desc, params := tool.SchemaWithSession(&ToolSession{}); desc != "" || params != nil {
		t.Error("offered while disabled")
	}
	if _, err := SearchWithProvider(SearchRequest{Query: "x", Provider: "none"}); err == nil || !strings.Contains(err.Error(), "web search is disabled") {
		t.Errorf("a search while disabled: %v", err)
	}
	provider = "duckduckgo"
	if desc, _ := tool.SchemaWithSession(&ToolSession{}); desc == "" {
		t.Error("not offered once on")
	}
}

// A search the work names as private is in the Outbound record (watched, not
// refused, by default), made for the turn's agent; the tool's search sees
// the turn's context, which is where the names are. Refused, it says why and
// sends nothing.
func TestAPrivateSearchIsWatched(t *testing.T) {
	saved := LoadWebSearchConfigFunc
	t.Cleanup(func() { LoadWebSearchConfigFunc = saved })
	LoadWebSearchConfigFunc = func() WebSearchConfig { return WebSearchConfig{Provider: "duckduckgo"} }
	outbound.SetStore(&DBase{Store: kvlite.MemStore()})
	t.Cleanup(func() { outbound.SetStore(nil) })
	ctx := outbound.WithActor(outbound.WithPrivateTerms(context.Background(), "Kestrel Control"), outbound.Actor{AgentName: "Coder", Owner: "alice"})
	outbound.SetQueryMode(outbound.ModeRefuse)
	tool := &WebSearchTool{}
	if _, err := tool.RunWithSession(map[string]any{"query": "kestrel control reconcile backoff"}, &ToolSession{Ctx: ctx}); err == nil || !strings.Contains(err.Error(), "NOT SENT") {
		t.Fatalf("a refused private search: %v", err)
	}
	ev := outbound.Events(0, nil)
	if len(ev) != 1 || ev[0].Kind != outbound.KindSearch || ev[0].Dest != ProviderTitle("duckduckgo") || ev[0].Who() != "Coder (agent of alice)" {
		t.Fatalf("the record: %+v", ev)
	}
}
