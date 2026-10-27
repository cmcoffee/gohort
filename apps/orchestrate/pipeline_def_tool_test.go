package orchestrate

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Builder writes a stage as JSON; render and card have to survive the parse,
// or a card it designed would silently come out as text.
func TestThePipelineToolKeepsACardLayout(t *testing.T) {
	stages, err := parsePipelineStages([]any{map[string]any{
		"name": "judge", "kind": "worker", "prompt": "Judge it.",
		"output": []any{"verdict", "confidence"},
		"render": " Card ", "card": map[string]any{"title": "verdict", "badges": "confidence"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != 1 || stages[0].Render != "card" || stages[0].Card["title"] != "verdict" || stages[0].Card["badges"] != "confidence" {
		t.Fatalf("render/card lost in the parse: %+v", stages)
	}
	// And Builder is told they exist, with a worked example.
	if !strings.Contains(pipelineHelpText, "=== CARDS ===") || !strings.Contains(pipelineHelpText, `"render": "card"`) {
		t.Error("the pipeline help must document cards")
	}
}

// Builder declares follow-ups as data; they have to survive the parse, show in
// what it reads back, and be served on the pipeline's own page.
func TestThePipelineToolTakesFollowUps(t *testing.T) {
	fus, err := parsePipelineFollowUps([]any{map[string]any{
		"name": "Write report", "description": "A briefing",
		"stages": []any{map[string]any{"name": "w", "prompt": "Write a briefing from {input}"}},
	}})
	if err != nil || len(fus) != 1 || fus[0].Name != "Write report" || len(fus[0].Stages) != 1 {
		t.Fatalf("follow-ups lost in the parse: %+v %v", fus, err)
	}
	if _, err := parsePipelineFollowUps("not a list"); err == nil {
		t.Error("a follow-up list that is not a list must be refused")
	}
	slim := string(slimPipelineJSON(PipelineDef{Name: "p", Stages: []PipelineStage{{Name: "s"}}, FollowUps: fus}))
	if !strings.Contains(slim, `"followups":[{"name":"Write report"`) {
		t.Errorf("get must show the follow-ups: %s", slim)
	}
	if !strings.Contains(pipelineHelpText, "=== FOLLOW-UPS ===") || !strings.Contains(pipelineHelpText, "{children}") {
		t.Error("the help must document follow-ups and what they read")
	}
	read := func(f string) string {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	mux := routeMux(&OrchestrateApp{})
	_, followups := mux.Handler(httptest.NewRequest("GET", "/api/pipelines/p1/followups", nil))
	_, followup := mux.Handler(httptest.NewRequest("POST", "/api/pipelines/p1/followup/report/run-1", nil))
	if followups == "" || followup == "" ||
		!strings.Contains(read("pipeline_page.go"), `FollowUpsURL: "api/pipelines/" + url_(def.ID) + "/followups"`) ||
		!strings.Contains(read("app_def_tool_build.go"), `FollowUpsURL: "pipeline/followups"`) {
		t.Error("the pipeline page and app sections must offer follow-ups, and the routes must reach them")
	}
}

// The authoring surface for loop: a nested body has to survive the tool's
// parser on create AND update. A field the tool documents but the parser
// drops is the failure shape this package has produced twice.

func TestParsePipelineStages_LoopRoundTrip(t *testing.T) {
	raw := []any{
		map[string]any{
			"name": "rounds", "kind": "loop",
			"count": float64(3), "until": "check.done", "collect": "all",
			"body": []any{
				map[string]any{"name": "argue", "kind": "agent", "agent": "debater",
					"prompt": "pass {iteration}: rebut {prev}", "think": true},
				map[string]any{"name": "check", "prompt": "settled?",
					"output": []any{map[string]any{"name": "done", "type": "bool", "required": true}}},
			},
		},
	}
	got, err := parsePipelineStages(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(got))
	}
	s := got[0]
	if s.Kind != StageLoop || s.Count != 3 || s.Until != "check.done" || s.Collect != "all" {
		t.Errorf("loop fields dropped: %+v", s)
	}
	if len(s.Body) != 2 {
		t.Fatalf("body dropped: %+v", s.Body)
	}
	// Nested stages keep the fields the flat parser handles, including the
	// two that used to vanish silently.
	// Think reads as a string now: as a *bool the store could not carry
	// "off" at all (gob drops a false pointer), so "off" became "inherit"
	// on the next read.
	if s.Body[0].Agent != "debater" || StageThinkMode(s.Body[0]) != "on" {
		t.Errorf("body stage fields dropped: %+v", s.Body[0])
	}
	if len(s.Body[1].Output) != 1 || s.Body[1].Output[0].Type != FieldBool {
		t.Errorf("body stage output dropped: %+v", s.Body[1].Output)
	}
	// And the whole thing has to actually validate.
	def := PipelineDef{Name: "t", Stages: got}
	if err := def.Validate(); err != nil {
		t.Errorf("parsed loop should validate: %v", err)
	}
}

func TestParsePipelineStages_LoopCountAsString(t *testing.T) {
	// Models send numbers as strings routinely; coerce rather than drop.
	got, err := parsePipelineStages([]any{
		map[string]any{"name": "rounds", "kind": "loop", "count": "2",
			"body": []any{map[string]any{"name": "step", "prompt": "x"}}},
	})
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got[0].Count != 2 {
		t.Errorf("count = %d, want 2", got[0].Count)
	}
}

func TestParsePipelineStages_BranchRoundTrip(t *testing.T) {
	got, err := parsePipelineStages([]any{
		map[string]any{"name": "frame", "prompt": "screen it",
			"output": []any{map[string]any{"name": "rejected", "type": "bool", "required": true}}},
		map[string]any{"name": "gate", "kind": "branch",
			"when": "frame.rejected", "skip_to": "report"},
		map[string]any{"name": "work", "prompt": "do it"},
		map[string]any{"name": "report", "prompt": "wrap up"},
	})
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	gate := got[1]
	if gate.Kind != StageBranch || gate.When != "frame.rejected" || gate.SkipTo != "report" {
		t.Errorf("branch fields dropped: %+v", gate)
	}
	if err := (PipelineDef{Name: "t", Stages: got}).Validate(); err != nil {
		t.Errorf("parsed branch should validate: %v", err)
	}
}

func TestParsePipelineStages_ModelTierRoundTrip(t *testing.T) {
	got, err := parsePipelineStages([]any{
		map[string]any{"name": "plan", "prompt": "decompose", "model": "LEAD"},
		map[string]any{"name": "note", "prompt": "transform"},
	})
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got[0].Model != "lead" {
		t.Errorf("model = %q, want normalized \"lead\"", got[0].Model)
	}
	if got[1].Model != "" {
		t.Errorf("unset model should stay empty, got %q", got[1].Model)
	}
	if err := (PipelineDef{Name: "t", Stages: got}).Validate(); err != nil {
		t.Errorf("parsed tiers should validate: %v", err)
	}
}

func TestParsePipelineStages_ToolStageRoundTrip(t *testing.T) {
	got, err := parsePipelineStages([]any{
		map[string]any{"name": "plan", "prompt": "x",
			"output": []any{map[string]any{"name": "expr", "type": "string"}}},
		map[string]any{"name": "math", "kind": "tool", "tool": "calculate",
			"args": map[string]any{"expr": "{stage:plan.expr}", "places": float64(2)}},
	})
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	m := got[1]
	if m.Kind != StageTool || m.Tool != "calculate" {
		t.Errorf("tool fields dropped: %+v", m)
	}
	if m.Args["expr"] != "{stage:plan.expr}" {
		t.Errorf("arg template dropped: %+v", m.Args)
	}
	// Non-string arg values coerce rather than vanishing.
	if m.Args["places"] != "2" {
		t.Errorf("numeric arg = %q, want coerced \"2\"", m.Args["places"])
	}
	if err := (PipelineDef{Name: "t", Stages: got}).Validate(); err != nil {
		t.Errorf("parsed tool stage should validate: %v", err)
	}
}

// Builder writes cite and verify as JSON; both must survive the parse, and the
// help must say how sources, citing and checking fit together.
func TestThePipelineToolTakesCiteAndVerify(t *testing.T) {
	stages, err := parsePipelineStages([]any{
		map[string]any{"name": "write", "prompt": "Cite from {sources}", "cite": true},
		map[string]any{"name": "check", "kind": "verify", "check": " write "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !stages[0].Cite || stages[1].Kind != "verify" || stages[1].Check != "write" {
		t.Fatalf("cite/verify lost in the parse: %+v", stages)
	}
	for _, want := range []string{"=== SOURCES ===", "{sources}", `"kind": "verify"`, "unverified_figures"} {
		if !strings.Contains(pipelineHelpText, want) {
			t.Errorf("the help must cover %q", want)
		}
	}
}

// Builder writes gather, a panel's research and count_from as JSON; all three
// must survive the parse (count_from was documented and silently dropped), and
// the help must say how gathering works.
func TestThePipelineToolTakesGatherAndResearch(t *testing.T) {
	stages, err := parsePipelineStages([]any{
		map[string]any{"name": "look", "kind": "gather", "prompt": "solar cost", "count": float64(4), "count_from": " {pages} "},
		map[string]any{"name": "debate", "kind": "panel", "panel": []any{"Pro", "Con"}, "research": float64(2), "prompt": "{research}"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stages[0].Kind != "gather" || stages[0].Count != 4 || stages[0].CountFrom != "{pages}" || stages[1].Research != 2 {
		t.Fatalf("gather/research/count_from lost in the parse: %+v", stages)
	}
	for _, want := range []string{`"kind": "gather"`, `"research": 2`, "{research}", "count_from (panel, loop, gather)"} {
		if !strings.Contains(pipelineHelpText, want) {
			t.Errorf("the help must cover %q", want)
		}
	}
}

// A gather or a researching panel names no tools, but a run started from a
// pipeline page or an app gets only the tools stages name: they must be named
// for it, across the follow-ups and the suggest pipeline too.
func TestStandaloneRunsGetTheToolsGatheringReadsWith(t *testing.T) {
	plain := []PipelineStage{{Name: "w", Prompt: "x"}}
	for name, def := range map[string]PipelineDef{
		"a gather":            {Stages: []PipelineStage{{Name: "g", Kind: StageGather}}},
		"a researching panel": {Stages: []PipelineStage{{Name: "p", Kind: StagePanel, Panel: []string{"a", "b"}, Research: 1}}},
		"a follow-up gather":  {Stages: plain, FollowUps: []PipelineDef{{Name: "f", Stages: []PipelineStage{{Name: "g", Kind: StageGather}}}}},
		"a suggest gather":    {Stages: plain, Suggest: &PipelineDef{Stages: []PipelineStage{{Name: "g", Kind: StageGather}}}},
	} {
		got := strings.Join(pipelineDeclaredToolNames(def), ",")
		if got != "fetch_url,web_search" {
			t.Errorf("%s must bring web_search and fetch_url, got %q", name, got)
		}
	}
	if got := pipelineDeclaredToolNames(PipelineDef{Stages: plain}); len(got) != 0 {
		t.Errorf("a stage that names nothing still gets nothing: %v", got)
	}
}

// Builder writes the suggest recipe as JSON; {} removes it.
func TestThePipelineToolTakesSuggest(t *testing.T) {
	sg, err := parsePipelineSuggest(map[string]any{"name": "Find a topic", "stages": []any{
		map[string]any{"name": "news", "kind": "gather", "prompt": "top stories today"},
		map[string]any{"name": "pick", "prompt": "Five questions from {stage:news}"},
	}})
	if err != nil || sg == nil || sg.Name != "Find a topic" || len(sg.Stages) != 2 || sg.Stages[0].Kind != StageGather {
		t.Fatalf("suggest lost in the parse: %+v %v", sg, err)
	}
	if sg, err := parsePipelineSuggest(map[string]any{}); sg != nil || err != nil {
		t.Errorf("{} removes it: %+v %v", sg, err)
	}
	if !strings.Contains(pipelineHelpText, "=== SUGGEST ===") {
		t.Error("the help must cover suggest")
	}
}

// A refused create stores nothing, and the reflex that follows it is
// action="update" — the fix belongs on the definition that failed, so updating
// "it" is the natural next move. One observed Forge build made that call twice
// in six rounds, both times after a refusal, and spent both of them being told
// the record did not exist. The tool now stores it.

func upsertTurn(db Database) *chatTurn {
	return &chatTurn{user: "u", udb: db, agent: AgentRecord{ID: "ag"}}
}

func validStages() []any {
	return []any{
		map[string]any{"name": "draft", "kind": "worker", "prompt": "write about {input}"},
		map[string]any{"name": "polish", "kind": "worker", "prompt": "improve {prev}"},
	}
}

func TestUpdateStoresACompleteDefinitionThatWasNeverCreated(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	ct := upsertTurn(db)

	out, err := ct.pipelineCreateOrUpdate(map[string]any{
		"name":   "forge_pipeline",
		"stages": validStages(),
	}, true)
	if err != nil {
		t.Fatalf("update carrying a full definition must save it, got: %v", err)
	}
	// It has to read as a create, or the next turn believes it edited something.
	if !strings.Contains(out, "Created") {
		t.Errorf("the result must say it created rather than updated, got:\n%s", out)
	}
	if !strings.Contains(out, "nothing was stored under that name") {
		t.Errorf("say why this was a create; a silent upsert hides a typo'd name:\n%s", out)
	}
	if _, ok := ct.findPipeline(map[string]any{"name": "forge_pipeline"}); !ok {
		t.Fatal("the pipeline is not retrievable after the upsert")
	}
}

// The upsert must not paper over a broken definition — validation still runs,
// so the round still ends in a refusal, just the RIGHT one.
func TestUpsertStillValidates(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	ct := upsertTurn(db)
	_, err := ct.pipelineCreateOrUpdate(map[string]any{
		"name": "broken",
		"stages": []any{
			map[string]any{"name": "a", "kind": "worker", "prompt": "see {stage:nope}"},
		},
	}, true)
	if err == nil {
		t.Fatal("an unresolvable stage reference must still refuse")
	}
	if !strings.Contains(err.Error(), "not runnable") {
		t.Errorf("the refusal must be the validation one, got: %v", err)
	}
}

// A partial patch is still an error: there is no definition here to store.
func TestUpdateWithNoStagesStillReportsMissing(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	ct := upsertTurn(db)
	_, err := ct.pipelineCreateOrUpdate(map[string]any{
		"name":        "ghost",
		"description": "just a description",
	}, true)
	if err == nil {
		t.Fatal("updating a description on a pipeline that does not exist must refuse")
	}
	if !strings.Contains(err.Error(), "no stages to store") {
		t.Errorf("say that the call carried nothing storable, got: %v", err)
	}
}

// A real update still overwrites in place rather than minting a second record.
func TestUpdateOfAnExistingPipelineStillUpdates(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	ct := upsertTurn(db)
	if _, err := ct.pipelineCreateOrUpdate(map[string]any{
		"name": "real", "stages": validStages(),
	}, false); err != nil {
		t.Fatalf("create: %v", err)
	}
	out, err := ct.pipelineCreateOrUpdate(map[string]any{
		"name": "real", "description": "now with a description",
	}, true)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !strings.Contains(out, "Updated") {
		t.Errorf("an existing pipeline must still report Updated, got:\n%s", out)
	}
	if got := ListPipelineDefs(ct.udb, "u"); len(got) != 1 {
		t.Fatalf("update must not mint a second record, have %d", len(got))
	}
}

// The pipeline tool says what the machine tool says about the same
// mistake — a prompt hand-rolling the JSON its declared fields already
// produce. The author on this path is a model, which is the one most
// likely to make it.
func TestThePipelineToolReportsItsOwnAdvice(t *testing.T) {
	ct := upsertTurn(&DBase{Store: kvlite.MemStore()})

	out, err := ct.pipelineCreateOrUpdate(map[string]any{
		"name": "Research",
		"stages": []any{
			map[string]any{"name": "plan", "kind": "worker",
				"prompt": "Break it up. Respond only with valid JSON.",
				"output": []any{map[string]any{"name": "queries", "type": "list", "desc": "searches"}}},
			map[string]any{"name": "answer", "kind": "worker", "prompt": "answer from {stage:plan.queries}"},
		},
	}, false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(out, "Worth a look") || !strings.Contains(out, "stage plan") {
		t.Errorf("the tool said nothing about it:\n%s", out)
	}
	// Advice, not refusal.
	if !strings.Contains(out, "Created pipeline") {
		t.Errorf("it should still have been stored:\n%s", out)
	}

	clean, err := ct.pipelineCreateOrUpdate(map[string]any{
		"name":   "Simple",
		"stages": []any{map[string]any{"name": "answer", "kind": "worker", "prompt": "reply plainly"}},
	}, false)
	if err != nil {
		t.Fatalf("create clean: %v", err)
	}
	if strings.Contains(clean, "Worth a look") {
		t.Errorf("nothing to say, so say nothing:\n%s", clean)
	}
	// And the spec now warns in advance, the way the machine spec does.
	if !strings.Contains(pipelineHelpText, "NEVER ask for JSON in the prompt") {
		t.Error("the spec should say it before the mistake, not only after")
	}

	// A pipeline written before the rule existed will never see a create
	// reply again, so get and list are the only places its findings can
	// reach anybody.
	got, err := ct.pipelineGet(map[string]any{"name": "Research"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !strings.Contains(got, "stage plan") {
		t.Errorf("get should carry the finding:\n%s", got)
	}
	if full, err := ct.pipelineGet(map[string]any{"name": "Research", "full": true}); err != nil {
		t.Fatalf("get full: %v", err)
	} else if !strings.Contains(full, "stage plan") {
		t.Errorf("and so should the full read:\n%s", full)
	}
	if quiet, err := ct.pipelineGet(map[string]any{"name": "Simple"}); err != nil {
		t.Fatalf("get clean: %v", err)
	} else if strings.Contains(quiet, "Worth a look") {
		t.Errorf("nothing to say, so say nothing:\n%s", quiet)
	}
	lst, err := ct.pipelineList()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(lst, `"worth_a_look":1`) {
		t.Errorf("list should say which pipelines need a look:\n%s", lst)
	}
	if strings.Count(lst, "worth_a_look") != 1 {
		t.Errorf("and should stay quiet about the ones that do not:\n%s", lst)
	}
}
