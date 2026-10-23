package core

import (
	"errors"
	"strings"
	"testing"
)

// The estimate counts four bytes a token, and the provider's count ran a
// quarter higher on code and logs: a prompt put at 165k was over 208k.
// Every reply's real count corrects it, per configured window, rising fast,
// falling slowly, never below 1 (so a count that leaves cached tokens out
// cannot loosen compaction), never above 2; compaction's window shrinks by
// it. A refusal's stated size counts too.
func TestTheEstimateIsCorrectedByWhatTheProviderCounts(t *testing.T) {
	const w = 204801 // a window no other test uses: the ratios are shared
	if tokenRatio(w) != 1 || calibratedWindow(100000, w) != 100000 {
		t.Fatal("nothing measured is no correction")
	}
	noteTokenRatio(w, 165510, 208401)
	r := tokenRatio(w)
	if r < 1.1 || r > 1.26 {
		t.Fatalf("one measurement moves it most of the way up: %.3f", r)
	}
	if got := calibratedWindow(204800, w); got >= 204800*10/11 {
		t.Errorf("the window shrinks by it: %d", got)
	}
	noteTokenRatio(w, 165510, 208401)
	up := tokenRatio(w)
	noteTokenRatio(w, 100000, 100000)
	if down := tokenRatio(w); down >= up || down < up-0.05 {
		t.Errorf("it falls slowly: %.3f -> %.3f", up, down)
	}
	noteTokenRatio(w, 100000, 10) // a count that leaves cache out
	if tokenRatio(w) < 1 {
		t.Error("never below 1")
	}
	const big = 204802
	for i := 0; i < 20; i++ {
		noteTokenRatio(big, 10000, 90000)
	}
	if tokenRatio(big) > 2 {
		t.Error("never above 2")
	}
	const small = 204803
	noteTokenRatio(small, 500, 2000)
	if tokenRatio(small) != 1 {
		t.Error("a small prompt says too little to learn from")
	}
	err := errors.New("vllm api error (400): This model's maximum context length is 208400 tokens. However, you requested 0 output tokens and your prompt contains at least 208401 input tokens, for a total of at least 208401 tokens.")
	if n := promptTokensInRefusal(err); n != 208401 {
		t.Errorf("the refusal's prompt size: %d", n)
	}
}

// One round that read three whole files put the next past the window before
// it ran. Each result is held to a share of the window as it arrives:
// kept whole in the paged store and read on with read_output when the loop
// has it, its end kept with a note when it does not. The same cap holds on
// every round, because a later turn rebuilds its history with each result
// whole, and it holds still: a capped result is not cut again.
func TestAResultIsHeldToItsShareOfTheWindow(t *testing.T) {
	file := strings.Repeat("line of a large file\n", 12000) // ~250 KB
	capBytes := ingestCapBytes(160000)                      // ~96 KB
	if capBytes < 90000 || capBytes > 100000 {
		t.Fatalf("15%% of the window: %d", capBytes)
	}
	if ingestCapBytes(10000) != ingestFloorBytes || ingestCapBytes(0) != 0 {
		t.Error("floored, and off when the window is unknown")
	}

	paged := []ToolResult{{ID: "a", Content: file}, {ID: "b", Content: "small"}}
	if n := capIngestedResults(paged, capBytes, true); n != 1 {
		t.Fatalf("capped %d", n)
	}
	if !strings.Contains(paged[0].Content, "output_id=") || len(paged[0].Content) > capBytes+capNoteSlack || paged[1].Content != "small" {
		t.Errorf("a large result arrives as a window with an id to read on: %d bytes", len(paged[0].Content))
	}
	plain := []ToolResult{{ID: "a", Content: file}}
	capIngestedResults(plain, capBytes, false)
	if !strings.Contains(plain[0].Content, "truncated as it arrived") || !strings.HasSuffix(plain[0].Content, "line of a large file\n") {
		t.Errorf("without read_output its end is kept with a note: %.100s", plain[0].Content)
	}

	stored := []Message{{Role: "user", ToolResults: []ToolResult{{ID: "a", Content: file}}}, {Role: "user", ToolResults: paged}}
	if n := capStoredResults(stored, capBytes); n != 1 {
		t.Fatalf("a whole stored result is cut, a capped one left: cut %d", n)
	}
	once := stored[0].ToolResults[0].Content
	if capStoredResults(stored, capBytes) != 0 || stored[0].ToolResults[0].Content != once {
		t.Error("cutting is stable: a cut result is not cut again")
	}

	// compactHistory holds it on every round, even under budget.
	msgs := []Message{{Role: "user", Content: "read these"}, {Role: "user", ToolResults: []ToolResult{{ID: "x", Content: file}}}}
	compactHistory(msgs, "", 1000, 160000, false)
	if len(msgs[1].ToolResults[0].Content) > capBytes+capNoteSlack {
		t.Errorf("a round starts with every result within its share: %d bytes", len(msgs[1].ToolResults[0].Content))
	}
}
