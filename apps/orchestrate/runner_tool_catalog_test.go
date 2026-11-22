package orchestrate

import (
	"context"
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/temptool"
)

// fakeOneTool is an MCP server offered as one tool, and fakeOneProxy one of
// its tools as it would be named separately.
type fakeOneTool struct{}

func (fakeOneTool) Name() string                       { return "fakeonesrv" }
func (fakeOneTool) Desc() string                       { return "d" }
func (fakeOneTool) Params() map[string]ToolParam       { return nil }
func (fakeOneTool) Run(map[string]any) (string, error) { return "", nil }
func (fakeOneTool) ActionCaps() map[string][]Capability {
	return map[string][]Capability{"search": {CapRead}, "create_ticket": {CapRead}, "delete_ticket": {CapRead}}
}

type fakeOneProxy struct{ name, action string }

func (p fakeOneProxy) Name() string                     { return p.name }
func (fakeOneProxy) Desc() string                       { return "d" }
func (fakeOneProxy) Params() map[string]ToolParam       { return nil }
func (fakeOneProxy) Run(map[string]any) (string, error) { return "", nil }
func (p fakeOneProxy) RedirectAction() (string, string) { return "fakeonesrv", p.action }

// An agent granted one of a one-tool MCP server's tools reaches that action
// and no other. The grant used to reach the whole server, so an agent given
// search could call create_ticket through the one tool.
func TestAGrantOfOneToolReachesOnlyItsAction(t *testing.T) {
	RegisterChatTool(fakeOneTool{})
	RegisterChatTool(fakeOneProxy{"fakeonesrv_search", "search"})
	RegisterChatTool(fakeOneProxy{"fakeonesrv_create_ticket", "create_ticket"})

	some := &chatTurn{user: "u", agent: AgentRecord{ID: "a", AllowedTools: []string{"fakeonesrv_search"}}}
	got := strings.Join(some.partialOneToolWithheld(), ",")
	if got != "fakeonesrv/create_ticket,fakeonesrv/delete_ticket" {
		t.Errorf("withheld = %q, want every action but search", got)
	}
	whole := &chatTurn{user: "u", agent: AgentRecord{ID: "a", AllowedTools: []string{"fakeonesrv"}}}
	if w := whole.partialOneToolWithheld(); len(w) != 0 {
		t.Errorf("a grant of the one tool itself keeps every action, withheld %v", w)
	}
	pool := &chatTurn{user: "u", agent: AgentRecord{ID: "a"}}
	if w := pool.partialOneToolWithheld(); len(w) != 0 {
		t.Errorf("an agent on the default pool has nothing withheld, got %v", w)
	}
	if !strings.Contains(strings.Join(some.withheldToolActions(), ","), "fakeonesrv/create_ticket") {
		t.Error("the turn's withheld actions must carry it")
	}
}

// A machine step, and a pipeline run inline from a turn, reach the owner's
// own tools. Both were built from resolveWorkerTools, which holds the
// registered tools only, so a pipeline Builder built over the owner's "geo"
// failed every try with "tool geo is not available to this pipeline" while
// geo sat in Builder's own catalog.
func TestSubRunsReachTheOwnersTools(t *testing.T) {
	turn, _ := machineTurnFixture(t, residentMachine())
	if err := AdminPersistTempTool(turn.udb, "u", TempTool{Name: "geo", Description: "Geocode a place.", CommandTemplate: "echo geo"}); err != nil {
		t.Fatal(err)
	}
	has := func(pool []AgentToolDef) bool {
		for _, td := range pool {
			if td.Tool.Name == "geo" {
				return true
			}
		}
		return false
	}

	if !has(turn.machineCatalog(MachinePhase{Name: "locate", Tools: []string{"geo"}})) {
		t.Fatal("a machine step naming the owner's tool could not reach it")
	}

	pool, _, err := turn.resolveWorkerTools(turn.newToolSession(), false)
	if err != nil {
		t.Fatal(err)
	}
	if has(pool) {
		t.Fatal("resolveWorkerTools now carries custom tools; withOwnTools may add them twice")
	}
	sess := turn.newToolSession()
	pool = turn.withOwnTools(sess, pool)
	if !has(pool) {
		t.Fatal("withOwnTools did not add the owner's tool")
	}
	n := 0
	for _, td := range turn.withOwnTools(sess, pool) {
		if td.Tool.Name == "geo" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("geo appears %d times after a second pass", n)
	}

	// Through the pipeline's own run: the tool stage resolves geo.
	def := PipelineDef{Name: "place-forecast", Stages: []PipelineStage{{Name: "locate", Kind: StageTool, Tool: "geo"}}}
	out, err := turn.runPipelineDefInline(def, "Paris")
	if err != nil && strings.Contains(err.Error(), "not available to this pipeline") {
		t.Fatalf("the inline pipeline could not reach the owner's tool: %v", err)
	}
	_ = out
}

