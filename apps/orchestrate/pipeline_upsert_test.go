package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

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
}
