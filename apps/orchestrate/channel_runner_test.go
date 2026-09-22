package orchestrate

import (
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"

	. "github.com/cmcoffee/gohort/core"
)

// The cortex card is the standing thread's only record of what a channel turn
// DID, and it used to say so as prose in the body — "↳ ran: shell(uptime)".
// That is the one shape neither reader can use: the panel has a renderer for a
// persisted trace (the expandable tool-runs section every other card has) and
// toLLMMessages rebuilds one into call-and-result protocol. A line of text is
// invisible to both.

func cortexCard(t *testing.T, db Database, agentID string) ChatMessage {
	t.Helper()
	sess, ok := loadChatSession(db, agentID, cortexSessionID(agentID))
	if !ok || len(sess.Messages) == 0 {
		t.Fatal("no cortex card was written")
	}
	return sess.Messages[len(sess.Messages)-1]
}

func cortexAgent(t *testing.T) (Database, string) {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	ag := AgentRecord{ID: "a1", Name: "Wren", Owner: "alice", Cortex: true,
		OrchestratorPrompt: "you are Wren"}
	if _, err := saveAgent(db, ag); err != nil {
		t.Fatal(err)
	}
	return db, ag.ID
}

// What a turn ran reaches the card as structure, so the panel can render it the
// way it renders every other turn's tools.
func TestAChannelCardCarriesItsTraceStructurally(t *testing.T) {
	db, agentID := cortexAgent(t)
	appendCortexObs(db, agentID, "iPhone", cortexKindMessage, "what's happening?\n↳ replied: a lot",
		PersistedToolCall{Name: "web_search", Args: map[string]any{"q": "world news"}, Result: "3 results"},
		PersistedToolCall{Name: "arm_monitor", Args: map[string]any{"name": "inbox"}, Err: "already armed"},
	)
	card := cortexCard(t, db, agentID)
	if len(card.ToolCalls) != 2 {
		t.Fatalf("the trace must ride the card: %+v", card.ToolCalls)
	}
	if card.ToolCalls[0].Name != "web_search" || card.ToolCalls[0].Args["q"] != "world news" {
		t.Errorf("name and args must survive: %+v", card.ToolCalls[0])
	}
	if card.ToolCalls[1].Err != "already armed" {
		t.Errorf("a failure is part of the record: %+v", card.ToolCalls[1])
	}
	// ...and NOT as prose in the body, or the card says it twice.
	for _, gone := range []string{"↳ ran:", "web_search(", "arm_monitor("} {
		if strings.Contains(card.Content, gone) {
			t.Errorf("the body still formats the trace as text (%q):\n%s", gone, card.Content)
		}
	}
	// The body keeps what only it can say.
	if !strings.Contains(card.Content, "what's happening?") || !strings.Contains(card.Content, "↳ replied:") {
		t.Errorf("the inbound and the reply must stay in the body:\n%s", card.Content)
	}
}

// A silent turn is the one most worth recording — an action with no reply
// attached to explain it — and the trace is the whole of what it has to say.
func TestASilentTurnStillRecordsWhatItRan(t *testing.T) {
	db, agentID := cortexAgent(t)
	appendCortexObs(db, agentID, "iPhone", cortexKindMessage,
		"what's happening?\n↳ stayed silent (nothing sent to the channel)",
		PersistedToolCall{Name: "web_search", Args: map[string]any{"q": "world news"}, Result: "3 results"},
	)
	card := cortexCard(t, db, agentID)
	if len(card.ToolCalls) != 1 {
		t.Fatalf("a silent turn's actions must still be recorded: %+v", card.ToolCalls)
	}
	if !strings.Contains(card.Content, "stayed silent") {
		t.Errorf("and that nothing went out:\n%s", card.Content)
	}
}

// A turn that ran nothing carries no trace — the feed is pointers to things
// that happened, not an empty section on every card.
func TestATurnThatRanNothingCarriesNoTrace(t *testing.T) {
	db, agentID := cortexAgent(t)
	appendCortexObs(db, agentID, "iPhone", cortexKindMessage, "just saying hi")
	if card := cortexCard(t, db, agentID); len(card.ToolCalls) != 0 {
		t.Errorf("no tools should mean no trace: %+v", card.ToolCalls)
	}
}

