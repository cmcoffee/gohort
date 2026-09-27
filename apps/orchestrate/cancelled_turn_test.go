package orchestrate

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A cancelled turn keeps what it did: its tool calls land on the thread with a
// note that the reply did not finish. A cancel used to return with nothing
// saved, and the next turn could neither see nor explain the tool edits it had
// made.
func TestACancelledTurnKeepsItsToolCalls(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	sess := ChatSession{ID: "s1", AgentID: "seed-builder", Messages: []ChatMessage{{Role: "user", Content: "make sure the pipeline works"}}}
	turn := &chatTurn{}
	turn.recordToolCall(toolCallRecord{Name: "tool_def", Args: map[string]any{"action": "update", "name": "get_meme"}, Result: "Updated get_meme"})
	persistIncompleteTurnTrace(&sess, udb, turn, "it was cancelled")

	got, ok := loadChatSession(udb, "seed-builder", "s1")
	if !ok || len(got.Messages) != 2 {
		t.Fatalf("the turn's work should be saved after the request: %+v", got.Messages)
	}
	last := got.Messages[1]
	if last.Role != "assistant" || len(last.ToolCalls) != 1 || last.ToolCalls[0].Name != "tool_def" || !strings.Contains(last.Content, "it was cancelled") {
		t.Errorf("want the tool call with a didn't-finish note: %+v", last)
	}
}

// Every cancel exit of the send handler saves the turn first. There were
// three returns on a cancel and none saved.
func TestEveryCancelExitSavesTheTurn(t *testing.T) {
	src, err := os.ReadFile("runner_http.go")
	if err != nil {
		t.Skip("source unavailable")
	}
	body := string(src)
	if n := strings.Count(body, `"text": "cancelled"`); n != 1 {
		t.Errorf("the cancelled notice should be sent from one place, the helper that saves first; found %d", n)
	}
	if !strings.Contains(body, `persistIncompleteTurnTrace(&sess, udb, turn, "it was cancelled")`) {
		t.Error("the cancel helper should save the turn")
	}
	if strings.Count(body, "case <-ctx.Done():\n\t\t\tcancelled()")+strings.Count(body, "case <-ctx.Done():\n\t\t\t\t\tcancelled()") != 2 {
		t.Error("both step loops should leave through the saving helper on a cancel")
	}
}
