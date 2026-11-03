package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A hand-off chip names the agent being asked, not the id the model passed;
// a call already naming it, another tool's agent argument, and an id that
// resolves to nothing are shown as sent.
func TestAHandOffChipNamesItsAgent(t *testing.T) {
	depStores(t, "u")
	udb := UserDB(orchestrateBaseDB, "u")
	music, err := saveAgent(udb, AgentRecord{Name: "Music Maker", Owner: "u", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	turn := &chatTurn{user: "u", udb: udb, agent: AgentRecord{ID: "wiwee", Name: "WiWee"}}
	for _, tool := range []string{"agents", "delegate"} {
		got := turn.chipArgs(tool, map[string]any{"action": "run", "agent": music.ID, "message": "a birthday song"})
		if got["agent"] != "Music Maker" || got["message"] != "a birthday song" {
			t.Errorf("%s chip args = %v", tool, got)
		}
	}
	sent := map[string]any{"agent": music.ID}
	turn.chipArgs("agents", sent)
	if sent["agent"] != music.ID {
		t.Error("the call's own args were rewritten; only the chip's copy may be")
	}
	for _, args := range []map[string]any{{"agent": "Music Maker"}, {"agent": "no-such-agent"}} {
		if got := turn.chipArgs("agents", args); got["agent"] != args["agent"] {
			t.Errorf("%v shown as %v", args, got)
		}
	}
	if got := turn.chipArgs("schedule", map[string]any{"agent": music.ID}); got["agent"] != music.ID {
		t.Error("another tool's agent argument was rewritten")
	}
}

// A file two steps both attach goes out once: each step runs on its own
// session, whose own check stops a repeat only within it, and a plan with a
// "make and attach" step and a "deliver" step sent one Word document twice.
func TestAFileTwoStepsAttachGoesOutOnce(t *testing.T) {
	buf := &syncBuf{}
	turn := &chatTurn{sse: &sseWriter{live: buf}}
	doc := FileAttachment{Name: "comparison.docx", MimeType: "application/zip", Data: "UEsDBA==", Size: 4}
	for _, f := range []FileAttachment{doc, doc, {Name: "notes.pdf", Data: "JVBERg==", Size: 4}} {
		step := &ToolSession{Username: "alice"}
		step.AppendFile(f)
		turn.flushNewAttachments(step, "m1", 0, 0, 0)
	}
	if n := strings.Count(buf.String(), `"kind":"file"`); n != 2 {
		t.Errorf("the document twice and another file once must send 2 files, sent %d", n)
	}
}