// The standing thread is read back whole every turn and is kept lean on
// purpose, so the copy that lands here is bounded — unlike the per-contact
// thread, which keeps the full version.
func TestTheCortexCopyOfATraceIsBounded(t *testing.T) {
	long := strings.Repeat("x", 4000)
	var many []PersistedToolCall
	for i := 0; i < 30; i++ {
		many = append(many, PersistedToolCall{Name: "read", Result: long})
	}
	got := cortexToolTrace(many)
	if len(got) >= len(many) {
		t.Errorf("an unbounded trace crowds out the awareness the thread exists for: %d", len(got))
	}
	for _, c := range got {
		if len([]rune(c.Result)) > 260 {
			t.Errorf("result not bounded: %d chars", len([]rune(c.Result)))
		}
	}
	// Names and arguments survive intact — they are what says WHAT was done.
	kept := cortexToolTrace([]PersistedToolCall{
		{Name: "shell", Args: map[string]any{"command": "uptime"}, Result: "ok"},
	})
	if kept[0].Name != "shell" || kept[0].Args["command"] != "uptime" || kept[0].Result != "ok" {
		t.Errorf("a short call must pass through untouched: %+v", kept[0])
	}
	if cortexToolTrace(nil) != nil {
		t.Error("no calls, no trace")
	}
}

// Observed live on a group chat: an agent answered an inbound with
//
//	↳ replied: Craig Coffee - Group Chat (iMessage): lol
//	Wiwee, if you win the lottery I get the money right ?
//
// — the framework's own marker, and the inbound echoed back, delivered to the
// contact. Every card in that agent's standing thread is shaped "<what came
// in>\n↳ replied: <what I said>", so the model had read that shape hundreds of
// times; nothing scrubbed it on the way out, because StripMetaTags removes
// <gohort-meta> and attach markers and nothing else.
//
// The report-origin marker on the very same card was already wrapped for
// exactly this reason. These were not.
func TestObservationMarkersAreFencedForTheModel(t *testing.T) {
	card := ChatMessage{
		Role:       "assistant",
		ReportFrom: "Craig - Group Chat (iMessage)",
		Content:    "if you win the lottery I get the money right ?\n↳ replied: not a chance",
	}
	got := llmHistoryContent(card)
	if !strings.Contains(got, "<gohort-meta>↳ replied: not a chance</gohort-meta>") {
		t.Errorf("the reply marker reaches the model bare, so an echo of it reaches the contact:\n%s", got)
	}
	// The model must still SEE what it said — that is why the line is there.
	if !strings.Contains(got, "not a chance") {
		t.Error("fencing must not hide what the agent already told this contact")
	}
	// And the inbound itself is untouched.
	if !strings.Contains(got, "if you win the lottery I get the money right ?") {
		t.Error("the inbound text must survive")
	}
	// An echo of the fenced line is scrubbed at delivery, which is the point.
	if out := StripMetaTags("↳ replied: not a chance"); out != "↳ replied: not a chance" {
		t.Error("precondition: a BARE marker is not scrubbed — that is the bug")
	}
	if out := StripMetaTags("<gohort-meta>↳ replied: not a chance</gohort-meta>"); strings.Contains(out, "↳") {
		t.Errorf("a fenced marker must not survive delivery: %q", out)
	}
}

func TestSilenceMarkerIsFencedToo(t *testing.T) {
	got := llmHistoryContent(ChatMessage{
		Role: "assistant", ReportFrom: "iPhone",
		Content: "what's happening?\n↳ stayed silent (nothing sent to the channel)",
	})
	if !strings.Contains(got, "<gohort-meta>↳ stayed silent") {
		t.Errorf("the silence marker is bare:\n%s", got)
	}
}

// An ordinary turn carries no origin marker and must not be rewritten.
func TestAnOrdinaryMessageIsNotFenced(t *testing.T) {
	got := llmHistoryContent(ChatMessage{Role: "assistant", Content: "just a reply"})
	if strings.Contains(got, "gohort-meta") {
		t.Errorf("a plain message gained a marker: %q", got)
	}
	// A body with no markers at all is returned untouched, arrow or not.
	if got := fenceObservationMarkers("no markers here"); got != "no markers here" {
		t.Errorf("untouched body changed: %q", got)
	}
	if got := fenceObservationMarkers("↳ Researcher: a mirrored diag line"); strings.Contains(got, "gohort-meta") {
		t.Errorf("only the observation markers are fenced, not every arrow: %q", got)
	}
}