// The catalog is assembled from several independent appends, and a name can
// legitimately appear in two of them — Builder's seed allowlists stay_silent
// and keep_going, and the framework supplies them too. The agent loop coped by
// keeping the first and logging a collision, but its warning blamed "an
// expanded toolbox action and a standalone tool" — a real cause of collisions,
// and not this one. So every reader was pointed at the wrong thing.
func TestDedupeToolsByNameKeepsFirst(t *testing.T) {
	tools := []AgentToolDef{
		{Tool: Tool{Name: "stay_silent", Description: "first"}},
		{Tool: Tool{Name: "keep_going"}},
		{Tool: Tool{Name: "stay_silent", Description: "second"}},
		{Tool: Tool{Name: "find_tools"}},
		{Tool: Tool{Name: "keep_going"}},
	}
	names := []string{"stay_silent", "keep_going", "stay_silent", "find_tools"}

	gotTools, gotNames := dedupeToolsByName(tools, names)

	if len(gotTools) != 3 {
		t.Fatalf("got %d tools, want 3: %+v", len(gotTools), gotTools)
	}
	// First definition wins — same rule the agent loop applied, so behavior
	// is unchanged and only the duplicates disappear.
	if gotTools[0].Tool.Description != "first" {
		t.Errorf("kept the later definition of stay_silent: %q", gotTools[0].Tool.Description)
	}
	// Order is preserved: the catalog's ordering is meaningful to the model.
	want := []string{"stay_silent", "keep_going", "find_tools"}
	for i, w := range want {
		if gotTools[i].Tool.Name != w {
			t.Errorf("tool %d = %q, want %q", i, gotTools[i].Tool.Name, w)
		}
	}
	if len(gotNames) != 3 {
		t.Errorf("names not deduped: %v", gotNames)
	}
}

// A catalog with no duplicates must come back untouched.
func TestDedupeToolsByNameLeavesCleanCatalogAlone(t *testing.T) {
	tools := []AgentToolDef{
		{Tool: Tool{Name: "a"}}, {Tool: Tool{Name: "b"}}, {Tool: Tool{Name: "c"}},
	}
	got, _ := dedupeToolsByName(tools, []string{"a", "b", "c"})
	if len(got) != 3 {
		t.Fatalf("dropped tools from a clean catalog: %d", len(got))
	}
}

func TestDedupeToolsByNameHandlesEmpty(t *testing.T) {
	got, names := dedupeToolsByName(nil, nil)
	if len(got) != 0 || len(names) != 0 {
		t.Error("empty input should stay empty")
	}
}

