package core

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// llmToolNamePattern is the character class every provider validates a tool
// name against. Anthropic states it directly (and Bedrock relays the same
// rejection: "tools.28.custom.name: String should match pattern
// '^[a-zA-Z0-9_-]{1,128}$'"); OpenAI and Gemini are no more permissive.
var llmToolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

// TestMCPExposedNameIsAlwaysValid is the regression for a whole-catalog outage.
// A remote MCP server names its own tools and we publish them; Atlassian ships
// "search", which used to compose to "atlassian.search". The dot is not in the
// class above, and the provider rejects the ENTIRE request rather than the one
// tool — so every tool in the catalog went away and the agent silently lost all
// tool use.
func TestMCPExposedNameIsAlwaysValid(t *testing.T) {
	raws := []string{
		"search", "get.page", "createIssue", "jira:get-issue",
		"read wiki page", "  spaced  ", "UPPER_CASE", "tool@v2",
		"a/b/c", "ключ", "123", strings.Repeat("long", 60),
	}
	servers := []string{"atlassian", "My Server", "svc.prod", "x", strings.Repeat("s", 200)}
	for _, srv := range servers {
		taken := map[string]bool{}
		for _, raw := range raws {
			got := mcpExposedName(srv, raw, taken)
			if got == "" {
				continue // unusable name; registerTools logs and skips it
			}
			if !llmToolNamePattern.MatchString(got) {
				t.Errorf("mcpExposedName(%q, %q) = %q, which the providers reject", srv, raw, got)
			}
			if taken[got] {
				t.Errorf("mcpExposedName(%q, %q) = %q, already assigned in this pass", srv, raw, got)
			}
			taken[got] = true
		}
	}
}

