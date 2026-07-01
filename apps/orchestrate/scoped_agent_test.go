package orchestrate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestAgentScopeStateIsolation proves the guarantee RunScopedAgent relies on: two
// scopes (synthetic ScopeUsers) running the SAME template agent keep entirely
// separate state. State is keyed under UserDB(root, ScopeUser), so a session
// written in one scope is invisible in another scope and in the template owner's
// own store.
func TestAgentScopeStateIsolation(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	const agentID = "tmpl-agent"

	scopeA := UserDB(root, "app:test:a")
	scopeB := UserDB(root, "app:test:b")
	ownerStore := UserDB(root, seedOwner)

	// A session accumulated by the instance running in scope A.
	if _, err := saveChatSession(scopeA, ChatSession{
		ID:       "s1",
		AgentID:  agentID,
		Messages: []ChatMessage{{Role: "user", Content: "remember: the sky is green"}},
	}); err != nil {
		t.Fatalf("save in scope A: %v", err)
	}

	// Visible within its own scope.
	if _, ok := loadChatSession(scopeA, agentID, "s1"); !ok {
		t.Fatal("session missing in its own scope")
	}
	// NOT visible in a different scope of the same template.
	if _, ok := loadChatSession(scopeB, agentID, "s1"); ok {
		t.Fatal("session leaked into another scope — scopes are not isolated")
	}
	// NOT visible in the template owner's own store (the template is config-only).
	if _, ok := loadChatSession(ownerStore, agentID, "s1"); ok {
		t.Fatal("session leaked into the template owner's store")
	}
}

