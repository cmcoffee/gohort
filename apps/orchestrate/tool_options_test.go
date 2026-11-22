package orchestrate

import (
	"context"
	"encoding/json"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/gohort/core/ui"
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestReferenceSelectionsFromArgs — models produce a flat string list far more
// reliably than an array of objects, so the CRUD tools take "<kind>:<item_id>".
// The split is on the FIRST colon: a kind never contains one, an id might.
func TestReferenceSelectionsFromArgs(t *testing.T) {
	got := referenceSelectionsFromArgs(map[string]any{
		"attached_sources": []any{
			"system:abc-123",
			" system : def-456 ",   // whitespace around both halves
			"system:abc-123",       // duplicate
			"confluence:space:ENG", // id containing a colon
			"nocolon",              // unusable
			"system:",              // no id
			":abc",                 // no kind
			"",                     // empty
		},
	}, "attached_sources")

	want := []struct{ kind, item string }{
		{"system", "abc-123"},
		{"system", "def-456"},
		{"confluence", "space:ENG"},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d selections, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Kind != w.kind || got[i].ItemID != w.item {
			t.Errorf("selection %d = %+v, want %s/%s", i, got[i], w.kind, w.item)
		}
	}
}

// TestReferenceSelectionsRejectsRatherThanGuesses — a value with no kind must be
// dropped, never defaulted. Inventing one attaches the agent to a source nobody
// chose, and the agent then answers from it as if the owner had asked for that.
func TestReferenceSelectionsRejectsRatherThanGuesses(t *testing.T) {
	got := referenceSelectionsFromArgs(map[string]any{
		"attached_sources": []any{"some-appliance-id", "another"},
	}, "attached_sources")
	if len(got) != 0 {
		t.Errorf("bare ids were accepted as selections: %+v", got)
	}
	if referenceSelectionsFromArgs(map[string]any{}, "attached_sources") != nil {
		t.Error("a missing parameter should parse to nil")
	}
}

// TestRenderReferenceSourcesIsEmptyHanded — with nothing registered the tool has
// to say so, not return a blank that reads as "there are none of these anywhere".
func TestRenderReferenceSourcesEmpty(t *testing.T) {
	out := formatReferenceSources("nobody-with-this-name", nil)
	if strings.TrimSpace(out) == "" {
		t.Fatal("empty listing returned nothing at all")
	}
	if !strings.Contains(out, "No reference sources") {
		t.Errorf("empty listing = %q", out)
	}
}

// TestListReferenceSourcesToolShape — the tool the Builder uses to discover what
// attached_sources will accept. Its name is referenced by the create_agent /
// update_agent parameter docs, so a rename there silently strands that guidance.
func TestListReferenceSourcesToolShape(t *testing.T) {
	td := listReferenceSourcesToolDef("someone")
	if td.Tool.Name != "list_reference_sources" {
		t.Errorf("tool name = %q; create_agent's attached_sources docs point at list_reference_sources", td.Tool.Name)
	}
	if len(td.Tool.Parameters) != 0 {
		t.Errorf("tool takes %d parameters, want none", len(td.Tool.Parameters))
	}
	out, err := td.Handler(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("handler errored: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("handler returned nothing")
	}
}

// TestAttachedSourceToolsDedupeByName — two attachments can mint the same tool
// name (the same system attached twice, or two names that slug identically).
// First wins; a catalog with a duplicate name has undefined dispatch.
func TestAttachedSourceToolsDedupeByName(t *testing.T) {
	turn := &chatTurn{user: "u", agent: AgentRecord{
		ID: "a1",
		AttachedSources: []ReferenceSelection{
			{Kind: "system", ItemID: "x"},
			{Kind: "system", ItemID: "x"},
		},
	}}
	defs := turn.buildAttachedSourceToolDefs(nil)
	seen := map[string]bool{}
	for _, d := range defs {
		if seen[d.Tool.Name] {
			t.Errorf("duplicate tool name %q in the agent's catalog", d.Tool.Name)
		}
		seen[d.Tool.Name] = true
	}
}

// TestNoAttachedSourcesIsNotAnError — an agent with none must build no tools and
// no complaint, since that is every agent that has not opted in.
func TestNoAttachedSourcesIsNotAnError(t *testing.T) {
	turn := &chatTurn{user: "u", agent: AgentRecord{ID: "a1"}}
	if defs := turn.buildAttachedSourceToolDefs(nil); len(defs) != 0 {
		t.Errorf("an agent with no attached sources built %d tools", len(defs))
	}
}

// TestBuilderAuthoringToolsSurvivesANilTurn — t is documented optional and
// three dispatch paths pass nil (agent_dispatch x2, agents_grouped_tool). A
// tool added here that dereferences it panics every delegated Builder run,
// which is what happened: a nil-turn crash reached production from a channel
// message, not from anything a test exercised.
func TestBuilderAuthoringToolsSurvivesANilTurn(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("builderAuthoringTools panicked with a nil turn: %v", r)
		}
	}()
	tools := builderAuthoringTools(&ToolSession{Username: "craig"}, nil)
	if len(tools) == 0 {
		t.Error("no authoring tools built")
	}
	found := false
	for _, td := range tools {
		if td.Tool.Name == "list_reference_sources" {
			found = true
		}
	}
	if !found {
		t.Error("list_reference_sources missing — it should still build without a turn")
	}
}

