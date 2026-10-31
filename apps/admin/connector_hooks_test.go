package admin

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/sourcehooks"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The API and RAG Source Hooks are listed as connectors, read from the hooks:
// what each gives, its query and host and where its key is, what it is used
// for, and its one endpoint. A paywall hook is not listed.
func TestSourceHooksAreConnectors(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	for _, h := range []sourcehooks.SourceHook{
		{Name: "caselaw", Type: sourcehooks.HookTypeAPI, Endpoint: "https://law.example/api/search", AuthType: "bearer", AuthKey: "k",
			QueryParam: "term", ResultsPath: "data.items", TitleField: "name", URLField: "link", SnippetField: "desc",
			TriggerDomains: []string{"legal"}, ExposeToLLM: true, ToolName: "caselaw_search"},
		{Name: "papers", Type: sourcehooks.HookTypeRAG, Endpoint: "http://rag.lan:8000/query", AlwaysActive: true},
		{Name: "news-walls", Type: sourcehooks.HookTypePaywall, Domains: []string{"news.example"}},
	} {
		sourcehooks.SaveSourceHook(db, h)
		t.Cleanup(func() { sourcehooks.DeleteSourceHook(db, h.Name) })
	}
	got := map[string]hookConnector{}
	for _, hc := range hookConnectors() {
		got[hc.hook.Name] = hc
	}
	if _, listed := got["news-walls"]; listed || len(got) != 2 {
		t.Errorf("listed: %v", got)
	}
	c := got["caselaw"]
	if c.name != "hook:caselaw" || c.wrapper != "Search results ⇐ term={query} ⇐ law.example (its own bearer key)" ||
		c.status != "Asked in research on: legal. Offered to agents as caselaw_search. Its key is kept in the hook, not an API credential. Set up under Admin > Extensions > Source Hooks." {
		t.Errorf("caselaw: %q / %q", c.wrapper, c.status)
	}
	if p := got["papers"]; p.wrapper != "Document passages ⇐ q={query} ⇐ rag.lan:8000 (no key)" || !strings.HasPrefix(p.status, "Asked in every research run.") {
		t.Errorf("papers: %q / %q", p.wrapper, p.status)
	}
	eps := hookEndpoints(c)
	if len(eps) != 1 || eps[0].Address != "GET https://law.example/api/search?term={query}" ||
		eps[0].Reads != "data.items[each]: title ← name; url ← link; snippet ← desc" || eps[0].UsedBy != "Asked in research on: legal. Offered to agents as caselaw_search." {
		t.Errorf("endpoints: %+v", eps)
	}
}
