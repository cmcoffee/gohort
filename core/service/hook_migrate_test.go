package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/migrate"
	"github.com/cmcoffee/gohort/core/sourcehooks"
)

// storeHook keeps a Source Hook as the hooks store did: by lowercased name
// with spaces as underscores, its key apart.
func storeHook(h sourcehooks.SourceHook) {
	key := strings.ToLower(strings.ReplaceAll(h.Name, " ", "_"))
	k := h.AuthKey
	h.AuthKey = ""
	RootDB.Set(sourceHookTable, key, h)
	if k != "" {
		RootDB.CryptSet(sourceHookTable, key+"_auth", k)
	}
}

func forgetHookCredentials(t *testing.T) {
	forgetCredentials(t, "news_wall_paywall")
}

// hookKey is the key connection name keeps, and how its spec sends it.
func hookKey(t *testing.T, name string) (ConnectionSecret, string) {
	t.Helper()
	c, _ := GetConnector(RootDB, name)
	sec, _ := GetConnectionSecret(RootDB, c, "api_key")
	s, _ := specOf(c)
	f, _ := s.secretField()
	return sec, f.Send
}

// An API hook becomes the search connector it described, its key kept by
// the connection, and a Research Sources entry with its topics and agent tool;
// the hook is gone. Its searches read as the hook read them, the key sent as
// the hook sent it. Run again, it changes nothing.
func TestAnAPIHookBecomesASearchConnectorAndASource(t *testing.T) {
	world(t)
	forgetHookCredentials(t)
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Write([]byte(`{"data":{"items":[{"name":"Smith v. Jones","link":"/case/1","blurb":"<em>Negligence</em> found."}]}}`))
	}))
	defer srv.Close()
	storeHook(sourcehooks.SourceHook{Name: "Case API", Type: sourcehooks.HookTypeAPI, Endpoint: srv.URL + "/v2/search?type=o",
		AuthType: sourcehooks.HookAuthAPIKey, AuthKey: "case-key", QueryParam: "term", ResultsPath: "data.items",
		TitleField: "name", URLField: "link", SnippetField: "blurb", TriggerDomains: []string{"legal"},
		ExposeToLLM: true, ToolDescription: "Court cases.", CostPerCall: 0.01})

	MigrateSourceHooks(RootDB)

	if keys := RootDB.Keys(sourceHookTable); len(keys) != 0 {
		t.Errorf("hooks left: %v", keys)
	}
	c, ok := GetConnector(RootDB, "case-api")
	if !ok || !c.Approved {
		t.Fatalf("the connector: %+v %v", c, ok)
	}
	rs, ok := GetResearchSource(RootDB, "case-api")
	if !ok || rs.Off || len(rs.Topics) != 1 || rs.Topics[0] != "legal" || rs.ToolName != "case_api_search" || rs.ToolDescription != "Court cases." {
		t.Errorf("the source: %+v %v", rs, ok)
	}
	if sec, send := hookKey(t, "case-api"); sec.Value != "case-key" || send != "header:X-API-Key" || len(sec.Addresses) != 1 || sec.Addresses[0] != srv.URL {
		t.Errorf("the key: %+v %q", sec, send)
	}
	out, err := Search(context.Background(), "case-api", "negligence site:courts.example.gov")
	if err != nil || out != "1. Smith v. Jones\n   "+srv.URL+"/case/1\n   Negligence found." {
		t.Errorf("a search: %q %v", out, err)
	}
	if got.URL.Path != "/v2/search" || got.URL.Query().Get("type") != "o" || got.URL.Query().Get("term") != "negligence" || got.Header.Get("X-API-Key") != "case-key" {
		t.Errorf("asked: %s %v", got.URL, got.Header)
	}
	if s, _ := specOf(c); s.Provides[0].Cost == nil || s.Provides[0].Cost.PerCall != 0.01 {
		t.Errorf("its price: %+v", s.Provides[0].Cost)
	}
	var m migrate.MigrationMarker
	if !RootDB.Get(migrate.MigrationsTable, "core:source_hooks_to_connectors:v1:", &m) || !m.Done || m.Error != "" {
		t.Errorf("Admin > Migrations shows: %+v", m)
	}
	storeHook(sourcehooks.SourceHook{Name: "Case API", Type: sourcehooks.HookTypeAPI, Endpoint: srv.URL})
	MigrateSourceHooks(RootDB)
	if len(RootDB.Keys(sourceHookTable)) == 0 {
		t.Error("the migration ran twice")
	}
}

