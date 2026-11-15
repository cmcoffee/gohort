package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A standing-activity line is the first line of what came in, cut short. Asked
// about one, an agent answered from the headline because the block forbade any
// lookup at all. It may still not act on a line by itself, but asked about an
// item it reads the source before saying more than the line does.
func TestStandingActivityIsAHeadlineToReadPast(t *testing.T) {
	udb := UserDB(&DBase{Store: kvlite.MemStore()}, "u-feed")
	card := ChatMessage{Role: "assistant", ReportFrom: "news digest", ReportKind: cortexKindScheduled,
		Content: "A person in the news was summoned over an allegation.\nThe rest of the report."}
	if _, err := saveChatSession(udb, ChatSession{ID: cortexSessionID("a1"), AgentID: "a1", Messages: []ChatMessage{card}}); err != nil {
		t.Fatal(err)
	}
	block := cortexContextBlock(udb, "a1")
	if !strings.Contains(block, "news digest: A person in the news was summoned") || strings.Contains(block, "The rest of the report") {
		t.Fatalf("the block should carry the first line only:\n%s", block)
	}
	for _, want := range []string{"nothing here is a reason to call a tool", "not the whole report", "read the source first", "list_runs"} {
		if !strings.Contains(block, want) {
			t.Errorf("the block lost %q", want)
		}
	}
	if strings.Contains(block, "do NOT launch tool calls, lookups") {
		t.Error("the block still forbids looking anything up, even when the user asks")
	}
}
