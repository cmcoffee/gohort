package orchestrate

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

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

// A call says when it began, so a page that rejoins mid-call times it from
// then; and a call that waits on the person under its own card (ask_user) says
// so, so the live line beside the dots does not count the wait as it running.
func TestAToolCallCarriesItsStartAndOwnCard(t *testing.T) {
	buf := &bytes.Buffer{}
	turn := &chatTurn{sse: &sseWriter{live: buf}}

	before := time.Now().UnixMilli()
	turn.emitToolCall("m1", "web_search", map[string]any{"q": "x"}, "")
	var ev map[string]any
	if err := json.Unmarshal(sseData(t, buf.String()), &ev); err != nil {
		t.Fatalf("decoding %q: %v", buf.String(), err)
	}
	if ms, _ := ev["started_ms"].(float64); int64(ms) < before || int64(ms) > time.Now().UnixMilli() {
		t.Errorf("started_ms = %v, want the time of the call", ev["started_ms"])
	}
	if _, ok := ev["own_card"]; ok {
		t.Error("a plain call claimed a card of its own")
	}

	buf.Reset()
	turn.emitToolCall("m1", "ask_user", map[string]any{"question": "which one?"}, "")
	if !strings.Contains(buf.String(), `"own_card":true`) {
		t.Errorf("ask_user should be kept off the live line: %s", buf.String())
	}
}

// sseData is the JSON of the one SSE event in s.
func sseData(t *testing.T, s string) []byte {
	t.Helper()
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "data: ") {
			return []byte(strings.TrimPrefix(line, "data: "))
		}
	}
	t.Fatalf("no data line in %q", s)
	return nil
}
