package extensions

// The Tools list is built from four places: the user's pool, tools scoped to
// one of their agents, legacy session drafts, and tools whose agent was
// deleted. View fetches one record by name from the same places, or it 404s
// on a row the table just drew.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func listedTools(t *testing.T, app *Extensions, r *http.Request) []map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	app.handleUserTools(w, r)
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("tools list: %v %s", err, w.Body.String())
	}
	return rows
}

func toolRowNamed(rows []map[string]any, name string) map[string]any {
	for _, r := range rows {
		if r["name"] == name {
			return r
		}
	}
	return nil
}

func TestTheToolsListReadsEveryPlaceAToolLives(t *testing.T) {
	app, req := authedExtensions(t)
	if err := AdminPersistTempTool(RootDB, "alice", TempTool{Name: "pool_tool", Description: "d", CommandTemplate: "echo pool"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolSharedWith(RootDB, "alice", "pool_tool", []string{"bob"}); err != nil {
		t.Fatal(err)
	}
	RegisterScopedToolLister(func(user string) []ScopedTool {
		if user != "alice" {
			return nil
		}
		return []ScopedTool{
			{Scope: ScopeAgentTool, AgentID: "a-1", AgentName: "Scout", Tool: TempTool{Name: "agent_tool", Description: "d"}},
			{Scope: ScopeAgentTool, AgentID: "a-2", AgentName: "Helper", ParentName: "Scout", Tool: TempTool{Name: "agent_tool", Description: "d"}},
			{Scope: ScopeSessionTool, AgentID: "a-1", AgentName: "Scout", SessionID: "s-1", Tool: TempTool{Name: "draft_tool", Description: "d"}},
			{Scope: ScopeSessionTool, AgentID: "a-1", AgentName: "Scout", SessionID: "s-2", Shadowed: true, Tool: TempTool{Name: "stale_draft", Description: "d"}},
		}
	})
	t.Cleanup(func() { RegisterScopedToolLister(nil) })
	AddOrphanedTempTools(AuthDB(), "alice", []OrphanedTempTool{{Tool: TempTool{Name: "lost_tool", Description: "d"}, FormerAgentName: "Gone", OrphanedAt: time.Now()}})

	rows := listedTools(t, app, req("GET", "/api/tools"))
	if r := toolRowNamed(rows, "pool_tool"); r == nil || r["pool"] != true {
		t.Errorf("the pool tool is missing: %+v", r)
	}
	n := 0
	for _, r := range rows {
		if r["name"] == "agent_tool" {
			n++
			if r["agent_tool"] != true || r["agent_list"] != "Scout, Scout ▸ Helper" {
				t.Errorf("a tool on two agents should be one row naming both: %+v", r)
			}
		}
	}
	if n != 1 {
		t.Errorf("the scoped tool is listed %d times, want once", n)
	}
	if r := toolRowNamed(rows, "draft_tool"); r == nil || r["session"] != true {
		t.Errorf("the session draft is missing: %+v", r)
	}
	if toolRowNamed(rows, "stale_draft") != nil {
		t.Error("a shadowed draft is listed")
	}
	if r := toolRowNamed(rows, "lost_tool"); r == nil || r["orphan"] != true || r["group"] != "Orphaned Tools: agent Gone was deleted" {
		t.Errorf("the orphaned tool is missing: %+v", r)
	}

	// View answers one record, from the same places.
	for _, name := range []string{"pool_tool", "agent_tool", "draft_tool"} {
		w := httptest.NewRecorder()
		app.handleUserTools(w, req("GET", "/api/tools?name="+name))
		var one TempTool
		if err := json.Unmarshal(w.Body.Bytes(), &one); err != nil || one.Name != name {
			t.Errorf("View of %s: %v %s", name, err, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	app.handleUserTools(w, req("GET", "/api/tools?name=stale_draft"))
	if w.Code != http.StatusNotFound {
		t.Errorf("View of a tool that is not listed answered %d, want 404", w.Code)
	}

	// The share picker reads only the recipient list.
	w = httptest.NewRecorder()
	app.handleUserTools(w, req("GET", "/api/tools?share=pool_tool"))
	var share map[string][]string
	if err := json.Unmarshal(w.Body.Bytes(), &share); err != nil || len(share) != 1 || len(share["shared_with"]) != 1 || share["shared_with"][0] != "bob" {
		t.Errorf("the share picker should get the recipients and nothing else: %v %s", err, w.Body.String())
	}
}
