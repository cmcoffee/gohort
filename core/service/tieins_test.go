package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// searchConnector saves a search connector named name, reaching srvURL (or
// nowhere), approved or not.
func searchConnector(t *testing.T, name, srvURL string, approved bool) {
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
	if approved {
		if err := ApproveConnector(RootDB, name); err != nil {
			t.Fatal(err)
		}
	}
}

func inUse(name string) bool {
	c, _ := GetConnector(RootDB, name)
	return c.Approved
}

// A services connector is in use exactly while a page names it: Web Search's
// engine, a Research Source that is on, a flow's step, the image or music
// lists offered to agents. Taken off every page, it is out of use.
func TestAConnectorIsInUseOnlyWhilePagesNameIt(t *testing.T) {
	world(t)
	for _, n := range []string{"engine", "papers", "idle", "pics", "tunes", "stepper"} {
		searchConnector(t, n, "", true)
	}
	RootDB.Set(SearchTable, "provider", "service:engine")
	RootDB.Set(ImageTable, "agents", []string{"pics"})
	RootDB.Set(MusicTable, "agents", []string{"tunes"})
	RootDB.Set(FlowTable, "f", Flow{Name: "f", Steps: []FlowStep{{ID: "a", Connector: "stepper", Capability: "web.search"}}})
	if err := SaveResearchSource(RootDB, ResearchSource{Connector: "papers", Always: true}); err != nil {
		t.Fatal(err)
	}
	Settle(RootDB)
	uses := TieIns(RootDB)
	for name, page := range map[string]string{"engine": "Web Search", "papers": "Research Sources",
		"pics": "Image Generation (offered to agents)", "tunes": "Music (offered to agents)", "stepper": "Flow f"} {
		if !slices.Contains(uses[name], page) {
			t.Errorf("%s: pages %v, want %s", name, uses[name], page)
		}
		if !inUse(name) {
			t.Errorf("%s is named by %s and not in use", name, page)
		}
	}
	if inUse("idle") || len(uses["idle"]) != 0 {
		t.Errorf("a connector no page names is in use: %v", uses["idle"])
	}

	// Turning the research source off, or naming another engine, takes the
	// connector out of use.
	if err := SaveResearchSource(RootDB, ResearchSource{Connector: "papers", Always: true, Off: true}); err != nil {
		t.Fatal(err)
	}
	RootDB.Set(SearchTable, "provider", "duckduckgo")
	Settle(RootDB)
	if inUse("papers") || inUse("engine") {
		t.Errorf("taken off their pages, papers in use %v, engine in use %v", inUse("papers"), inUse("engine"))
	}
}

// A connector changed by someone not an administrator is held out of use,
// though a page names it, until an administrator accepts the change by
// saving a page that names it.
func TestAChangeIsHeldUntilAnAdministratorAccepts(t *testing.T) {
	world(t)
	searchConnector(t, "engine", "", true)
	RootDB.Set(SearchTable, "provider", "service:engine")
	Settle(RootDB)
	if !inUse("engine") {
		t.Fatal("named by Web Search, not in use")
	}
	MarkChanged(RootDB, "engine", "someone")
	Settle(RootDB)
	if inUse("engine") {
		t.Error("a change waiting on acceptance left the connector in use")
	}
	if ch, ok := PendingChange(RootDB, "engine"); !ok || ch.By != "someone" {
		t.Errorf("the change waiting: %+v %v", ch, ok)
	}
	Settle(RootDB, "other")
	if inUse("engine") {
		t.Error("accepting another connector accepted this one")
	}
	Settle(RootDB, "engine")
	if !inUse("engine") {
		t.Error("accepted, still out of use")
	}
	if _, ok := PendingChange(RootDB, "engine"); ok {
		t.Error("accepted, still waiting")
	}
}

// An administrator's test on a settings page reaches the connector its form
// names before a save puts it to use, and only that test does: a call
// without its context, or of another connector, is still refused.
func TestAnAdministratorsPreviewReachesOnlyItsConnector(t *testing.T) {
	world(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"hits":[{"name":"A","href":"https://a.example/1"}]}`))
	}))
	defer srv.Close()
	searchConnector(t, "trial", srv.URL, false)
	searchConnector(t, "other", srv.URL, false)
	in := map[string]any{"query": "q"}
	if _, err := Call(context.Background(), nil, "trial", "web.search", in); err == nil || !strings.Contains(err.Error(), "not in use") {
		t.Fatalf("an unused connector answered: %v", err)
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
