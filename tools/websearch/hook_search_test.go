package websearch

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/sourcehooks"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A Source Hook as the search engine: its API answers, read the way the
// hook reads it, into the numbered results every provider gives, and every
// search goes to the API itself, never the hook's caches (which hold what
// earlier queries gathered, and a peer borrowing this search must not get).
func TestASourceHookIsTheSearchEngine(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.URL.Query().Get("term") != "solar panels" {
			t.Errorf("the query went as %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"items":[{"name":"Panels compared","link":"https://example.org/panels","desc":"What each one costs."}]}}`))
	}))
	defer srv.Close()
	db := &DBase{Store: kvlite.MemStore()}
	// The caches on, as in production: a search answered from them would
	// not reach the server the second time.
	sourcehooks.SetHookCacheDB(db)
	defer sourcehooks.SetHookCacheDB(nil)
	hook := sourcehooks.SourceHook{Name: "mysearch", Type: sourcehooks.HookTypeAPI, Endpoint: srv.URL + "/search",
		QueryParam: "term", ResultsPath: "data.items", TitleField: "name", URLField: "link", SnippetField: "desc"}
	sourcehooks.SaveSourceHook(db, hook)
	defer sourcehooks.DeleteSourceHook(db, "mysearch")

	for i := 0; i < 2; i++ {
		out, err := SearchWithProvider(SearchRequest{Query: "solar panels", Provider: "hook", Hook: "mysearch"})
		if err != nil {
			t.Fatal(err)
		}
		src := ParseSearchResults(out)
		if len(src) != 1 || src[0].Title != "Panels compared" || src[0].URL != "https://example.org/panels" || src[0].Snippet != "What each one costs." {
			t.Fatalf("want the result as a search result: %q -> %+v", out, src)
		}
	}
	if hits != 2 {
		t.Errorf("every search should ask the API itself: %d calls for 2 searches", hits)
	}

	// Switched off, or not an API hook: refused, never searched elsewhere.
	hook.Disabled = true
	sourcehooks.SaveSourceHook(db, hook)
	if _, err := SearchWithProvider(SearchRequest{Query: "x", Provider: "hook", Hook: "mysearch"}); err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Errorf("a switched-off hook should be refused: %v", err)
	}
	sourcehooks.SaveSourceHook(db, sourcehooks.SourceHook{Name: "walls", Type: sourcehooks.HookTypePaywall, Domains: []string{"example.com"}})
	defer sourcehooks.DeleteSourceHook(db, "walls")
	if _, ok := sourcehooks.SearchHook("walls"); ok {
		t.Error("a paywall hook is not a search engine")
	}
}

// Picked as the search engine, a hook stops being a source hook of its own:
// not in the active list, not routed to by topic, not a tool, not grantable
// by name; and web_search still reaches it.
func TestTheEngineHookLeavesTheSourceHooks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[{"title":"A","url":"https://example.org/a","snippet":"s"}]}`))
	}))
	defer srv.Close()
	db := &DBase{Store: kvlite.MemStore()}
	h := sourcehooks.SourceHook{Name: "mysearch", Type: sourcehooks.HookTypeAPI, Endpoint: srv.URL,
		ResultsPath: "results", AlwaysActive: true, ExposeToLLM: true}
	sourcehooks.SaveSourceHook(db, h)
	defer sourcehooks.DeleteSourceHook(db, "mysearch")
	prev := sourcehooks.EngineHook
	defer func() { sourcehooks.EngineHook = prev }()

	engine := ""
	sourcehooks.EngineHook = func() string { return engine }
	used := func() (active, topic, tool, byName bool) {
		// The query_source catalog lists the hooks it can dispatch to.
		tool = strings.Contains(RenderAvailableSourcesBlock(db), "mysearch")
		for _, x := range sourcehooks.ActiveSourceHooks() {
			active = active || x.Name == "mysearch"
		}
		for _, x := range sourcehooks.HooksForTopic(nil) {
			topic = topic || x.Name == "mysearch"
		}
		for _, d := range BuildSourceHookAgentToolDefs(db) {
			tool = tool || d.Tool.Name == "mysearch_search"
		}
		_, byName = SourceHookToolDefByName("mysearch_search")
		return
	}
	if a, tp, tl, bn := used(); !a || !tp || !tl || !bn {
		t.Fatalf("as a source hook it should be in every use: active=%v topic=%v tool=%v byname=%v", a, tp, tl, bn)
	}
	engine = "mysearch"
	if a, tp, tl, bn := used(); a || tp || tl || bn {
		t.Errorf("as the engine it should be in none of them: active=%v topic=%v tool=%v byname=%v", a, tp, tl, bn)
	}
	if out, err := SearchWithProvider(SearchRequest{Query: "q", Provider: "hook", Hook: "mysearch"}); err != nil || !strings.Contains(out, "https://example.org/a") {
		t.Errorf("web_search should still reach it: %v %q", err, out)
	}
}

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
	out, err := SearchWithProvider(SearchRequest{Query: "solar panels", Provider: "service:finder"})
	src := ParseSearchResults(out)
	if err != nil || len(src) != 1 || src[0].Title != "Panels compared" || src[0].URL != "https://example.org/panels" || src[0].Snippet != "What each one costs." {
		t.Fatalf("want the result as a search result: %q %v -> %+v", out, err, src)
	}
	if _, err := SearchWithProvider(SearchRequest{Query: "x", Provider: "elsewhere"}); err == nil || !strings.Contains(err.Error(), "unknown search provider") {
		t.Errorf("an unknown provider: %v", err)
	}
}
