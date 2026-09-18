package orchestrate

// A standing agent's report card is the only account of a run nobody watched.
// It said what the run concluded and never what it did, while the recurring
// fire's card — same card, same renderer, same kind of run — carried its tool
// trace all along.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestTheLedgerTraceBecomesCardChips(t *testing.T) {
	steps := []RunStep{
		{Name: "moltish/get_feed", Args: `{"limit":4}`, Result: "4 threads"},
		{Name: "moltish/reply_to_post", Args: `{"post_id":"abc","content":"hi"}`, Result: "201"},
		{Name: "moltish/reply_to_comment", Args: `{"post_id":"abc"}`, Err: `missing required arg "content"`},
	}
	got := persistedToolCallsFromSteps(steps)
	if len(got) != 3 {
		t.Fatalf("every call must survive the conversion: %d", len(got))
	}
	if got[0].Name != "moltish/get_feed" || got[0].Result != "4 threads" {
		t.Errorf("name and result must carry: %+v", got[0])
	}
	if got[1].Args["post_id"] != "abc" || got[1].Args["content"] != "hi" {
		t.Errorf("args must come back as a map the panel can render: %+v", got[1].Args)
	}
	// A failure is part of the account, not an omission from it.
	if got[2].Err == "" || got[2].Result != "" {
		t.Errorf("a failed call must carry its error and no result: %+v", got[2])
	}
	if len(persistedToolCallsFromSteps(nil)) != 0 {
		t.Error("no steps, no chips")
	}
}

// The NAME is the load-bearing half — it is what tells a reader a write ran
// rather than a read. An argument blob that will not parse must not take the
// whole chip with it.
func TestAnUnreadableArgBlobKeepsTheCall(t *testing.T) {
	got := persistedToolCallsFromSteps([]RunStep{
		{Name: "moltish/create_post", Args: "not json at all", Result: "201"},
	})
	if len(got) != 1 || got[0].Name != "moltish/create_post" {
		t.Fatalf("the call must survive its arguments: %+v", got)
	}
	if got[0].Args != nil {
		t.Errorf("unparseable args must be dropped, not guessed: %+v", got[0].Args)
	}
	if got[0].Result != "201" {
		t.Error("the outcome must still carry")
	}
}

// The two directions must agree, or a run inspected in the ledger and the same
// run read on the thread describe different work.
func TestTheTwoDirectionsRoundTrip(t *testing.T) {
	original := []PersistedToolCall{
		{Name: "a/read", Args: map[string]any{"n": "1"}, Result: "ok"},
		{Name: "a/write", Args: map[string]any{"body": "text"}, Err: "boom"},
	}
	back := persistedToolCallsFromSteps(runStepsFromToolCalls(original))
	if len(back) != len(original) {
		t.Fatalf("round trip lost calls: %d -> %d", len(original), len(back))
	}
	for i := range original {
		if back[i].Name != original[i].Name || back[i].Result != original[i].Result || back[i].Err != original[i].Err {
			t.Errorf("call %d changed: %+v -> %+v", i, original[i], back[i])
		}
		a, _ := json.Marshal(original[i].Args)
		b, _ := json.Marshal(back[i].Args)
		if string(a) != string(b) {
			t.Errorf("call %d args changed: %s -> %s", i, a, b)
		}
	}
}

// The plumbing half. RecordRun moves Raw AND Steps to encrypted side tables and
// returns a record carrying neither; the reporter restored Raw from the start
// and Steps not at all, so the card writer had nothing to convert however
// willing it was.
func TestTheReportRecordKeepsItsTrace(t *testing.T) {
	src, err := os.ReadFile("../../core/standing_agent.go")
	if err != nil {
		t.Skip("standing_agent.go unavailable")
	}
	body := string(src)
	if !strings.Contains(body, "forReport.Steps = fullSteps") {
		t.Error("the reporter gets no tool trace, so a standing card can never show one")
	}
	if !strings.Contains(body, "forReport.Raw = fullOutput") {
		t.Error("the output restore must stay — this is the pair, not a replacement")
	}
	// Captured BEFORE the record is stored, or the restore hands back what
	// RecordRun already blanked. Measured against the RecordRun that FOLLOWS
	// the capture — an earlier one runs in the no-runner branch, and matching
	// that made this assertion fail on correct code.
	capture := strings.Index(body, "fullOutput, fullSteps := rec.Raw, rec.Steps")
	if capture < 0 {
		t.Fatal("the trace is never captured")
	}
	after := body[capture:]
	record := strings.Index(after, "rec = RecordRun(db, rec)")
	restore := strings.Index(after, "forReport.Steps = fullSteps")
	if record < 0 {
		t.Fatal("nothing stores the record after the capture")
	}
	if restore < 0 || restore < record {
		t.Error("the restore must come after the store, or it restores nothing")
	}
}

// The card itself.
func TestTheStandingCardCarriesItsTrace(t *testing.T) {
	src, err := os.ReadFile("standing_runner.go")
	if err != nil {
		t.Skip("standing_runner.go unavailable")
	}
	if !strings.Contains(string(src), "ToolCalls: persistedToolCallsFromSteps(rec.Steps)") {
		t.Error("the standing report card writes no tool calls; the panel renders what is not there as nothing")
	}
}
