// "Off" has to survive being saved.
package core

import (
	"encoding/json"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// The bug this migration exists for: Think was a *bool, kvlite stores through
// gob, and gob encodes a FALSE POINTER as nothing at all. So an author picked
// "Off — this stage is a transform", saved, reopened, and found the control
// unset — with the run deliberating anyway if the caller's default said so.
// Nothing failed and nothing was reported; the setting simply was not there.
func TestOffSurvivesTheStore(t *testing.T) {
	udb := UserDB(&DBase{Store: kvlite.MemStore()}, "u")
	def := PipelineDef{Name: "research", Owner: "u", Stages: []PipelineStage{
		{Name: "gather", Kind: StageWorker, Prompt: "look", ThinkMode: "on"},
		{Name: "format", Kind: StageWorker, Prompt: "tidy it", ThinkMode: "off"},
		{Name: "reply", Kind: StageWorker, Prompt: "answer"},
	}}
	saved := SavePipelineDef(udb, def)
	back, ok := LoadPipelineDef(udb, "u", saved.ID)
	if !ok {
		t.Fatal("the pipeline did not come back")
	}
	for i, want := range []string{"on", "off", ""} {
		if got := StageThinkMode(back.Stages[i]); got != want {
			t.Errorf("stage %q: think came back %q, saved %q", back.Stages[i].Name, got, want)
		}
	}
	if StageThinks(back.Stages[1]) {
		t.Error("a stage set to off must not deliberate")
	}
}

// A pipeline written before the change keeps what it was given. gob matches on
// field NAME, so the legacy field is still there to be decoded — removing it
// would have failed the whole record, and renaming it would have dropped the
// "on" that older stages legitimately carry.
func TestALegacyStageKeepsItsSetting(t *testing.T) {
	yes := true
	legacy := PipelineStage{Name: "judge", Kind: StageWorker, Prompt: "weigh it", Think: &yes}
	if got := StageThinkMode(legacy); got != "on" {
		t.Errorf("a legacy true should read as on, got %q", got)
	}
	// And it migrates in place, so nothing downstream sees two fields.
	stages := []PipelineStage{legacy}
	normalizeStageThink(stages)
	if stages[0].ThinkMode != "on" || stages[0].Think != nil {
		t.Errorf("the legacy field should fold into the string one: %+v", stages[0])
	}
}

// An exported recipe outlives the field it was written against. "think": true
// is what every pipeline exported before today carries, and an import that
// rejected it would refuse a pipeline that is perfectly good.
func TestARecipeAcceptsBothShapesOfThink(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"name":"a","think":true}`, "on"},
		{`{"name":"a","think":false}`, "off"},
		{`{"name":"a","think":"on"}`, "on"},
		{`{"name":"a","think":"off"}`, "off"},
		{`{"name":"a"}`, ""},
		{`{"name":"a","think":"nonsense"}`, ""},
	} {
		var s PipelineStage
		if err := json.Unmarshal([]byte(tc.raw), &s); err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		if got := StageThinkMode(s); got != tc.want {
			t.Errorf("%s → %q, want %q", tc.raw, got, tc.want)
		}
		if s.Name != "a" {
			t.Errorf("%s: the rest of the stage should decode normally: %+v", tc.raw, s)
		}
	}
}

// The two authoring surfaces agree: a step and a stage say this the same way.
func TestAStageAndAStepSayThinkTheSameWay(t *testing.T) {
	for _, mode := range []string{"on", "off", ""} {
		stage := StageThinks(PipelineStage{ThinkMode: mode})
		phase := PhaseThink(MachinePhase{Think: mode}, false)
		if stage != phase {
			t.Errorf("think %q: stage says %v, step says %v", mode, stage, phase)
		}
	}
}