// PubMed, OpenAlex and EDGAR hooks become connectors on their built-in
// searches (PubMed's key sent as api_key); CourtListener's token goes as
// "Token <key>" for its search and its opinions; a RAG hook reads its
// content where a result has no snippet; the hook that was the web search
// engine hands the engine to its connector; a switched-off hook leaves an
// off source and an unapproved connector; a paywall hook leaves a switched
// off credential for its sites; a hook with no endpoint is only removed.
func TestEveryKindOfHookMoves(t *testing.T) {
	world(t)
	forgetHookCredentials(t)
	for _, h := range []sourcehooks.SourceHook{
		{Name: "PubMed", Type: sourcehooks.HookTypeAPI, AuthType: sourcehooks.HookAuthAPIKey, AuthKey: "ncbi-key", TriggerDomains: []string{"medical"}},
		{Name: "SEC EDGAR", Type: sourcehooks.HookTypeAPI, TriggerDomains: []string{"financial"}, Disabled: true},
		{Name: "CourtListener", Type: sourcehooks.HookTypeAPI, Endpoint: "https://www.courtlistener.com/api/rest/v4/search/?type=o",
			AuthType: sourcehooks.HookAuthBearer, AuthKey: "cl-token", TriggerDomains: []string{"legal"}},
		{Name: "RAG Docs", Type: sourcehooks.HookTypeRAG, Endpoint: "https://rag.example/query", AuthType: sourcehooks.HookAuthBearer,
			AuthKey: "rag-key", AlwaysActive: true},
		{Name: "Engine", Type: sourcehooks.HookTypeAPI, Endpoint: "https://engine.example/s"},
		{Name: "News Wall", Type: sourcehooks.HookTypePaywall, AuthType: sourcehooks.HookAuthBearer, AuthKey: "wall-key", Domains: []string{"news.example", "https://more.example/"}},
		{Name: "Broken", Type: sourcehooks.HookTypeAPI, TriggerDomains: []string{"legal"}},
	} {
		storeHook(h)
	}
	RootDB.Set(SearchTable, "provider", "hook")
	RootDB.Set(SearchTable, "hook", "engine")
	if _, err := migrateSourceHooks(RootDB); err != nil {
		t.Fatal(err)
	}

	pubmed, _ := GetConnector(RootDB, "pubmed")
	if s, _ := specOf(pubmed); s.Provider.Operations["search"].Builtin != "pubmed" || pubmed.Template != "pubmed-search" || s.Provider.Auth.Secret != "api_key" {
		t.Errorf("PubMed: %s", pubmed.Spec)
	}
	if sec, send := hookKey(t, "pubmed"); sec.Value != "ncbi-key" || send != "query:api_key" {
		t.Errorf("PubMed's key: %+v %q", sec, send)
	}
	edgar, _ := GetConnector(RootDB, "sec-edgar")
	if rs, _ := GetResearchSource(RootDB, "sec-edgar"); edgar.Approved || !rs.Off {
		t.Errorf("a switched-off hook: approved %v, source %+v", edgar.Approved, rs)
	}
	if sec, send := hookKey(t, "courtlistener"); sec.Value != "Token cl-token" || send != "header:Authorization" {
		t.Errorf("CourtListener's token: %+v %q", sec, send)
	}
	rag, _ := GetConnector(RootDB, "rag-docs")
	if !strings.Contains(string(rag.Spec), `"snippet":"snippet|content"`) {
		t.Errorf("the RAG hook's reading: %s", rag.Spec)
	}
	if rs, _ := GetResearchSource(RootDB, "rag-docs"); !rs.Always {
		t.Errorf("the RAG source: %+v", rs)
	}
	var provider, hook string
	RootDB.Get(SearchTable, "provider", &provider)
	if provider != "service:engine" || RootDB.Get(SearchTable, "hook", &hook) {
		t.Errorf("web search: %q, hook %q", provider, hook)
	}
	if _, ok := GetResearchSource(RootDB, "engine"); ok {
		t.Error("a hook asked by nothing but Web Search became a source")
	}
	wall, ok := Secure().Load("news_wall_paywall")
	if !ok || !wall.Disabled || wall.BaseURL != "https://news.example" || len(wall.AlsoCovers) != 1 || wall.AlsoCovers[0] != "https://more.example" {
		t.Errorf("the paywall's credential: %+v %v", wall, ok)
	}
	if _, ok := GetConnector(RootDB, "broken"); ok {
		t.Error("a hook with no endpoint became a connector")
	}
	if keys := RootDB.Keys(sourceHookTable); len(keys) != 0 {
		t.Errorf("hooks left: %v", keys)
	}
}

