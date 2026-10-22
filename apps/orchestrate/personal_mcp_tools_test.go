package orchestrate

// A person's own MCP servers reach their own agents in their own runs: not
// someone else's run of an agent shared with them, not under Private mode,
// and, for an agent with a tool list, only the names on it.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/mcppersonal"
)

func onePersonalServer(t *testing.T, owner string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if strings.HasPrefix(req.Method, "notifications/") {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		result := map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}}
		if req.Method == "tools/list" {
			result = map[string]any{"tools": []map[string]any{
				{"name": "search", "description": "Find things.", "inputSchema": map[string]any{"type": "object"}},
				{"name": "lookup", "description": "Look one up.", "inputSchema": map[string]any{"type": "object"}},
			}}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(srv.Close)
	if err := mcppersonal.Save(mcppersonal.Server{Owner: owner, Name: "notes", URL: srv.URL}, "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := mcppersonal.Check(context.Background(), owner, "notes"); err != nil {
		t.Fatal(err)
	}
}

func names(defs []AgentToolDef) string {
	var out []string
	for _, d := range defs {
		out = append(out, d.Tool.Name)
	}
	return strings.Join(out, ",")
}

func TestPersonalMCPToolsReachOnlyTheOwnersRuns(t *testing.T) {
	root := depStores(t, "dana")
	root.Set(AuthTable, "user:boss", AuthUser{Username: "boss", Admin: true}) // a loopback test server is an administrator's to add
	onePersonalServer(t, "boss")
	open, err := saveAgent(UserDB(root, "boss"), AgentRecord{Name: "Open", Owner: "boss", OrchestratorPrompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := saveAgent(UserDB(root, "boss"), AgentRecord{Name: "Listed", Owner: "boss", OrchestratorPrompt: "x", AllowedTools: []string{"notes_search"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(personalMCPTools(&ToolSession{Username: "boss"}, "boss", open.ID)); got != "notes_lookup,notes_search" && got != "notes_search,notes_lookup" {
		t.Errorf("the owner's own agent: %q", got)
	}
	if got := names(personalMCPTools(&ToolSession{Username: "boss"}, "boss", listed.ID)); got != "notes_search" {
		t.Errorf("an agent with a tool list takes only what is on it: %q", got)
	}
	if got := personalMCPTools(&ToolSession{Username: "dana"}, "boss", open.ID); len(got) != 0 {
		t.Errorf("someone else's run of the owner's agent got the owner's server: %q", names(got))
	}
	AuthSetPrivateModeForAgent(AuthDB(), "boss", open.ID, true)
	if got := personalMCPTools(&ToolSession{Username: "boss"}, "boss", open.ID); len(got) != 0 {
		t.Errorf("Private mode should take network tools away: %q", names(got))
	}
}

// An agent's tool list keeps a personal server's tool: it is not a registered
// tool, and loading the agent used to heal it away.
func TestAnAgentKeepsAPersonalToolOnItsList(t *testing.T) {
	root := depStores(t)
	root.Set(AuthTable, "user:boss", AuthUser{Username: "boss", Admin: true})
	onePersonalServer(t, "boss")
	a, err := saveAgent(UserDB(root, "boss"), AgentRecord{Name: "Listed", Owner: "boss", OrchestratorPrompt: "x", AllowedTools: []string{"notes_search"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := mcppersonal.SetDisabled("boss", "notes", true); err != nil {
		t.Fatal(err)
	}
	got, ok := loadAgent(UserDB(root, "boss"), a.ID)
	if !ok || len(got.AllowedTools) != 1 || got.AllowedTools[0] != "notes_search" {
		t.Errorf("the agent lost its personal tool: %v", got.AllowedTools)
	}
}