// TestSeedScopedMemoryIsolation proves an app can pre-load a scope's memory and
// that the seed is (a) readable in that scope and (b) invisible in another scope
// of the same template.
func TestSeedScopedMemoryIsolation(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{AppCore: AppCore{DB: root}}
	const agentID = "tmpl-agent"

	if err := app.SeedScopedMemory(AgentScope{AgentID: agentID, ScopeUser: "app:test:a"},
		"the appliance runs nginx 1.24 on port 8443"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ns := factsNamespace(agentID)
	if got := ListMemoryFacts(UserDB(root, "app:test:a"), ns); len(got) != 1 {
		t.Fatalf("scope A: want 1 fact, got %d", len(got))
	}
	if got := ListMemoryFacts(UserDB(root, "app:test:b"), ns); len(got) != 0 {
		t.Fatalf("scope B: fact leaked across scopes, got %d", len(got))
	}
}

// TestSeedScopedGraphEntityIsolation proves the keyed/structured seeding path:
// an appliance's facts land as entity ATTRS in the scope's graph namespace,
// merge on re-seed (deterministic key overwrite, no duplicate entity), and stay
// invisible in another scope of the same template.
func TestSeedScopedGraphEntityIsolation(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{AppCore: AppCore{DB: root}}
	const agentID = "tmpl-agent"
	scope := AgentScope{AgentID: agentID, ScopeUser: "app:test:a"}

	if err := app.SeedScopedGraphEntity(scope, "appliance", "web01", []string{"appl-1"},
		map[string]string{"os": "Ubuntu 22.04", "port": "8443"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Re-seed updates an attr in place — must NOT create a second entity.
	if err := app.SeedScopedGraphEntity(scope, "appliance", "web01", []string{"appl-1"},
		map[string]string{"os": "Ubuntu 24.04"}); err != nil {
		t.Fatalf("re-seed: %v", err)
	}

	ns := factsNamespace(agentID)
	ents := ListGraphEntities(UserDB(root, "app:test:a"), ns)
	if len(ents) != 1 {
		t.Fatalf("scope A: want 1 merged entity, got %d", len(ents))
	}
	if ents[0].Attrs["os"] != "Ubuntu 24.04" || ents[0].Attrs["port"] != "8443" {
		t.Fatalf("attrs not merged/overwritten: %#v", ents[0].Attrs)
	}
	if got := ListGraphEntities(UserDB(root, "app:test:b"), ns); len(got) != 0 {
		t.Fatalf("scope B: entity leaked across scopes, got %d", len(got))
	}
}

// TestSeedScopedGraphLink proves an app can build a multi-entity graph in a
// scope: a link creates BOTH the subject (with attrs) and object entities.
func TestSeedScopedGraphLink(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{AppCore: AppCore{DB: root}}
	const agentID = "tmpl-agent"
	scope := AgentScope{AgentID: agentID, ScopeUser: "app:test:a"}

	if err := app.SeedScopedGraphLink(scope, "service", "nginx",
		map[string]string{"version": "1.24"}, "proxies to", "app", "web-app", "over :8080", false); err != nil {
		t.Fatalf("link: %v", err)
	}
	ents := ListGraphEntities(UserDB(root, "app:test:a"), factsNamespace(agentID))
	if len(ents) != 2 {
		t.Fatalf("want 2 entities (subject+object), got %d", len(ents))
	}
	var nginx *GraphEntity
	for i := range ents {
		if ents[i].Name == "nginx" {
			nginx = &ents[i]
		}
	}
	if nginx == nil || nginx.Attrs["version"] != "1.24" {
		t.Fatalf("subject entity missing or attr not set: %#v", ents)
	}
}

// TestScopedGraphSummary proves the read-back: the rendered summary contains the
// entities, their attrs, and the relationship between them.
func TestScopedGraphSummary(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{AppCore: AppCore{DB: root}}
	const agentID = "tmpl-agent"
	scope := AgentScope{AgentID: agentID, ScopeUser: "app:test:a"}

	_ = app.SeedScopedGraphLink(scope, "service", "nginx",
		map[string]string{"port": "443"}, "proxies to", "app", "web-app", "", false)

	s := app.ScopedGraphSummary(scope)
	for _, want := range []string{"nginx", "port=443", "proxies to", "web-app"} {
		if !strings.Contains(s, want) {
			t.Fatalf("summary missing %q:\n%s", want, s)
		}
	}
	if app.ScopedGraphSummary(AgentScope{AgentID: agentID, ScopeUser: "app:test:b"}) != "" {
		t.Fatal("empty scope should render empty summary")
	}
}

// TestWipeScopedMemory proves a full per-instance reset clears the in-DB layers
// (Explicit facts + Graph entities) and leaves other scopes untouched.
func TestWipeScopedMemory(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{AppCore: AppCore{DB: root}}
	const agentID = "tmpl-agent"
	a := AgentScope{AgentID: agentID, ScopeUser: "app:test:a"}
	b := AgentScope{AgentID: agentID, ScopeUser: "app:test:b"}

	_ = app.SeedScopedMemory(a, "appliance runs nginx on 8443")
	_ = app.SeedScopedGraphEntity(a, "appliance", "web01", nil, map[string]string{"os": "Ubuntu"})
	_ = app.SeedScopedMemory(b, "other box")

	if err := app.WipeScopedMemory(a); err != nil {
		t.Fatalf("wipe: %v", err)
	}
	ns := factsNamespace(agentID)
	if got := ListMemoryFacts(UserDB(root, "app:test:a"), ns); len(got) != 0 {
		t.Fatalf("scope A facts not cleared: %d", len(got))
	}
	if got := ListGraphEntities(UserDB(root, "app:test:a"), ns); len(got) != 0 {
		t.Fatalf("scope A graph not cleared: %d", len(got))
	}
	if got := ListMemoryFacts(UserDB(root, "app:test:b"), ns); len(got) != 1 {
		t.Fatalf("scope B should be untouched, got %d facts", len(got))
	}
}

// TestKnowledgeBaseLayer proves enabler #1: a scoped instance retrieves the
// TEMPLATE owner's base knowledge ALONGSIDE its own, and without a base scope it
// sees only its own (the layered-vs-pure-split behavior).
func TestKnowledgeBaseLayer(t *testing.T) {
	prev := VectorDB
	VectorDB = &DBase{Store: kvlite.MemStore()}
	defer func() { VectorDB = prev }()

	const agentID = "tmpl-agent"
	const base = "system"     // template owner
	const inst = "app:test:a" // appliance instance scope

	put := func(id, user, text string) {
		VectorDB.Set(EmbeddedChunks, id, EmbeddedChunk{
			ID: id, Source: knowledgeSource(user, agentID, "general"), Text: text, Date: "2026",
		})
	}
	put("base1", base, "base note: nginx config usually lives in /etc/nginx")
	put("inst1", inst, "instance note: this appliance runs nginx on port 8443")

	// Layered: base + instance both retrieved.
	got := searchAgentKnowledge(context.Background(), nil, inst, base, agentID, "general", "nginx", 10, nil, nil, ChunkScopeAll)
	if len(got) != 2 {
		t.Fatalf("with base layer: want 2 hits (base+instance), got %d", len(got))
	}
	// No base scope: instance only.
	if got := searchAgentKnowledge(context.Background(), nil, inst, "", agentID, "general", "nginx", 10, nil, nil, ChunkScopeAll); len(got) != 1 {
		t.Fatalf("without base layer: want 1 hit (instance only), got %d", len(got))
	}
}

// TestSeedScopedRulesOverlay proves enabler #2: an instance's rules are isolated
// per scope, deduped, and merge UNDER the template's base rules.
func TestSeedScopedRulesOverlay(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{AppCore: AppCore{DB: root}}
	const agentID = "tmpl-agent"

	if err := app.SeedScopedRules(AgentScope{AgentID: agentID, ScopeUser: "app:test:a"},
		"this box is prod — read-only commands only", "this box is prod — read-only commands only"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	a := listScopeRules(UserDB(root, "app:test:a"), agentID)
	if len(a) != 1 { // deduped
		t.Fatalf("scope A: want 1 rule (deduped), got %d", len(a))
	}
	if got := listScopeRules(UserDB(root, "app:test:b"), agentID); len(got) != 0 {
		t.Fatalf("scope B: rule leaked across scopes, got %d", len(got))
	}

	// Merge layers the instance rule UNDER the template base.
	merged := mergeScopeRules("BASE: never run destructive commands", a)
	if !strings.Contains(merged, "BASE:") || !strings.Contains(merged, "read-only commands only") {
		t.Fatalf("merge missing a layer: %q", merged)
	}
	if strings.Index(merged, "BASE:") > strings.Index(merged, "read-only") {
		t.Fatal("base rules should come before the instance overlay")
	}
}

// TestForScopeHandlersRequireSession proves the per-scope memory handlers (Part A
// of the display) still gate on a valid session — servitor authorizes WHICH scope,
// but a logged-out request never reads anyone's memory.
func TestForScopeHandlersRequireSession(t *testing.T) {
	app := &OrchestrateApp{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	for _, call := range []func(http.ResponseWriter, *http.Request){
		func(w http.ResponseWriter, r *http.Request) { app.PublicHandleAgentGraphForScope(w, r, "app:test:a", "tmpl") },
		func(w http.ResponseWriter, r *http.Request) { app.PublicHandleAgentFactsForScope(w, r, "app:test:a", "tmpl") },
		func(w http.ResponseWriter, r *http.Request) { app.PublicHandleAgentKnowledgeForScope(w, r, "app:test:a", "tmpl") },
	} {
		w := httptest.NewRecorder()
		call(w, httptest.NewRequest(http.MethodGet, "/", nil)) // no session
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("logged-out per-scope read should be 401, got %d", w.Code)
		}
	}
}

// TestRunScopedAgentValidation checks the wrapper rejects an incomplete scope
// before reaching the run pipeline (so an app gets a clear error, not a cryptic
// downstream failure).
func TestRunScopedAgentValidation(t *testing.T) {
	app := &OrchestrateApp{} // LLM nil — we only exercise the pre-run validation

	if _, err := app.RunScopedAgent(context.Background(), AgentScope{AgentID: "x"}, "hi", nil); err == nil {
		t.Fatal("expected error when ScopeUser is empty")
	}
	if _, err := app.RunScopedAgent(context.Background(), AgentScope{ScopeUser: "app:test:a"}, "hi", nil); err == nil {
		t.Fatal("expected error when AgentID is empty")
	}
}