// A search connector whose spec held its research topics and agent tool,
// before they became Research Sources, keeps them there: the spec loses
// them and saves again, and an approved connector's source is on.
func TestResearchSettingsLeaveTheSpec(t *testing.T) {
	world(t)
	spec := templateSpec(t, "europe-pmc-search", "https://api.example.org")
	b := spec["provides"].([]any)[0].(map[string]any)
	b["research"] = map[string]any{"topics": []any{"medical"}, "always": false}
	b["tool"] = map[string]any{"name": "pmc_search", "description": "Biomedical papers."}
	raw, _ := json.Marshal(spec)
	// Saved as it was before the move, past today's check.
	RootDB.Set("connectors", "pmc", Connector{Name: "pmc", Kind: Kind, Spec: raw, Approved: true})
	if _, ok := GetConnector(RootDB, "pmc"); !ok {
		t.Fatal("the connector was not stored where it is read")
	}
	if n, err := migrateResearchSettings(RootDB); err != nil || n != 2 {
		t.Fatalf("migrate: %d %v", n, err)
	}
	c, _ := GetConnector(RootDB, "pmc")
	if _, err := specOf(c); err != nil || strings.Contains(string(c.Spec), "research") || !c.Approved {
		t.Errorf("the spec after: %s %v", c.Spec, err)
	}
	if rs, ok := GetResearchSource(RootDB, "pmc"); !ok || rs.Off || rs.ToolName != "pmc_search" || rs.Topics[0] != "medical" {
		t.Errorf("the source: %+v %v", rs, ok)
	}
}

// A Source Hook in a bundle imports as its search connector, unapproved, and
// an off source, the connection waiting for its key, which never travels;
// nothing exports as one.
func TestABundledHookImportsAsAConnector(t *testing.T) {
	world(t)
	forgetHookCredentials(t)
	recipe, _ := json.Marshal(sourcehooks.SourceHook{Name: "Case API", Type: sourcehooks.HookTypeAPI, Endpoint: "https://cases.example/search",
		AuthType: sourcehooks.HookAuthBearer, AuthKey: "leaked", TriggerDomains: []string{"legal"}})
	name, skip, err := sourceHookArtifact{}.ImportArtifact(RootDB, recipe, "")
	if err != nil || skip != "" || name != "case-api" {
		t.Fatalf("import: %q %q %v", name, skip, err)
	}
	c, _ := GetConnector(RootDB, "case-api")
	rs, _ := GetResearchSource(RootDB, "case-api")
	if s, _ := specOf(c); c.Approved || !rs.Off || s.Provider.Auth.Secret != "api_key" {
		t.Errorf("landed: connector approved %v, source %+v, spec %s", c.Approved, rs, c.Spec)
	}
	if HasSecret(c, "api_key") {
		t.Error("the bundle's connection has a key to send")
	}
	if _, err := (sourceHookArtifact{}).ExportArtifact(RootDB, "case-api", ""); err == nil {
		t.Error("a hook exported")
	}
}
