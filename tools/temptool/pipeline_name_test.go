package temptool

// Pipeline mode created tools with no name check at all: any form, a
// built-in's name, a reserved per-turn tool's name. It now passes the same
// gate as the other modes, and an update of an existing pipeline tool still
// goes through (update re-runs the create path).

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAPipelineToolIsHeldToTheNameRules(t *testing.T) {
	RegisterReservedToolName("tncol_reserved_send")
	create := func(sess *ToolSession, name string) error {
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