// TestBuilderAuthoringToolsSurvivesNoSessionEither — belt and braces: neither
// carrier present should degrade to an empty user, not a crash.
func TestBuilderAuthoringToolsSurvivesNoSessionEither(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("builderAuthoringTools panicked with nil session and turn: %v", r)
		}
	}()
	_ = builderAuthoringTools(nil, nil)
}

// A dispatched Builder has no chat turn, and every dispatch path passes nil
// here. The bulletins tool needs only the store and the user, so it comes from
// the session: without it the dispatched Builder wrote a posting script.
func TestBuilderAuthoringToolsCarryBulletinsWithoutATurn(t *testing.T) {
	sess := &ToolSession{Username: "u", DB: &DBase{Store: kvlite.MemStore()}}
	for _, td := range builderAuthoringTools(sess, nil) {
		if td.Tool.Name == "bulletins" {
			return
		}
	}
	t.Fatal("builderAuthoringTools(sess, nil) must include bulletins")
}

// A research source offered to agents is a tool of its own: listed in the
// agent editor under Research sources, in the default set, and kept out of
// an agent whose own list does not tick it.
func TestAResearchSourceIsAnAgentsTool(t *testing.T) {
	connectorOwnerFixture(t)
	spec := json.RawMessage(`{"provider":{"transport":"http","base_url":"https://law.example.test","auth":{"none":true},"operations":{"find":{"path":"/s?q={query}"}}},"provides":[{"capability":"web.search","steps":[{"call":"find"}],"output":{"results":{"path":"hits","fields":{"title":"t","url":"u"}}}}]}`)
	if err := SaveConnector(RootDB, Connector{Name: "caselaw", Kind: service.Kind, Spec: spec}); err != nil {
		t.Fatal(err)
	}
	if err := service.SaveResearchSource(RootDB, service.ResearchSource{Connector: "caselaw", ToolName: "caselaw_search", ToolDescription: "Search court opinions."}); err != nil {
		t.Fatal(err)
	}
	var found *ui.SelectOption
	for _, o := range availableWorkerToolOptions("") {
		if o.Value == "caselaw_search" {
			o := o
			found = &o
		}
	}
	if found == nil || found.Group != ResearchSourceCategory || found.Help != "Search court opinions." {
		t.Fatalf("in the agent editor: %+v", found)
	}
	if !slices.Contains(availableWorkerToolNames(), "caselaw_search") {
		t.Error("not in the default set")
	}
	open := &chatTurn{agent: AgentRecord{}}
	ticked := &chatTurn{agent: AgentRecord{AllowedTools: []string{"caselaw_search"}}}
	unticked := &chatTurn{agent: AgentRecord{AllowedTools: []string{"web_search"}}}
	for name, turn := range map[string]*chatTurn{"default": open, "ticked": ticked, "unticked": unticked} {
		wc := &workerCatalog{t: turn, sess: &ToolSession{Username: "alice"}}
		wc.chooseNames()
		wc.loadTools()
		has := false
		for _, td := range wc.tools {
			has = has || td.Tool.Name == "caselaw_search"
		}
		if has != (name != "unticked") {
			t.Errorf("%s agent has the source: %v", name, has)
		}
	}
}

// The framework's own tools that reach outside sit under Built-In Network,
// first in the list: exactly the ones Private mode takes away, unless a tool
// names a heading of its own. Nothing else is under it.
func TestBuiltInNetworkIsWhatPrivateModeTakesAway(t *testing.T) {
	net := map[string]bool{}
	for _, n := range internetWorkerToolNames() {
		net[n] = true
	}
	opts := availableWorkerToolOptions("")
	if len(opts) == 0 || !net["browse_page"] {
		t.Fatalf("no tools to group (%d options; browse_page reaching outside: %v)", len(opts), net["browse_page"])
	}
	under := 0
	for i, o := range opts {
		if o.Group != builtinNetworkGroup {
			continue
		}
		under++
		if !net[o.Value] {
			t.Errorf("%s is under %s but Private mode keeps it", o.Value, builtinNetworkGroup)
		}
		if i >= under {
			t.Errorf("%s is under %s but not at the top of the list", o.Value, builtinNetworkGroup)
		}
	}
	for _, o := range opts {
		switch o.Value {
		case "browse_page", "screenshot_page":
			if o.Group != builtinNetworkGroup {
				t.Errorf("%s is under %q, not %s", o.Value, o.Group, builtinNetworkGroup)
			}
		case "image":
			// Reaches outside, but names a heading of its own.
			if o.Group == builtinNetworkGroup {
				t.Errorf("image lost its own heading to %s", builtinNetworkGroup)
			}
		}
	}
}

