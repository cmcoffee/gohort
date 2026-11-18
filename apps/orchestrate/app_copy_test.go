package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/apptools"
	"github.com/cmcoffee/snugforge/kvlite"
)

// copyTestTurn is a Builder turn of "u" with an orchestrate app registered,
// so app_def, agents and pipelines all see one store.
func copyTestTurn(t *testing.T) (*chatTurn, Database) {
	t.Helper()
	turn, _ := folderTestTurn(t)
	T := &OrchestrateApp{}
	T.DB = &DBase{Store: kvlite.MemStore()}
	prevHost, prevBase := agenthost.Get(), orchestrateBaseDB
	agenthost.Register(T)
	orchestrateBaseDB = T.DB
	t.Cleanup(func() { agenthost.Register(prevHost); orchestrateBaseDB = prevBase })
	turn.app, turn.udb = T, UserDB(T.DB, "u")
	return turn, turn.udb
}

func copyTestApp(t *testing.T, turn *chatTurn, extra map[string]any) string {
	t.Helper()
	args := map[string]any{
		"name":     "Desk",
		"sections": []any{map[string]any{"id": "page", "kind": "html", "html": "<!DOCTYPE html><html><body><h1>Desk</h1></body></html>"}},
	}
	for k, v := range extra {
		args[k] = v
	}
	out, err := turn.appDefCreateOrUpdate(args, false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return out
}

// Binding one of the person's agents gives the app its own copy, bound in
// its place: the same name, a note of what it was copied from, its
// sub-agents and the tools it names (a tool reaching a credential keeping
// its approval), and the original left as it was. Outside the app the name
// still means the original; inside it, the app's own.
func TestBindingAnAgentGivesTheAppItsOwnCopy(t *testing.T) {
	turn, udb := copyTestTurn(t)
	if err := AdminPersistTempTool(udb, "u", TempTool{Name: "crm_lookup", Description: "d", Mode: TempToolModeAPI, Credential: "crm", CommandTemplate: "https://crm.invalid/{q}"}); err != nil {
		t.Fatal(err)
	}
	orig, err := saveAgent(udb, AgentRecord{Owner: "u", Name: "Research", OrchestratorPrompt: "Dig in.", AllowedTools: []string{"crm_lookup", "web_search"}, Everyone: true})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := saveAgent(udb, AgentRecord{Owner: "u", Name: "Reader", OrchestratorPrompt: "Read.", OwnedBy: orig.ID})
	if err != nil {
		t.Fatal(err)
	}

	out := copyTestApp(t, turn, map[string]any{"agent_id": "Research"})
	if !strings.Contains(out, `Copied into the app, as its own: agent "Research"`) || !strings.Contains(out, "crm_lookup") {
		t.Errorf("the save's reply: %s", out)
	}
	spec, _ := LoadAppSpec("u", "desk")
	cp, ok := loadAgent(udb, spec.AgentID)
	if !ok || cp.ID == orig.ID || cp.OwningApp != "desk" || cp.Name != "Research" || cp.CopiedFrom == nil || cp.CopiedFrom.ID != orig.ID {
		t.Fatalf("the app's agent: %+v", cp)
	}
	if cp.Everyone {
		t.Error("the copy took the original's sharing")
	}
	var subCopy AgentRecord
	for _, a := range listAgents(udb, "u") {
		if a.OwnedBy == cp.ID {
			subCopy = a
		}
	}
	if subCopy.Name != "Reader" || subCopy.OwningApp != "desk" {
		t.Errorf("the sub-agent's copy: %+v", subCopy)
	}
	if got, _ := loadAgent(udb, sub.ID); got.OwnedBy != orig.ID || got.OwningApp != "" {
		t.Errorf("the original sub-agent changed: %+v", got)
	}
	if got, _ := loadAgent(udb, orig.ID); got.OwningApp != "" || got.CopiedFrom != nil {
		t.Errorf("the original changed: %+v", got)
	}
	at, ok := apptools.ByName("u", "desk", "crm_lookup")
	if !ok || at.CopiedFrom == nil {
		t.Fatalf("the tool it names, in the app's space: %+v %v", at, ok)
	}
	if why := apptools.Held("u", "desk", at.Tool, apptools.LoadReview("u", "desk")); why != "" {
		t.Errorf("an unchanged copy did not keep its approval: %s", why)
	}
	if _, ok := apptools.ByName("u", "desk", "web_search"); ok {
		t.Error("gohort's own tool was copied into the app")
	}
	if got, ok := findAgentByNameOrID(udb, "u", "Research"); !ok || got.ID != orig.ID {
		t.Errorf("outside the app, Research means %+v", got)
	}
	if got, err := turn.app.appOwnAgent("u", "desk", spec.AgentID, "Research"); err != nil || got.ID != cp.ID {
		t.Errorf("inside the app, Research means %+v (%v)", got, err)
	}

	// Saving again with the app's own agent copies nothing more.
	out, err = turn.appDefCreateOrUpdate(map[string]any{"id": "desk", "agent_id": cp.ID, "description": "A desk."}, true)
	if err != nil || strings.Contains(out, "Copied into the app") {
		t.Errorf("binding the app's own agent again: %v %s", err, out)
	}
}

// An app that points at an agent of the person's makes it its own the first
// time Builder or Anvil saves it (piece D): the copy takes what the original
// learned (facts whoever said them, and its working notes), once, the
// tools it names come into the app's space, and the app then holds its own
// tools.
func TestAnAppsFirstChangeMakesWhatItPointsAtItsOwn(t *testing.T) {
	turn, udb := copyTestTurn(t)
	if err := AdminPersistTempTool(udb, "u", TempTool{Name: "lookup", Description: "d", CommandTemplate: "echo x"}); err != nil {
		t.Fatal(err)
	}
	// A colleague's tool the person took: copied in too, so the app travels.
	if err := AdminPersistTempTool(udb, "lender", TempTool{Name: "wiki_read", Description: "d", CommandTemplate: "echo lent"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolSharedWith(udb, "lender", "wiki_read", []string{"u"}); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalToolAdopted(udb, "u", "wiki_read", "lender", true); err != nil {
		t.Fatal(err)
	}
	orig, _ := saveAgent(udb, AgentRecord{Owner: "u", Name: "Helper", OrchestratorPrompt: "Help.", AllowedTools: []string{"lookup", "wiki_read"}})
	ns := factsNamespace(orig.ID)
	StoreMemoryFactP(udb, ns, "Prices are in euros.", FactWritePolicy{})
	StoreMemoryFactP(udb, ns, "Dana prefers short answers.", FactWritePolicy{Speaker: "Dana", SpeakerHandle: "dana"})
	SaveOperatingNotes(udb, ns, "Answer in two lines.")
	copyTestApp(t, turn, nil)
	spec, _ := LoadAppSpec("u", "desk")
	spec.AgentID = orig.ID
	SaveAppSpec(spec)

	out, err := turn.appDefCreateOrUpdate(map[string]any{"id": "desk", "description": "A desk."}, true)
	if err != nil || !strings.Contains(out, `agent "Helper"`) || !strings.Contains(out, "What the originals learned came with them") {
		t.Fatalf("the first change: %v %s", err, out)
	}
	spec, _ = LoadAppSpec("u", "desk")
	cp, ok := loadAgent(udb, spec.AgentID)
	if !ok || cp.ID == orig.ID || cp.OwningApp != "desk" {
		t.Fatalf("the app's agent after its first change: %+v", cp)
	}
	facts := ListMemoryFacts(udb, factsNamespace(cp.ID))
	var dana bool
	for _, f := range facts {
		if strings.Contains(f.Note, "Dana") && f.Speaker == "Dana" {
			dana = true
		}
	}
	if len(facts) != 2 || !dana {
		t.Errorf("the copy's facts: %+v", facts)
	}
	if n := LoadOperatingNotes(udb, factsNamespace(cp.ID)).Text; !strings.Contains(n, "two lines") {
		t.Errorf("the copy's working notes: %q", n)
	}
	if len(ListMemoryFacts(udb, ns)) != 2 {
		t.Error("the original lost what it learned")
	}
	if _, ok := apptools.ByName("u", "desk", "lookup"); !ok {
		t.Error("the tool the agent names was not copied into the app")
	}
	if at, ok := apptools.ByName("u", "desk", "wiki_read"); !ok || at.CopiedFrom == nil || !strings.Contains(at.CopiedFrom.Name, "lender") || at.Tool.CommandTemplate != "echo lent" {
		t.Errorf("the colleague's tool it names, in the app: %+v %v", at, ok)
	}
	if !spec.OwnTools {
		t.Error("the app does not hold its own tools after its first change")
	}

	// Saving again copies nothing more, and teaches nothing twice.
	out, err = turn.appDefCreateOrUpdate(map[string]any{"id": "desk", "description": "A desk, again."}, true)
	if err != nil || strings.Contains(out, "Copied into the app") || len(ListMemoryFacts(udb, factsNamespace(cp.ID))) != 2 {
		t.Errorf("a second save: %v %s", err, out)
	}
}

// An app that holds its own tools cuts the person's pool for its agents,
// keeping its own space; while one of its agents is allowed every tool the
// app keeps loading the pool, and its save says why.
func TestAnAppHoldingItsOwnToolsCutsThePool(t *testing.T) {
	turn, udb := copyTestTurn(t)
	for _, n := range []string{"lookup", "pool_only"} {
		if err := AdminPersistTempTool(udb, "u", TempTool{Name: n, Description: "d", CommandTemplate: "echo x"}); err != nil {
			t.Fatal(err)
		}
	}
	helper, _ := saveAgent(udb, AgentRecord{Owner: "u", Name: "Helper", OrchestratorPrompt: "Help.", AllowedTools: []string{"lookup"}})
	copyTestApp(t, turn, map[string]any{"agent_id": helper.ID})
	spec, _ := LoadAppSpec("u", "desk")
	if !spec.OwnTools {
		t.Fatal("a new app whose agent names its tools does not hold its own")
	}
	load := func(a AgentRecord) map[string]bool {
		tt := &chatTurn{user: "u", udb: udb, agent: a}
		sess := &ToolSession{}
		tt.loadAgentTempTools(sess, "u", udb)
		got := map[string]bool{}
		for _, x := range sess.TempTools {
			got[x.Name] = true
		}
		return got
	}
	cp, _ := loadAgent(udb, spec.AgentID)
	if got := load(cp); !got["lookup"] || got["pool_only"] {
		t.Errorf("the app's agent loaded %v: want its own lookup and none of the pool", got)
	}
	if got := load(helper); !got["lookup"] {
		t.Errorf("the person's own agent lost its pool: %v", got)
	}

	// An agent of the app allowed every tool holds the cut back.
	roamer, err := saveAgent(udb, AgentRecord{Owner: "u", Name: "Roamer", OrchestratorPrompt: "Roam.", OwningApp: "desk", appEdit: "Builder"})
	if err != nil {
		t.Fatal(err)
	}
	if got := load(roamer); got["pool_only"] {
		t.Errorf("while the app holds its own tools, its agent allowed every tool loaded the pool: %v", got)
	}
	out, err := turn.appDefCreateOrUpdate(map[string]any{"id": "desk", "description": "A desk."}, true)
	if err != nil || !strings.Contains(out, "Roamer is allowed every tool") {
		t.Fatalf("a save with an agent allowed every tool: %v %s", err, out)
	}
	spec, _ = LoadAppSpec("u", "desk")
	if spec.OwnTools {
		t.Error("the app holds its own tools while one of its agents is allowed every tool")
	}
	if got := load(roamer); !got["pool_only"] || !got["lookup"] {
		t.Errorf("with the cut held, the app's agent should load the pool beside its own tools: %v", got)
	}
}

// Binding a pipeline gives the app its own copy, each stage's agent the
// app's own too; and an app's pipeline saved by Builder naming an agent by
// name stores the app's own agent of that name.
func TestBindingAPipelineCopiesWhatItsStagesRun(t *testing.T) {
	turn, udb := copyTestTurn(t)
	orig, _ := saveAgent(udb, AgentRecord{Owner: "u", Name: "Writer", OrchestratorPrompt: "Write."})
	pipe, err := SavePipelineDefBy(udb, PipelineDef{Owner: "u", Name: "Brief", Stages: []PipelineStage{
		{Name: "draft", Kind: StageAgent, Agent: "Writer", Prompt: "{input}"},
	}}, "create", "")
	if err != nil {
		t.Fatal(err)
	}
	out := copyTestApp(t, turn, map[string]any{"pipeline_id": "Brief", "sections": []any{
		map[string]any{"id": "page", "kind": "html", "html": "<!DOCTYPE html><html><body><h1>Desk</h1></body></html>"},
		map[string]any{"id": "run", "kind": "pipeline"},
	}})
	if !strings.Contains(out, `pipeline "Brief"`) || !strings.Contains(out, `agent "Writer"`) {
		t.Errorf("the save's reply: %s", out)
	}
	spec, _ := LoadAppSpec("u", "desk")
	cp, ok := LoadPipelineDef(udb, "u", spec.PipelineID)
	if !ok || cp.ID == pipe.ID || cp.OwningApp != "desk" || cp.CopiedFrom == nil {
		t.Fatalf("the app's pipeline: %+v", cp)
	}
	stageAgent, ok := loadAgent(udb, cp.Stages[0].Agent)
	if !ok || stageAgent.ID == orig.ID || stageAgent.OwningApp != "desk" {
		t.Errorf("the stage runs %+v", stageAgent)
	}
	if got, _ := LoadPipelineDef(udb, "u", pipe.ID); got.Stages[0].Agent != "Writer" || got.OwningApp != "" {
		t.Errorf("the original pipeline changed: %+v", got)
	}
	if got, ok := findPipelineByNameOrID(udb, "u", "Brief"); !ok || got.ID != pipe.ID {
		t.Errorf("outside the app, Brief means %+v", got)
	}

	cp.Stages[0].Agent = "Writer"
	saved, err := SavePipelineDefBy(udb, cp, "update", "Anvil")
	if err != nil || saved.Stages[0].Agent != stageAgent.ID {
		t.Errorf("an app's pipeline naming Writer stores %q, want the app's own %q (%v)", saved.Stages[0].Agent, stageAgent.ID, err)
	}
}
