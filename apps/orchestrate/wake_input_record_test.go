package orchestrate

import (
	"os"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
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

// An event card is capped where a person's message never is.
//
// A watch monitor hands over a diff and a payload the agent reads once and
// answers. The answer — stored in full right beneath it — is what anybody needs
// later; the payload's value decays the moment it has been read, and its cost
// does not, because a persistent thread replays every card into every prompt
// after it.
func TestAnEventCardIsCappedAndSaysSo(t *testing.T) {
	cap := TuneInt("tune_event_card_chars")
	if cap <= 0 {
		t.Fatal("the cap must have a default, or nothing bounds a firing monitor")
	}
	payload := "[EVENT — monitor \"molty\" fired]\n" + strings.Repeat("{\"comments\":[…]}", 400)
	got := storedRunInput(AgentSyncRun{
		InputReportFrom: "molty", InputReportKind: cortexKindMonitor,
	}, payload, time.Now())

	if len([]rune(got.Content)) >= len([]rune(payload)) {
		t.Errorf("the card kept the whole payload: %d runes", len([]rune(got.Content)))
	}
	// The head survives — a card whose first line is gone names nothing.
	if !strings.HasPrefix(got.Content, "[EVENT — monitor \"molty\" fired]") {
		t.Errorf("the card lost its opening: %q", got.Content[:60])
	}
	// And it SAYS it was trimmed. A card that just stops reads as a payload the
	// agent might still be able to use, and a model will reason about the
	// fragment rather than about the reply underneath.
	if !strings.Contains(got.Content, "went to the agent when it fired") {
		t.Errorf("a trimmed card must say what happened to the rest: %q", got.Content)
	}
	// A small event is untouched — the cap is for the ones that need it.
	small := storedRunInput(AgentSyncRun{InputReportFrom: "molty"}, "nothing much changed", time.Now())
	if small.Content != "nothing much changed" {
		t.Errorf("a small event was rewritten: %q", small.Content)
	}
	// A person's message is never capped, however long: it is the thing they
	// said, and the agent answering it is not a reason to keep less of it.
	long := strings.Repeat("x", cap*3)
	if got := storedRunInput(AgentSyncRun{MessageSender: "Dana"}, long, time.Now()); got.Content != long {
		t.Error("a user's message must be stored whole")
	}
}

// A wake that carries a readable card stores the card, and the model still
// gets the prompt: the two readers get the text written for them.
func TestAnEventCardPrefersItsReadableText(t *testing.T) {
	now := time.Now()
	prompt := "[EVENT — monitor \"molty\" fired]\nWatch monitor \"molty\" detected a change.\n\nWhat changed:\n+ {\"author\":{\"name\":\"x\"}}\n\nCurrent output:\nHTTP 200 OK\n{…}"
	got := storedRunInput(AgentSyncRun{
		Message: prompt, InputReportFrom: "molty", InputReportKind: cortexKindMonitor,
		InputCardText: "Watch \"molty\" changed.\n\nNew (1):\n• x — \"hi\"",
	}, prompt, now)
	if got.Content != "Watch \"molty\" changed.\n\nNew (1):\n• x — \"hi\"" {
		t.Errorf("the card must be the readable text, got %q", got.Content)
	}
	if got.ReportFrom != "molty" || got.Role == "user" {
		t.Errorf("it is still a monitor card: %+v", got)
	}
	// No readable text: the message is the card, exactly as before.
	plain := storedRunInput(AgentSyncRun{Message: prompt, InputReportFrom: "molty"}, prompt, now)
	if plain.Content != prompt {
		t.Errorf("without a card text the message is stored, got %q", plain.Content)
	}
}