// desktopCatalog is the catalog of a turn by a user whose desktop app has
// registered one tool: the names offered, and that tool's definition.
func desktopCatalog(t *testing.T, chatPage, fromDesktop bool) ([]string, *AgentToolDef) {
	t.Helper()
	app, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	// What the desktop announced, as the bridge keeps it between connections.
	RootDB.Set("desktop_known_tools", user, []DesktopToolDescriptor{{Name: "contacts_lookup", Desc: "Look up a contact."}})
	a, err := saveAgent(udb, AgentRecord{Name: "Helper", Owner: user, OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	turn := &chatTurn{app: app, agent: a, user: user, udb: udb, ctx: context.Background(), chatPageSend: chatPage, fromDesktopClient: fromDesktop}
	tools, _, err := turn.resolveWorkerTools(turn.newToolSession(), true)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	var desk *AgentToolDef
	for i, td := range tools {
		names = append(names, td.Tool.Name)
		if td.Tool.Name == "from_client_contacts_lookup" {
			desk = &tools[i]
		}
	}
	return names, desk
}

// The desktop tools are offered on every chat-page send, from whichever app,
// so the list does not change with the app a conversation starts from: it
// did, and every switch made the next new conversation re-read its whole
// prompt. They run only from the desktop app.
func TestTheDesktopToolsAreOfferedFromEveryAppAndRunOnlyFromTheDesktop(t *testing.T) {
	fromBrowser, desk := desktopCatalog(t, true, false)
	if desk == nil {
		t.Fatalf("a chat-page send from a browser must be offered the desktop tools: %v", fromBrowser)
	}
	_, err := desk.Handler(context.Background(), map[string]any{"query": "x"})
	if err == nil || !strings.Contains(err.Error(), "only when they write from the gohort desktop app") {
		t.Fatalf("from a browser the tool must refuse and say why, got %v", err)
	}

	fromDesktop, desk := desktopCatalog(t, true, true)
	if !slices.Equal(fromBrowser, fromDesktop) {
		t.Errorf("the tool list must be the same from either app:\nbrowser %v\ndesktop %v", fromBrowser, fromDesktop)
	}
	// From the desktop app the call goes through to the bridge, which here
	// has no desktop connected and says so.
	_, err = desk.Handler(context.Background(), map[string]any{"query": "x"})
	if err == nil || !strings.Contains(err.Error(), "isn't connected") {
		t.Fatalf("from the desktop app the call must reach the bridge, got %v", err)
	}
}

// A turn nobody sent from a chat page (a schedule, a channel, a dispatch) is
// not offered tools it could never run.
func TestTheDesktopToolsStayOffTurnsWithoutAChatPage(t *testing.T) {
	if names, desk := desktopCatalog(t, false, false); desk != nil {
		t.Errorf("a turn without a chat page was offered the desktop tools: %v", names)
	}
}

// The chat page marks its sends, which is what offers the desktop tools.
func TestTheChatPageMarksItsSends(t *testing.T) {
	if src := readFile(t, "runner_http.go"); !strings.Contains(src, "chatPageSend:      true,") {
		t.Error("a chat-page send is no longer marked, so the desktop tools would never be offered")
	}
}

// A toolbox offered one tool per action loses the ones switched off for the
// agent from its catalog, and keeps the rest and every other tool.
func TestTheCatalogLeavesOutASwitchedOffToolboxAction(t *testing.T) {
	sess := &ToolSession{Username: "u"}
	if err := sess.AppendTempTool(&TempTool{Name: "jira", Mode: TempToolModeToolbox, Credential: "no_auth", Expand: true,
		Actions: []TempToolAction{{Name: "search"}, {Name: "create_issue", Method: "POST"}}}); err != nil {
		t.Fatal(err)
	}
	sess.SetWithheldActions([]string{"jira/create_issue"})
	tools := []AgentToolDef{{Tool: Tool{Name: "jira_search"}}, {Tool: Tool{Name: "jira_create_issue"}}, {Tool: Tool{Name: "web_search"}}}
	names := []string{"jira_search", "jira_create_issue", "web_search"}
	tools, names = withoutWithheldToolboxActions(sess, tools, names)
	var kept []string
	for _, td := range tools {
		kept = append(kept, td.Tool.Name)
	}
	if strings.Join(kept, ",") != "jira_search,web_search" || strings.Join(names, ",") != "jira_search,web_search" {
		t.Errorf("kept tools %v, names %v", kept, names)
	}
}

// Through the real paths: an agent's own expanded toolbox loses the action
// switched off for it, both in a turn's catalog (its own tools are built
// after the turn's catalog tells the session what is switched off) and in a
// dispatched run's.
func TestBothCatalogsLeaveOutASwitchedOffToolboxAction(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	jira := TempTool{Name: "jira", Mode: TempToolModeToolbox, Credential: "no_auth", Expand: true, Description: "Jira.",
		Actions: []TempToolAction{
			{Name: "search", Description: "Search.", URLTemplate: "https://jira.example/s", Method: "GET"},
			{Name: "create_issue", Description: "Create.", URLTemplate: "https://jira.example/c", Method: "POST"},
		}}
	a, err := saveAgent(udb, AgentRecord{Name: "Tracker", Owner: user, OrchestratorPrompt: "p",
		Tools: []TempTool{jira}, DisabledToolActions: []string{"jira/create_issue"}})
	if err != nil {
		t.Fatal(err)
	}
	names := func(tools []AgentToolDef) []string {
		var n []string
		for _, td := range tools {
			n = append(n, td.Tool.Name)
		}
		return n
	}
	turn := &chatTurn{app: app, agent: a, user: user, udb: udb, ctx: context.Background()}
	sess := turn.newToolSession()
	if _, _, err := turn.resolveWorkerTools(sess, true); err != nil {
		t.Fatal(err)
	}
	own := names(temptool.BuildAgentToolDefs(sess))
	if !slices.Contains(own, "jira_search") || slices.Contains(own, "jira_create_issue") {
		t.Errorf("a turn's own tools should offer jira_search and not jira_create_issue: %v", own)
	}

	dsess := &ToolSession{Username: user, DB: udb}
	if err := dsess.AppendTempTool(&jira); err != nil {
		t.Fatal(err)
	}
	dturn := &chatTurn{app: app, agent: a, user: user, udb: udb, ctx: context.Background()}
	out := names(dturn.finalizeDispatchCatalog(dsess, []AgentToolDef{{Tool: Tool{Name: "jira_search"}}, {Tool: Tool{Name: "jira_create_issue"}}}))
	if !slices.Contains(out, "jira_search") || slices.Contains(out, "jira_create_issue") {
		t.Errorf("a dispatched run's catalog should offer jira_search and not jira_create_issue: %v", out)
	}
}
