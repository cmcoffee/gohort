package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/sourcehooks"
)

// A search connector put to use under Research Sources is asked by research
// on its topics, and by agents through query_source or, granted by a skill,
// as a tool of its own; adding it puts its connector to use. Off, removed,
// or the Web Search engine, it is asked by neither.
func TestAResearchSourceIsAskedOnItsTopicsAndByAgents(t *testing.T) {
	world(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"hits":[{"name":"Smith v. Jones","href":"https://law.example/smith"},{"name":"Later","href":"https://www.google.com/goto?id=9"}]}`))
	}))
	defer srv.Close()
	spec := map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": srv.URL, "auth": map[string]any{"none": true},
			"operations": map[string]any{"find": map[string]any{"url": "/find?q={query}"}}},
		"provides": []any{map[string]any{"capability": "web.search", "steps": []any{map[string]any{"call": "find"}},
			"output": map[string]any{"results": map[string]any{"path": "hits", "fields": map[string]any{"title": "name", "url": "href"}}}}},
	}
	raw, _ := json.Marshal(spec)
	if err := SaveConnector(RootDB, Connector{Name: "caselaw", Kind: Kind, Spec: raw}); err != nil {
		t.Fatal(err)
	}
	if len(sourcehooks.HooksForTopic([]string{"legal"})) != 0 {
		t.Fatal("a connector no tie-in uses is asked")
	}
	if err := SaveResearchSource(RootDB, ResearchSource{Connector: "caselaw", Topics: []string{" Legal "},
		ToolName: "caselaw_search", ToolDescription: "Search court opinions."}); err != nil {
		t.Fatal(err)
	}
	if !Referenced(RootDB, "caselaw") {
		t.Error("adding the source did not reference its connector")
	}
	if got := sourcehooks.HooksForTopic([]string{"medical"}); len(got) != 0 {
		t.Errorf("asked on a topic it is not for: %+v", got)
	}
	hooks := sourcehooks.HooksForTopic([]string{"legal"})
	if len(hooks) != 1 || hooks[0].Connector != "caselaw" {
		t.Fatalf("research on its topic: %+v", hooks)
	}
	out, err := sourcehooks.QuerySourceHook(hooks[0], "negligence")
	if err != nil || !strings.HasPrefix(out, "1. Smith v. Jones\n   https://law.example/smith") || !strings.Contains(out, "2. Later\n   (no usable link") {
		t.Errorf("research's query: %q %v", out, err)
	}

	// Offered to agents, it is a tool of its own by its tool name, under
	// Research sources, and its call is the source's own search.
	tools := ResearchSourceTools(nil)
	if len(tools) != 1 || tools[0].Tool.Name != "caselaw_search" || tools[0].Tool.Category != ResearchSourceCategory ||
		tools[0].Tool.Description != "Search court opinions." {
		t.Fatalf("the source's tool: %+v", tools)
	}
	if out, err := tools[0].Handler(context.Background(), map[string]any{"query": "negligence"}); err != nil || !strings.Contains(out, "Smith v. Jones") || !strings.Contains(out, "[Grounding:") {
		t.Errorf("its search: %q %v", out, err)
	}
	if def, isSource, ok := ResearchSourceTool(nil, "caselaw_search"); !isSource || !ok || def.Tool.Name != "caselaw_search" {
		t.Errorf("by its name: %v %v", isSource, ok)
	}
	if got, err := GetAgentToolsWithSession(nil, "caselaw_search"); err != nil || len(got) != 1 {
		t.Errorf("resolved by name for an agent: %v %v", got, err)
	}
	if def, ok := SourceHookToolDefByName("caselaw_search"); !ok || def.Tool.Description != "Search court opinions." {
		t.Errorf("a skill's grant: %+v %v", def.Tool, ok)
	}
	// An agent switched off the connection is not given the source's tool,
	// nor served by it, nor by a skill's grant of it.
	off := &ToolSession{Username: "u", DeniedCredentials: map[string]bool{"connection:caselaw": true}}
	if len(ResearchSourceTools(off)) != 0 {
		t.Error("a barred agent is offered the source")
	}
	if got, err := GetAgentToolsWithSession(off, "caselaw_search"); err != nil || len(got) != 0 {
		t.Errorf("a barred agent's grant of it: %v %v", got, err)
	}
	ctx := off.ContextWithSession(context.Background())
	if _, err := tools[0].Handler(ctx, map[string]any{"query": "negligence"}); err == nil {
		t.Error("the source's tool served a barred agent")
	}
	// A tool name another tool has is refused: agents would reach that one,
	// whether a name the framework keeps or another source's.
	RegisterReservedToolName("kept_by_framework")
	if err := SaveConnector(RootDB, Connector{Name: "statutes", Kind: Kind, Spec: raw}); err != nil {
		t.Fatal(err)
	}
	// Asked by research only, with no tool name, it is not an agent's tool.
	if err := SaveResearchSource(RootDB, ResearchSource{Connector: "statutes", Always: true}); err != nil {
		t.Fatal(err)
	}
	if got := ResearchSourceTools(nil); len(got) != 1 || got[0].Tool.Name != "caselaw_search" {
		t.Errorf("a source not offered to agents is a tool: %+v", got)
	}
	if err := SaveResearchSource(RootDB, ResearchSource{Connector: "statutes", Always: true, ToolName: "statute_search"}); err != nil {
		t.Fatal(err)
	}
	for _, taken := range []string{"kept_by_framework", "statute_search"} {
		if err := SaveResearchSource(RootDB, ResearchSource{Connector: "caselaw", Always: true, ToolName: taken}); err == nil || !strings.Contains(err.Error(), "another tool's") {
			t.Errorf("tool name %s: %v", taken, err)
		}
	}
	DeleteResearchSource(RootDB, "statutes")
	if def, ok := SourceHookToolDefByName("caselaw_search"); !ok {
		t.Error("the skill's grant is gone")
	} else if _, err := def.Handler(ctx, map[string]any{"query": "negligence"}); err == nil {
		t.Error("a skill's grant of the source served a barred agent")
	}

	// The Web Search engine is asked as web_search only.
	saved := LoadWebSearchConfigFunc
	LoadWebSearchConfigFunc = func() WebSearchConfig { return WebSearchConfig{Provider: "caselaw"} }
	if len(sourcehooks.HooksForTopic([]string{"legal"})) != 0 {
		t.Error("the Web Search engine is also asked as a source")
	}
	LoadWebSearchConfigFunc = saved

	// A source whose connector has a change waiting is not asked.
	MarkChanged(RootDB, "caselaw", "someone")
	if len(sourcehooks.HooksForTopic([]string{"legal"})) != 0 {
		t.Error("a source on a connector with a change waiting is asked")
	}
	AcceptChanges(RootDB, "caselaw")

	rs, _ := GetResearchSource(RootDB, "caselaw")
	rs.Off = true
	SaveResearchSource(RootDB, rs)
	if len(ResearchSourceTools(nil)) != 0 || len(sourcehooks.HooksForTopic([]string{"legal"})) != 0 {
		t.Error("an off source is still asked")
	}
	rs.Off = false
	SaveResearchSource(RootDB, rs)
	DeleteResearchSource(RootDB, "caselaw")
	if len(sourcehooks.HooksForTopic([]string{"legal"})) != 0 {
		t.Error("a removed source is still asked")
	}
	if _, ok := GetConnector(RootDB, "caselaw"); !ok {
		t.Error("removing the source removed its connector")
	}
}

// A research source is a search connector that is asked somewhere, by a
// name an agent can call; a spec no longer says where it is put to use.
func TestAResearchSourceIsChecked(t *testing.T) {
	world(t)
	install(t, "finder", templateSpec(t, "europe-pmc-search", "https://api.example.org"))
	install(t, "voice", templateSpec(t, "elevenlabs-speech", "https://api.example.org"))
	for want, rs := range map[string]ResearchSource{
		"no search connector": {Connector: "voice", Always: true},
		"say when":            {Connector: "finder"},
		"lowercase letters":   {Connector: "finder", ToolName: "Find It"},
	} {
		if err := SaveResearchSource(RootDB, rs); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
	spec := templateSpec(t, "europe-pmc-search", "https://api.example.org")
	spec["provides"].([]any)[0].(map[string]any)["research"] = map[string]any{"topics": []any{"medical"}}
	if err := validateSpec(t, spec); err == nil || !strings.Contains(err.Error(), "Research Sources") {
		t.Errorf("research in a spec: %v", err)
	}
}

// SourceGet reads a path under a search connector's address with its own
// key, and only of a connector in use.
func TestSourceGetReadsWithTheConnectionsKey(t *testing.T) {
	world(t)
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Write([]byte(`{"plain_text":"The court held."}`))
	}))
	defer srv.Close()
	spec := map[string]any{
		"config": []any{map[string]any{"name": "token", "type": "secret", "send": "header:Authorization"}},
		"provider": map[string]any{"transport": "http", "base_url": srv.URL, "auth": map[string]any{"secret": "token"},
			"operations": map[string]any{"find": map[string]any{"path": "/s?q={query}"}}},
		"provides": []any{map[string]any{"capability": "web.search", "steps": []any{map[string]any{"call": "find"}},
			"output": map[string]any{"results": map[string]any{"path": "hits", "fields": map[string]any{"title": "t", "url": "u"}}}}},
	}
	install(t, "opinions", spec)
	c, _ := GetConnector(RootDB, "opinions")
	SetConnectionSecret(RootDB, c, "token", "Token t0k", []string{srv.URL})
	body, err := SourceGet(context.Background(), "opinions", "/opinions/42/")
	if err != nil || string(body) != `{"plain_text":"The court held."}` || auth != "Token t0k" {
		t.Errorf("read: %q %v, auth %q", body, err, auth)
	}
	MarkChanged(RootDB, "opinions", "someone")
	if _, err := SourceGet(context.Background(), "opinions", "/opinions/42/"); err == nil || !strings.Contains(err.Error(), "change waiting") {
		t.Errorf("a connector a change holds was read: %v", err)
	}
}
