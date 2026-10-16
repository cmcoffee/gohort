package temptool

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func carriedPipelineSession(t *testing.T) *ToolSession {
	t.Helper()
	return &ToolSession{
		Username: "alice", ChatSessionID: "s1",
		WorkspaceDir: t.TempDir(), DB: &DBase{Store: kvlite.MemStore()},
	}
}

func onePipeline(name, prompt string) PipelineDef {
	return PipelineDef{ID: "p-1", Owner: "alice", Name: name, Stages: []PipelineStage{{Name: "write", Kind: StageWorker, Prompt: prompt}}}
}

// A tool made from a saved pipeline keeps its own copy, runs it with the
// caller's input, and folds the original in once the tool is saved.
func TestAToolMadeFromAPipelineCarriesAndRunsIt(t *testing.T) {
	sess := carriedPipelineSession(t)
	folded := false
	sess.PipelineSource = func(ref string) (PipelineDef, func() string, error) {
		if ref != "song maker" {
			t.Fatalf("ref = %q", ref)
		}
		return onePipeline("song maker", "write a song about {input}"), func() string { folded = true; return "FOLDED." }, nil
	}

	res, err := createGrouped(map[string]any{"action": "create", "name": "make_song", "description": "Write a song.",
		"mode": "pipeline", "from_pipeline": "song maker"}, sess)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !folded || !strings.Contains(res, "FOLDED.") {
		t.Errorf("the original was not folded in, or the result does not say so: %s", res)
	}
	rec, ok := loadExistingToolRecord(sess, "make_song")
	if !ok {
		t.Fatal("tool missing after create")
	}
	def, carried, err := carriedPipelineOf(&rec)
	if !carried || err != nil || def.Name != "song maker" || def.ID != "" || def.Owner != "" {
		t.Fatalf("the tool's copy: %+v carried=%v err=%v", def, carried, err)
	}
	if _, ok := rec.Params["input"]; !ok {
		t.Errorf("a tool with no params should take the pipeline's one input: %+v", rec.Params)
	}

	var ran PipelineDef
	var gotInput string
	sess.PipelineRunner = func(_ context.Context, d PipelineDef, input string) (string, error) {
		ran, gotInput = d, input
		return "la la la", nil
	}
	out, err := dispatchTempToolUncached(sess, &rec, map[string]any{"input": "the sea"})
	if err != nil || out != "la la la" || ran.Name != "song maker" || gotInput != "the sea" {
		t.Fatalf("dispatch: out=%q err=%v ran=%q input=%q", out, err, ran.Name, gotInput)
	}
}

// An edit that leaves the pipeline alone keeps it, and pipeline_def replaces it.
func TestACarriedPipelineSurvivesAnEditAndCanBeReplaced(t *testing.T) {
	sess := carriedPipelineSession(t)
	if _, err := createGrouped(map[string]any{"action": "create", "name": "make_song", "description": "Write a song.",
		"mode": "pipeline", "pipeline_def": onePipeline("song maker", "verse one")}, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := updateGrouped(map[string]any{"name": "make_song", "description": "Write a better song."}, sess); err != nil {
		t.Fatalf("description edit: %v", err)
	}
	rec, _ := loadExistingToolRecord(sess, "make_song")
	if def, ok, _ := carriedPipelineOf(&rec); !ok || def.Stages[0].Prompt != "verse one" || rec.Description != "Write a better song." {
		t.Fatalf("a description edit lost the pipeline: %+v / %q", def, rec.Description)
	}
	edited := onePipeline("song maker", "verse two")
	if _, err := updateGrouped(map[string]any{"name": "make_song", "pipeline_def": edited}, sess); err != nil {
		t.Fatalf("pipeline edit: %v", err)
	}
	rec, _ = loadExistingToolRecord(sess, "make_song")
	if def, _, _ := carriedPipelineOf(&rec); def.Stages[0].Prompt != "verse two" {
		t.Fatalf("pipeline_def did not replace the copy: %+v", def)
	}
	if _, err := createGrouped(map[string]any{"action": "create", "name": "bad", "description": "x", "mode": "pipeline",
		"pipeline_def": PipelineDef{Name: "empty"}}, sess); err == nil {
		t.Error("a pipeline with no stages was accepted")
	}
}