// What a person's apps expose is in their agents' tool picker under the
// app's name, never in the default set (an action writes), and an agent
// has one only when it is ticked.
func TestAnAppsToolsAreTickedOnAnAgent(t *testing.T) {
	prev := AppToolsFor
	t.Cleanup(func() { AppToolsFor = prev })
	AppToolsFor = func(sess *ToolSession, user string) []AgentToolDef {
		if user != "alice" {
			return nil
		}
		return []AgentToolDef{{Tool: Tool{Name: "books_add", Description: "Add a book.", Category: "App: Reading List"},
			Handler: func(context.Context, map[string]any) (string, error) { return "added", nil }}}
	}
	var found *ui.SelectOption
	for _, o := range availableWorkerToolOptions("alice") {
		if o.Value == "books_add" {
			o := o
			found = &o
		}
	}
	if found == nil || found.Group != "App: Reading List" {
		t.Fatalf("in alice's tool picker: %+v", found)
	}
	if slices.Contains(availableWorkerToolNames(), "books_add") {
		t.Error("an app tool is in the default set")
	}
	open := &chatTurn{agent: AgentRecord{}}
	ticked := &chatTurn{agent: AgentRecord{AllowedTools: []string{"web_search", "books_add"}}}
	for name, turn := range map[string]*chatTurn{"default": open, "ticked": ticked} {
		wc := &workerCatalog{t: turn, sess: &ToolSession{Username: "alice"}}
		wc.chooseNames()
		wc.loadTools()
		has := false
		for _, td := range wc.tools {
			has = has || td.Tool.Name == "books_add"
		}
		if has != (name == "ticked") {
			t.Errorf("%s agent has the app tool: %v", name, has)
		}
	}
	// Run for someone else, the ticked agent does not reach alice's app.
	wc := &workerCatalog{t: ticked, sess: &ToolSession{Username: "bob"}}
	wc.chooseNames()
	wc.loadTools()
	for _, td := range wc.tools {
		if td.Tool.Name == "books_add" {
			t.Error("an agent run for bob has alice's app tool")
		}
	}
}

// The Security page lists every action of the toolboxes an agent has, from
// its owner's pool and its own, each marked as one that reads or writes, so a
// toolbox's actions can be switched off one by one.
func TestAToolboxsActionsCanBeSwitchedOffOneByOne(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	jira := TempTool{Name: "jira", Mode: TempToolModeToolbox, Credential: "no_auth", Description: "Jira.",
		Actions: []TempToolAction{
			{Name: "search", Description: "Search issues. With JQL.", URLTemplate: "https://jira.example/s", Method: "POST", Reads: true},
			{Name: "add_comment", Description: "Comment on an issue.", URLTemplate: "https://jira.example/c", Method: "POST"},
		}}
	if err := AdminPersistTempTool(udb, "u", jira); err != nil {
		t.Fatal(err)
	}
	own := TempTool{Name: "notes", Mode: TempToolModeToolbox, Credential: "no_auth",
		Actions: []TempToolAction{{Name: "list", URLTemplate: "https://notes.example/l"}}}
	plain := TempTool{Name: "weather", Mode: TempToolModeAPI}
	agent := AgentRecord{ID: "a1", Owner: "u", Tools: []TempTool{own, plain}}

	got := map[string]ui.SelectOption{}
	for _, o := range narrowableActionOptions(udb, "u", agent) {
		got[o.Value] = o
	}
	for value, label := range map[string]string{
		"jira/search":      "jira - search (reads)",
		"jira/add_comment": "jira - add_comment (writes)",
		"notes/list":       "notes - list (reads)",
	} {
		if o, ok := got[value]; !ok || o.Label != label || o.Group != "Toolbox "+strings.SplitN(value, "/", 2)[0] {
			t.Errorf("%s: offered %+v, want label %q", value, o, label)
		}
	}
	if got["jira/search"].Help != "Search issues." {
		t.Errorf("an action's row should carry its first sentence: %q", got["jira/search"].Help)
	}
	for v := range got {
		if strings.HasPrefix(v, "weather/") {
			t.Errorf("a tool that is not a toolbox was offered by action: %s", v)
		}
	}
}
