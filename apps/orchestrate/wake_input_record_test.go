package orchestrate

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A monitor firing is not something the owner said.
//
// The wake prompt — "[EVENT — monitor "x" fired]", the diff payload, and the
// closing "React in this thread…" instruction — was stored as a plain user
// message, so the owner's cortex showed the whole thing in a bubble attributed
// to them. It read back to the model that way too: on the next turn it is the
// owner's words, not an event, with nothing saying where it came from.
func TestAnEventInputIsStoredAsACardNotAsTheUsersMessage(t *testing.T) {
	now := time.Now()
	got := storedRunInput(AgentSyncRun{
		Message:         "[EVENT — monitor \"molty\" fired]\n…",
		InputReportFrom: "molty",
		InputReportKind: cortexKindMonitor,
	}, "[EVENT — monitor \"molty\" fired]\n…", now)

	if got.Role == "user" {
		t.Error("an event stored as a user turn is attributed to the owner")
	}
	if got.ReportFrom != "molty" || got.ReportKind != cortexKindMonitor {
		t.Errorf("the card must name its producer and kind: %+v", got)
	}
	// The card shape is what earns the fenced origin marker on the way back
	// into the model — the same treatment a standing report gets.
	marked := llmHistoryContent(got)
	if !strings.Contains(marked, "molty") || !strings.HasPrefix(marked, "<gohort-meta>") {
		t.Errorf("a stored event must reach the model saying where it came from: %q", marked)
	}
}

// Every other caller is untouched: a channel inbound and a dispatch really are
// somebody's message, and they must stay user turns carrying their sender.
func TestAnOrdinaryRunInputStaysAUserTurn(t *testing.T) {
	got := storedRunInput(AgentSyncRun{MessageSender: "Dana"}, "I'm making pasta", time.Now())
	if got.Role != "user" {
		t.Errorf("an ordinary input must stay a user turn, got %q", got.Role)
	}
	if got.Sender != "Dana" {
		t.Errorf("the sender must survive: %+v", got)
	}
	if got.ReportFrom != "" {
		t.Error("an ordinary input is not a report card")
	}
}

// The two halves of one monitor fire must leave the same kind of trace. The
// direct path has always recorded a card; the channel wake handed the identical
// event to the agent as a user turn, and which one you got depended on a notify
// mode nobody reads as an attribution setting.
func TestBothMonitorPathsLeaveTheSameKindOfTrace(t *testing.T) {
	raw, err := os.ReadFile("operator_wake.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	if !strings.Contains(src, "InputReportFrom: monitorName") {
		t.Error("the channel wake must mark its input as a monitor event")
	}
	if !strings.Contains(src, "InputReportKind: cortexKindMonitor") {
		t.Error("and carry the kind the direct path records, so both render alike")
	}
	// The direct path's card is the shape being matched; if it goes, this test
	// is comparing against nothing.
	if !strings.Contains(src, "ReportKind: cortexKindMonitor") {
		t.Error("the direct path's monitor card is the reference shape")
	}
}
