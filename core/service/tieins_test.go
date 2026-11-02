package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// searchConnector saves a search connector named name, reaching srvURL (or
// nowhere).
func searchConnector(t *testing.T, name, srvURL string) {
	t.Helper()
	if srvURL == "" {
		srvURL = "https://find.example.test"
	}
	spec := map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": srvURL, "auth": map[string]any{"none": true},
			"operations": map[string]any{"find": map[string]any{"url": "/find?q={query}"}}},
		"provides": []any{map[string]any{"capability": "web.search", "steps": []any{map[string]any{"call": "find"}},
			"output": map[string]any{"results": map[string]any{"path": "hits", "fields": map[string]any{"title": "name", "url": "href"}}}}},
	}
	raw, _ := json.Marshal(spec)
	if err := SaveConnector(RootDB, Connector{Name: name, Kind: Kind, Spec: raw}); err != nil {
		t.Fatal(err)
	}
}

// searchServer answers every search with one result.
func searchServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"hits":[{"name":"A","href":"https://a.example/1"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A connector is referenced by the pages that name it: Web Search's engine,
// a Research Source that is on, a flow's step, the image or music lists
// offered to agents. Taken off every page, it is referenced by none, and
// only then can it be deleted.
func TestAConnectorIsReferencedByThePagesThatNameIt(t *testing.T) {
	world(t)
	for _, n := range []string{"engine", "papers", "idle", "pics", "tunes", "stepper"} {
		searchConnector(t, n, "")
	}
	RootDB.Set(SearchTable, "provider", "service:engine")
	RootDB.Set(ImageTable, "agents", []string{"pics"})
	RootDB.Set(MusicTable, "agents", []string{"tunes"})
	RootDB.Set(FlowTable, "f", Flow{Name: "f", Steps: []FlowStep{{ID: "a", Connector: "stepper", Capability: "web.search"}}})
	if err := SaveResearchSource(RootDB, ResearchSource{Connector: "papers", Always: true}); err != nil {
		t.Fatal(err)
	}
	uses := TieIns(RootDB)
	for name, page := range map[string]string{"engine": "Web Search", "papers": "Research Sources",
		"pics": "Image Generation (offered to agents)", "tunes": "Music (offered to agents)", "stepper": "Flow f"} {
		if !slices.Contains(uses[name], page) || !Referenced(RootDB, name) {
			t.Errorf("%s: pages %v, want %s", name, uses[name], page)
		}
	}
	if Referenced(RootDB, "idle") || len(uses["idle"]) != 0 {
		t.Errorf("a connector no page names is referenced: %v", uses["idle"])
	}
	if err := RemoveConnector(RootDB, "engine"); err == nil || !strings.Contains(err.Error(), "referenced by Web Search") {
		t.Errorf("a referenced connector was deleted: %v", err)
	}

	// Turning the research source off, or naming another engine, leaves the
	// connector referenced by nothing, and it can be deleted.
	if err := SaveResearchSource(RootDB, ResearchSource{Connector: "papers", Always: true, Off: true}); err != nil {
		t.Fatal(err)
	}
	RootDB.Set(SearchTable, "provider", "duckduckgo")
	if Referenced(RootDB, "papers") || Referenced(RootDB, "engine") {
		t.Errorf("taken off their pages, still referenced: %v", TieIns(RootDB))
	}
	if err := RemoveConnector(RootDB, "engine"); err != nil {
		t.Errorf("an unreferenced connector: %v", err)
	}
}

// A connector has no approval: saved, it is served and answers whatever
// reaches it. A change by someone not an administrator holds it, so no call
// reaches it, until an administrator accepts the change by saving a page
// that names it.
func TestAChangeIsHeldUntilAnAdministratorAccepts(t *testing.T) {
	world(t)
	srv := searchServer(t)
	searchConnector(t, "engine", srv.URL)
	in := map[string]any{"query": "q"}
	if _, err := Call(context.Background(), nil, "engine", "web.search", in); err != nil {
		t.Fatalf("saved, it does not answer: %v", err)
	}
	MarkChanged(RootDB, "engine", "someone")
	if _, err := Call(context.Background(), nil, "engine", "web.search", in); err == nil || !strings.Contains(err.Error(), "change waiting") {
		t.Errorf("a change waiting on acceptance: %v", err)
	}
	if ch, ok := PendingChange(RootDB, "engine"); !ok || ch.By != "someone" {
		t.Errorf("the change waiting: %+v %v", ch, ok)
	}
	AcceptChanges(RootDB, "other")
	if _, err := Call(context.Background(), nil, "engine", "web.search", in); err == nil {
		t.Error("accepting another connector accepted this one")
	}
	AcceptChanges(RootDB, "engine")
	if _, err := Call(context.Background(), nil, "engine", "web.search", in); err != nil {
		t.Errorf("accepted, still held: %v", err)
	}
	if _, ok := PendingChange(RootDB, "engine"); ok {
		t.Error("accepted, still waiting")
	}
}

// An administrator's test on a settings page reaches the connector its form
// names while a change holds it, and only that test does: a call without
// its context, or of another held connector, is still refused.
func TestAnAdministratorsPreviewReachesOnlyItsConnector(t *testing.T) {
	world(t)
	srv := searchServer(t)
	searchConnector(t, "trial", srv.URL)
	searchConnector(t, "other", srv.URL)
	MarkChanged(RootDB, "trial", "someone")
	MarkChanged(RootDB, "other", "someone")
	in := map[string]any{"query": "q"}
	if _, err := Call(context.Background(), nil, "trial", "web.search", in); err == nil || !strings.Contains(err.Error(), "change waiting") {
		t.Fatalf("a held connector answered: %v", err)
	}
	ctx, end := Preview(context.Background(), "trial")
	if _, err := Call(ctx, nil, "trial", "web.search", in); err != nil {
		t.Errorf("the preview did not reach it: %v", err)
	}
	if _, err := Call(context.Background(), nil, "trial", "web.search", in); err == nil {
		t.Error("a call outside the preview reached it while the preview ran")
	}
	if _, err := Call(ctx, nil, "other", "web.search", in); err == nil {
		t.Error("the preview reached a connector it does not name")
	}
	end()
	if _, err := Call(ctx, nil, "trial", "web.search", in); err == nil {
		t.Error("the preview's context still reaches it after it ended")
	}

	// A test of several connectors (a flow's steps) previews each.
	ctx, endTrial := Preview(context.Background(), "trial")
	both, endOther := Preview(ctx, "other")
	for _, n := range []string{"trial", "other"} {
		if _, err := Call(both, nil, n, "web.search", in); err != nil {
			t.Errorf("previewing two, %s: %v", n, err)
		}
	}
	endOther()
	if _, err := Call(both, nil, "other", "web.search", in); err == nil {
		t.Error("an ended inner preview still reaches its connector")
	}
	if _, err := Call(both, nil, "trial", "web.search", in); err != nil {
		t.Errorf("ending the inner preview ended the outer: %v", err)
	}
	endTrial()
}

// The free health check runs on its own only for a connector a page names:
// one nothing references makes no calls, a check's among them.
func TestOnlyAReferencedConnectorIsCheckedOnItsOwn(t *testing.T) {
	world(t)
	var mu sync.Mutex
	asked := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked[r.URL.Query().Get("who")]++
		mu.Unlock()
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	for _, n := range []string{"named", "draft"} {
		spec := map[string]any{
			"provider": map[string]any{"transport": "http", "base_url": srv.URL, "auth": map[string]any{"none": true},
				"health":     map[string]any{"call": "ping"},
				"operations": map[string]any{"ping": map[string]any{"url": "/ping?who=" + n}, "find": map[string]any{"url": "/find?q={query}"}}},
			"provides": []any{map[string]any{"capability": "web.search", "steps": []any{map[string]any{"call": "find"}},
				"output": map[string]any{"results": map[string]any{"path": "hits", "fields": map[string]any{"title": "name", "url": "href"}}}}},
		}
		install(t, n, spec)
	}
	RootDB.Set(SearchTable, "provider", "search:named")
	checkDue()
	mu.Lock()
	defer mu.Unlock()
	if asked["named"] != 1 || asked["draft"] != 0 {
		t.Errorf("checked: %v, want only the referenced one", asked)
	}
}

// The connections an agent's tools reach: the image connectors, the music
// ones offered to agents, a Research Source offered through query_source and
// what a flow's steps call. Not one a page has serve the whole instance, nor
// a source research asks without offering it to agents.
func TestAgentConnectionsAreWhatAgentsToolsReach(t *testing.T) {
	world(t)
	for _, n := range []string{"pics", "more-pics", "tunes", "chat", "voice", "ears"} {
		install(t, n, embedSpec("m"))
	}
	search := map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": "https://law.example", "auth": map[string]any{"none": true},
			"operations": map[string]any{"find": map[string]any{"url": "/find?q={query}"}}},
		"provides": []any{map[string]any{"capability": "web.search", "steps": []any{map[string]any{"call": "find"}},
			"output": map[string]any{"results": map[string]any{"path": "hits", "fields": map[string]any{"title": "name", "url": "href"}}}}},
	}
	install(t, "law", search)
	install(t, "quiet-law", search)
	RootDB.Set(ImageTable, "provider", "pics")
	RootDB.Set(ImageTable, "agents", []string{"more-pics"})
	RootDB.Set(MusicTable, "agents", []string{"tunes", "gone"})
	RootDB.Set(LLMTable, "provider", "llm:chat")
	RootDB.Set(FlowTable, "f", Flow{Name: "f", Steps: []FlowStep{{Connector: "voice"}, {Connector: "ears"}}})
	for _, rs := range []ResearchSource{{Connector: "law", Always: true, ToolName: "law_search", ToolDescription: "Law."}, {Connector: "quiet-law", Always: true}} {
		if err := SaveResearchSource(RootDB, rs); err != nil {
			t.Fatal(err)
		}
	}
	got := strings.Join(AgentConnections(RootDB), ",")
	if got != "ears,law,more-pics,pics,tunes,voice" {
		t.Errorf("agent connections: %s", got)
	}
}
