package extensions

// The two lists a user reads about versions: their own published tool says
// which version everybody else runs and whether their copy has moved on; the
// catalog says when a colleague's tool they took has an update, and Accept
// takes it.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/promotion"
)

func rowsNamed(t *testing.T, body []byte, name string) map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("decode: %v %s", err, body)
	}
	for _, r := range rows {
		if r["name"] == name {
			return r
		}
	}
	t.Fatalf("no row %s in %s", name, body)
	return nil
}

func TestAPublishedToolRowSaysItsCopyDiffers(t *testing.T) {
	app, req := authedExtensions(t)
	if err := AdminPersistTempTool(RootDB, "alice", TempTool{Name: "lookup", Description: "d", CommandTemplate: "echo one"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolShared(RootDB, "alice", "lookup", true); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	serve(app)(w, req("GET", "/api/tools"))
	if r := rowsNamed(t, w.Body.Bytes(), "lookup"); r["release"] != "Published v1" || r["can_update"] == true {
		t.Fatalf("an unchanged published tool: %+v", r)
	}
	if err := AdminPersistTempTool(RootDB, "alice", TempTool{Name: "lookup", Description: "d", CommandTemplate: "echo two"}); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	serve(app)(w, req("GET", "/api/tools"))
	r := rowsNamed(t, w.Body.Bytes(), "lookup")
	if r["can_update"] != true || r["differs"] != true || !strings.Contains(r["release"].(string), "your copy differs") {
		t.Fatalf("an edited published tool should offer Request update: %+v", r)
	}
	if d, _ := r["diff"].(string); !strings.Contains(d, "+ echo two") {
		t.Fatalf("the row's diff does not show the edit: %q", d)
	}

	// The owner withdraws it themselves.
	w = httptest.NewRecorder()
	serve(app)(w, req("POST", "/api/tools?action=withdraw&name=lookup"))
	if w.Code/100 != 2 {
		t.Fatalf("withdraw: %d %s", w.Code, w.Body.String())
	}
	if p, _ := UserToolByName(RootDB, "alice", "lookup"); p.Shared {
		t.Fatal("withdraw left the tool published")
	}
}

func TestTheCatalogOffersAColleaguesUpdate(t *testing.T) {
	app, req := authedExtensions(t)
	if err := AdminPersistTempTool(RootDB, "lender", TempTool{Name: "wiki_read", Description: "d", CommandTemplate: "echo one"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolSharedWith(RootDB, "lender", "wiki_read", []string{"alice"}); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalToolAdopted(RootDB, "alice", "wiki_read", "lender", true); err != nil {
		t.Fatal(err)
	}
	if err := AdminPersistTempTool(RootDB, "lender", TempTool{Name: "wiki_read", Description: "d", CommandTemplate: "echo two"}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	serve(app)(w, req("GET", "/api/global-tools"))
	r := rowsNamed(t, w.Body.Bytes(), "wiki_read")
	if r["update_available"] != true || !strings.Contains(r["diff"].(string), "+ echo two") {
		t.Fatalf("the colleague's edit should read as an update available: %+v", r)
	}
	w = httptest.NewRecorder()
	serve(app)(w, req("POST", "/api/global-tools?name=wiki_read&owner=lender&adopt=true"))
	if w.Code/100 != 2 {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	serve(app)(w, req("GET", "/api/global-tools"))
	if r := rowsNamed(t, w.Body.Bytes(), "wiki_read"); r["update_available"] == true {
		t.Fatalf("accepting should clear the update: %+v", r)
	}
	for _, p := range AdoptedToolsFor(RootDB, "alice") {
		if p.Tool.Name == "wiki_read" && p.Tool.CommandTemplate != "echo two" {
			t.Fatalf("after accepting, alice still runs %q", p.Tool.CommandTemplate)
		}
	}
}

// A tool kept to one agent can still be offered to the deployment. The
// owner asks from its row, approval publishes the definition, and the
// owner's scope stays exactly as it was: the tool is still on that one
// agent of theirs, while whoever adds it from the catalog has it on theirs.
func TestAToolOnOneAgentCanBeShared(t *testing.T) {
	app, req := authedExtensions(t)
	if err := AdminPersistTempTool(RootDB, "alice", TempTool{Name: "fx_rate", Description: "d", CommandTemplate: "echo rate"}); err != nil {
		t.Fatal(err)
	}
	if !SetUserToolScopeAgents(RootDB, "alice", "fx_rate", []string{"a-1"}) {
		t.Fatal("scope not set")
	}
	RegisterScopedToolLister(func(user string) []ScopedTool {
		if user != "alice" {
			return nil
		}
		p, _ := UserToolByName(RootDB, "alice", "fx_rate")
		return []ScopedTool{{Scope: ScopeAgentTool, AgentID: "a-1", AgentName: "Travel", Tool: p.Tool}}
	})
	t.Cleanup(func() { RegisterScopedToolLister(nil) })

	w := httptest.NewRecorder()
	serve(app)(w, req("GET", "/api/tools"))
	if r := rowsNamed(t, w.Body.Bytes(), "fx_rate"); r["agent_tool"] != true || r["can_request"] != true {
		t.Fatalf("a scoped tool should offer Request to publish: %+v", r)
	}
	w = httptest.NewRecorder()
	serve(app)(w, req("POST", "/api/promotions?kind=tool&name=fx_rate"))
	if w.Code/100 != 2 {
		t.Fatalf("request: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	serve(app)(w, req("GET", "/api/tools"))
	if r := rowsNamed(t, w.Body.Bytes(), "fx_rate"); r["requested"] != true || r["can_request"] == true {
		t.Fatalf("a requested scoped tool should read as waiting: %+v", r)
	}

	if err := promotion.Approve(AuthDB(), PromotionRequestKey("tool", "alice", "fx_rate"), "admin"); err != nil {
		t.Fatal(err)
	}
	p, _ := UserToolByName(RootDB, "alice", "fx_rate")
	if !p.Shared || len(p.ScopeAgents) != 1 || p.ScopeAgents[0] != "a-1" {
		t.Fatalf("approval should publish it and leave the owner's scope alone: shared=%v scope=%v", p.Shared, p.ScopeAgents)
	}
	w = httptest.NewRecorder()
	serve(app)(w, req("GET", "/api/tools"))
	if r := rowsNamed(t, w.Body.Bytes(), "fx_rate"); r["release"] != "Published v1" || r["shared"] != true {
		t.Fatalf("the scoped row should say it is published: %+v", r)
	}

	if err := SetGlobalToolAdopted(RootDB, "bob", "fx_rate", "alice", true); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, lt := range AdoptedToolsFor(RootDB, "bob") {
		if lt.Tool.Name == "fx_rate" {
			found = true
			if len(lt.ScopeAgents) != 0 {
				t.Fatalf("an adopter inherited the owner's agents: %v", lt.ScopeAgents)
			}
		}
	}
	if !found {
		t.Fatal("bob added the published tool and does not load it")
	}
}

// An administrator's own tool publishes, and takes an update, at once: no
// request to themselves. A publish that cannot happen leaves nothing waiting
// in the queue.
func TestAnAdministratorsOwnToolPublishesDirectly(t *testing.T) {
	app, req := authedExtensions(t)
	AuthDB().Set(AuthTable, "user:alice", AuthUser{Username: "alice", Admin: true})
	if err := AdminPersistTempTool(RootDB, "alice", TempTool{Name: "lookup", Description: "d", CommandTemplate: "echo one"}); err != nil {
		t.Fatal(err)
	}
	row := func() map[string]any {
		w := httptest.NewRecorder()
		serve(app)(w, req("GET", "/api/tools"))
		return rowsNamed(t, w.Body.Bytes(), "lookup")
	}
	publish := func(name string) int {
		w := httptest.NewRecorder()
		serve(app)(w, req("POST", "/api/promotions?kind=tool&name="+name))
		return w.Code
	}
	if r := row(); r["can_publish"] != true || r["can_request"] == true {
		t.Fatalf("an admin's own tool should offer Publish, not a request: %+v", r)
	}
	if code := publish("lookup"); code/100 != 2 {
		t.Fatalf("publish: %d", code)
	}
	if r := row(); r["release"] != "Published v1" || r["requested"] == true {
		t.Fatalf("published at once: %+v", r)
	}
	if err := AdminPersistTempTool(RootDB, "alice", TempTool{Name: "lookup", Description: "d", CommandTemplate: "echo two"}); err != nil {
		t.Fatal(err)
	}
	if r := row(); r["can_publish_update"] != true || r["can_update"] == true {
		t.Fatalf("an admin's edited tool should offer Publish update: %+v", r)
	}
	if code := publish("lookup"); code/100 != 2 {
		t.Fatalf("publish update: %d", code)
	}
	if r := row(); r["release"] != "Published v2" {
		t.Fatalf("the update is the next version at once: %+v", r)
	}

	// A name the deployment already publishes for somebody else.
	if err := AdminPersistTempTool(RootDB, "bob", TempTool{Name: "dup", Description: "d", CommandTemplate: "echo bob"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolShared(RootDB, "bob", "dup", true); err != nil {
		t.Fatal(err)
	}
	if err := AdminPersistTempTool(RootDB, "alice", TempTool{Name: "dup", Description: "d", CommandTemplate: "echo alice"}); err != nil {
		t.Fatal(err)
	}
	if code := publish("dup"); code/100 == 2 {
		t.Fatal("a clashing name was published")
	}
	if PendingPromotion(AuthDB(), "alice", "tool", "dup") {
		t.Fatal("a failed direct publish left a request in the queue")
	}
}