// TestMCPExposedNameShape pins the readable form. The name is what a user types
// into an agent's allow-list, so it should stay recognizable rather than being
// hashed into safety.
func TestMCPExposedNameShape(t *testing.T) {
	cases := map[[2]string]string{
		{"atlassian", "search"}:        "atlassian_search",
		{"atlassian", "get.page"}:      "atlassian_get_page",
		{"atlassian", "jira:getIssue"}: "atlassian_jira_getissue",
		{"My Server", "read wiki"}:     "my_server_read_wiki",
	}
	for in, want := range cases {
		if got := mcpExposedName(in[0], in[1], nil); got != want {
			t.Errorf("mcpExposedName(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

// TestMCPExposedNameCollisionIsStable — two raw names that sanitize to the same
// string must not overwrite each other, and the assignment has to be the same on
// every reload. A name that moved between restarts would quietly invalidate any
// agent allow-list that referenced it.
func TestMCPExposedNameCollisionIsStable(t *testing.T) {
	assign := func() []string {
		taken := map[string]bool{}
		var out []string
		// Sorted order, matching registerTools.
		for _, raw := range []string{"get-page", "get.page", "get:page"} {
			out = append(out, mcpExposedName("srv", raw, taken))
			taken[out[len(out)-1]] = true
		}
		return out
	}
	first := assign()
	if first[0] != "srv_get_page" {
		t.Errorf("first assignment = %q, want srv_get_page", first[0])
	}
	seen := map[string]bool{}
	for _, n := range first {
		if n == "" {
			t.Fatal("a colliding name was dropped instead of being disambiguated")
		}
		if seen[n] {
			t.Fatalf("collision: %q assigned twice", n)
		}
		seen[n] = true
		if !llmToolNamePattern.MatchString(n) {
			t.Errorf("disambiguated name %q is not valid", n)
		}
	}
	second := assign()
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("assignment %d is unstable: %q then %q", i, first[i], second[i])
		}
	}
}

// categorizedFake is a ChatTool that claims a category, standing in for a
// registered tool without dragging a real one into the test.
type categorizedFake struct{ name, cat string }

func (f categorizedFake) Name() string                       { return f.name }
func (f categorizedFake) Desc() string                       { return "fake" }
func (f categorizedFake) Params() map[string]ToolParam       { return nil }
func (f categorizedFake) Run(map[string]any) (string, error) { return "", nil }
func (f categorizedFake) Category() string                   { return f.cat }

// TestToolCategoryReachesTheAgentToolDef — a claimed category is only useful if
// it survives the ChatTool→AgentToolDef conversion the pickers read from. Both
// converters have to carry it, since different surfaces use different ones.
func TestToolCategoryReachesTheAgentToolDef(t *testing.T) {
	ct := categorizedFake{name: "atlassian_search", cat: "MCP: atlassian"}
	if got := ToolCategory(ct); got != "MCP: atlassian" {
		t.Errorf("ToolCategory = %q", got)
	}
	if got := ChatToolToAgentToolDef(ct).Tool.Category; got != "MCP: atlassian" {
		t.Errorf("ChatToolToAgentToolDef category = %q", got)
	}
	if got := ChatToolToAgentToolDefWithSession(ct, nil).Tool.Category; got != "MCP: atlassian" {
		t.Errorf("ChatToolToAgentToolDefWithSession category = %q", got)
	}
	// A tool claiming nothing must report "" so the caller falls through to
	// the ToolGroup mapping and then the capability label.
	if got := ToolCategory(uncategorizedFake{}); got != "" {
		t.Errorf("an uncategorized tool reported %q", got)
	}
}

type uncategorizedFake struct{}

func (uncategorizedFake) Name() string                       { return "plain" }
func (uncategorizedFake) Desc() string                       { return "fake" }
func (uncategorizedFake) Params() map[string]ToolParam       { return nil }
func (uncategorizedFake) Run(map[string]any) (string, error) { return "", nil }

// TestMCPToolCategoryIsPerServer — one header per server, not one shared "MCP"
// bucket. Two servers have nothing to do with each other and a single server can
// contribute dozens of tools; a merged list is not separable enough to use.
func TestMCPToolCategoryIsPerServer(t *testing.T) {
	if a, b := MCPToolCategory("atlassian"), MCPToolCategory("github"); a == b {
		t.Fatalf("two servers share the category %q", a)
	}
	if got := MCPToolCategory("atlassian"); got != "MCP: atlassian" {
		t.Errorf("MCPToolCategory = %q, want \"MCP: atlassian\"", got)
	}
	if got := MCPToolCategory("  "); got != "MCP" {
		t.Errorf("an unnamed server got category %q, want \"MCP\"", got)
	}
	// And the proxy actually claims it through the optional interface.
	var ct ChatTool = &mcpProxyTool{server: "atlassian", fullName: "atlassian_search"}
	if got := ToolCategory(ct); got != "MCP: atlassian" {
		t.Errorf("mcpProxyTool claims category %q", got)
	}
}

// withToolGroupDB points AuthDB at a throwaway database so the category
// registry can be exercised without touching a real deployment's groups.
//
// The tool-group cache is process-global and hydrates lazily from whichever
// Store it is first handed — correct in production, where AuthDB is one
// database for the life of the process, and a cross-test leak here, where each
// test swaps in a fresh one. A save followed by a delete drops the stale
// snapshot (both mutations invalidate it) and leaves this database empty, so
// the next read hydrates from ours.
func withToolGroupDB(t *testing.T) Database {
	t.Helper()
	db, err := OpenDB(filepath.Join(t.TempDir(), "groups.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	prev := AuthDB
	AuthDB = func() Database { return db }
	t.Cleanup(func() {
		AuthDB = prev
		SaveToolGroup(db, ToolGroup{ID: "test-cache-primer", Name: "primer"})
		DeleteToolGroup(db, "test-cache-primer")
	})
	if _, err := SaveToolGroup(db, ToolGroup{ID: "test-cache-primer", Name: "primer"}); err != nil {
		t.Fatalf("prime the group cache: %v", err)
	}
	if err := DeleteToolGroup(db, "test-cache-primer"); err != nil {
		t.Fatalf("prime the group cache: %v", err)
	}
	return db
}

// TestEnsureMCPToolGroupCreatesOnceAndKeepsTheAdminsWords is the ownership
// split: the description belongs to the admin, the name belongs to the code.
// A reconnect happens constantly, and one that reverted an edited description
// would make the field pointless to edit.
func TestEnsureMCPToolGroupCreatesOnce(t *testing.T) {
	db := withToolGroupDB(t)
	cfg := MCPServerConfig{Name: "atlassian", URL: "https://mcp.atlassian.com/v1/sse"}

	ensureMCPToolGroup(cfg)
	g, found := LoadToolGroup(db, mcpToolGroupID("atlassian"))
	if !found {
		t.Fatal("the category was not created")
	}
	if g.Name != "MCP: atlassian" {
		t.Errorf("category name = %q", g.Name)
	}
	if !strings.Contains(g.Description, "atlassian") || !strings.Contains(g.Description, "mcp.atlassian.com") {
		t.Errorf("description does not name the server or its host: %q", g.Description)
	}
	if len(g.Members) != 0 {
		t.Error("the category should have no Members — the tools claim it on themselves")
	}

	// An admin rewrites it; reconnecting must leave that alone.
	g.Description = "Jira and Confluence for the platform team."
	if _, err := SaveToolGroup(db, g); err != nil {
		t.Fatal(err)
	}
	ensureMCPToolGroup(cfg)
	after, _ := LoadToolGroup(db, mcpToolGroupID("atlassian"))
	if after.Description != "Jira and Confluence for the platform team." {
		t.Errorf("a reconnect overwrote the admin's description: %q", after.Description)
	}
	if after.ID != g.ID {
		t.Error("a reconnect minted a second category instead of finding the existing one")
	}
}

// TestEnsureMCPToolGroupReassertsTheName — the tools claim their category as a
// computed string, so a claim cannot follow a rename. The row is corrected
// rather than left pointing at a category nothing belongs to.
func TestEnsureMCPToolGroupReassertsTheName(t *testing.T) {
	db := withToolGroupDB(t)
	cfg := MCPServerConfig{Name: "atlassian", URL: "https://mcp.atlassian.com"}
	ensureMCPToolGroup(cfg)

	g, _ := LoadToolGroup(db, mcpToolGroupID("atlassian"))
	g.Name = "Renamed By Admin"
	g.Description = "kept"
	if _, err := SaveToolGroup(db, g); err != nil {
		t.Fatal(err)
	}
	ensureMCPToolGroup(cfg)

	after, _ := LoadToolGroup(db, mcpToolGroupID("atlassian"))
	if after.Name != MCPToolCategory("atlassian") {
		t.Errorf("name = %q, want it re-asserted to %q", after.Name, MCPToolCategory("atlassian"))
	}
	if after.Description != "kept" {
		t.Errorf("re-asserting the name clobbered the description: %q", after.Description)
	}
}

// TestDropMCPToolGroupSpareEditedRows — deleting a server takes its generated
// category with it, but never an admin's own writing.
func TestDropMCPToolGroupSparesEditedRows(t *testing.T) {
	db := withToolGroupDB(t)
	cfg := MCPServerConfig{Name: "atlassian", URL: "https://mcp.atlassian.com"}

	ensureMCPToolGroup(cfg)
	dropMCPToolGroup(cfg)
	if _, found := LoadToolGroup(db, mcpToolGroupID("atlassian")); found {
		t.Error("an untouched generated category survived its server")
	}

	ensureMCPToolGroup(cfg)
	g, _ := LoadToolGroup(db, mcpToolGroupID("atlassian"))
	g.Description = "hand-written, not regenerable"
	if _, err := SaveToolGroup(db, g); err != nil {
		t.Fatal(err)
	}
	dropMCPToolGroup(cfg)
	after, found := LoadToolGroup(db, mcpToolGroupID("atlassian"))
	if !found {
		t.Fatal("an edited category was deleted along with its server")
	}
	if after.Description != "hand-written, not regenerable" {
		t.Errorf("description = %q", after.Description)
	}
}

// TestMCPToolGroupIDIsStable — the id is derived, not generated, or every
// restart would mint a duplicate category.
func TestMCPToolGroupIDIsStable(t *testing.T) {
	if a, b := mcpToolGroupID("atlassian"), mcpToolGroupID("atlassian"); a != b {
		t.Fatalf("unstable id: %q then %q", a, b)
	}
	if mcpToolGroupID("My Server") == mcpToolGroupID("my-server-2") {
		t.Error("two server names collapsed onto one category id")
	}
	if mcpToolGroupID("  ") != "" {
		t.Error("an unnamed server should get no category id")
	}
}

// TestMCPExposedNameRespectsLengthCap — the cap is 128 bytes including the
// disambiguating suffix, which is why the suffix is applied after truncation
// rather than appended to an already-full name.
func TestMCPExposedNameRespectsLengthCap(t *testing.T) {
	long := strings.Repeat("verylongtoolname", 20) // 320 chars
	taken := map[string]bool{}
	for i := 0; i < 3; i++ {
		got := mcpExposedName("server", long, taken)
		if len(got) > maxLLMToolNameBytes {
			t.Fatalf("name is %d bytes, over the %d cap: %q", len(got), maxLLMToolNameBytes, got)
		}
		if !llmToolNamePattern.MatchString(got) {
			t.Fatalf("truncated name %q is not valid", got)
		}
		if taken[got] {
			t.Fatalf("truncation collapsed two names onto %q", got)
		}
		taken[got] = true
	}
}
