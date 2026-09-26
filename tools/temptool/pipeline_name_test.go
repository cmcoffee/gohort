package temptool

// Pipeline mode created tools with no name check at all: any form, a
// built-in's name, a reserved per-turn tool's name. It now passes the same
// gate as the other modes, and an update of an existing pipeline tool still
// goes through (update re-runs the create path).

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAPipelineToolIsHeldToTheNameRules(t *testing.T) {
	RegisterReservedToolName("tncol_reserved_send")
	create := func(sess *ToolSession, name string) error {
		// The inner tool has to exist: a pipeline naming a tool it cannot
		// call is refused at save.
		if !sess.HasTempTool("inner") {
			sess.AppendTempTool(&TempTool{Name: "inner", CommandTemplate: "echo hi"})
		}
		_, err := createGrouped(map[string]any{
			"mode": "pipeline", "name": name, "description": "d",
			"pipeline_prompt": "do it", "pipeline_tools": []any{"inner"},
		}, sess)
		return err
	}
	for _, bad := range []string{"Bad Name", "tncol_reserved_send"} {
		if err := create(newTestSession(), bad); err == nil {
			t.Errorf("a pipeline tool was created as %q", bad)
		}
	}
	sess := newTestSession()
	if err := create(sess, "tncol_pipe"); err != nil {
		t.Fatalf("a valid pipeline tool was refused: %v", err)
	}
	// An edit keeps what it did not name and applies what it did. The
	// rebuild used to drop every pipeline field, so no pipeline tool could be
	// updated at all.
	if _, err := updateGrouped(map[string]any{"name": "tncol_pipe", "description": "changed", "pipeline_prompt": "do it better"}, sess); err != nil {
		t.Fatalf("updating an existing pipeline tool was refused: %v", err)
	}
	got := sess.LookupTempTool("tncol_pipe")
	if got == nil || got.Description != "changed" || got.PipelinePrompt != "do it better" ||
		len(got.PipelineTools) != 1 || got.PipelineTools[0] != "inner" {
		t.Errorf("the update lost or ignored pipeline fields: %+v", got)
	}
}

// A pipeline step reaches the user's own custom tools, and a pipeline asks
// for everything its steps can do. Observed: a two-step pipeline failed on
// step 1 with "generate_music not found" while generate_music sat loaded.
func TestAPipelineStepReachesCustomTools(t *testing.T) {
	sess := newTestSession()
	gen := &TempTool{Name: "gen_song", Mode: TempToolModeAPI, Credential: "not_registered", CommandTemplate: "https://x.example/gen", Method: "POST"}
	ext := &TempTool{Name: "extract_song", CommandTemplate: "python3 x.py", ScriptBody: "print(1)"}
	for _, tt := range []*TempTool{gen, ext} {
		if err := sess.AppendTempTool(tt); err != nil {
			t.Fatal(err)
		}
	}
	def, custom, err := pipelineStepTool(sess, "gen_song")
	if err != nil || custom == nil || custom.Name != "gen_song" || def.Handler == nil {
		t.Fatalf("a custom tool should resolve as a step: custom=%v err=%v", custom, err)
	}
	if _, _, err := pipelineStepTool(sess, "no_such_tool"); err == nil {
		t.Error("a name nothing answers to cannot be a step")
	}

	pipe := &TempTool{Name: "song_pipe", Mode: TempToolModePipeline, PipelineTools: []string{"gen_song", "extract_song"}}
	caps, confirm := pipelineInnerProfile(sess, pipe, 0)
	if !capsSubset([]Capability{CapNetwork, CapExecute}, caps) {
		t.Errorf("the pipeline must claim what its steps can do, got %v", caps)
	}
	if !confirm {
		t.Error("a step that asks before each call makes the pipeline ask too")
	}
	pdef := agentToolFromTemp(sess, pipe)
	if !capsSubset([]Capability{CapNetwork}, pdef.Tool.Caps) || !pdef.NeedsConfirm {
		t.Errorf("the pipeline's catalog entry carries its steps' reach: caps=%v confirm=%v", pdef.Tool.Caps, pdef.NeedsConfirm)
	}

	_, err = createGrouped(map[string]any{
		"mode": "pipeline", "name": "broken_pipe", "description": "d",
		"pipeline_tools": []any{"gen_song", "ghost_tool"},
		"pipeline_steps": []any{map[string]any{"tool": "gen_song", "args": map[string]any{}}},
	}, sess)
	if err == nil || !strings.Contains(err.Error(), "ghost_tool") {
		t.Errorf("a pipeline naming a tool it cannot call is refused at save: %v", err)
	}
}

// update keeps a tool's mode, and says so instead of rebuilding the old mode
// under a warning about something else.
func TestUpdateRefusesAModeChange(t *testing.T) {
	sess := newTestSession()
	sess.AppendTempTool(&TempTool{Name: "inner", CommandTemplate: "echo hi"})
	if _, err := createGrouped(map[string]any{"mode": "pipeline", "name": "a_pipe", "description": "d", "pipeline_prompt": "go", "pipeline_tools": []any{"inner"}}, sess); err != nil {
		t.Fatal(err)
	}
	_, err := updateGrouped(map[string]any{"name": "a_pipe", "mode": "shell", "script_body": "print(1)"}, sess)
	if err == nil || !strings.Contains(err.Error(), "cannot change a tool's mode") || !strings.Contains(err.Error(), "Nothing was changed") {
		t.Errorf("a mode change must be refused plainly: %v", err)
	}
	if _, err := updateGrouped(map[string]any{"name": "a_pipe", "mode": "pipeline", "description": "same mode"}, sess); err != nil {
		t.Errorf("naming the same mode is fine: %v", err)
	}
}
