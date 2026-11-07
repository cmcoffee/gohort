package orchestrate

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The double the user sees live and not after a reload: the same answer
// rendered twice on screen, once in the saved transcript.
//
// The live dedup and the persist dedup look at different sets. Persist
// (appendMidTurnBubbles) weighs EVERY captured bubble against the final reply.
// Live (emitCapturedAsBubble) weighs the captured reply against
// lastFinalizedText alone — and the lead-in finalize never wrote to it. So a
// model that put its whole answer in a tool round and then let the loop hand
// the same text back as the captured reply got two bubbles live and one on
// reload.

func emitFixture() (*planRun, *syncBuf) {
	buf := &syncBuf{}
	turn := &chatTurn{user: "u", sse: &sseWriter{live: buf}}
	return &planRun{t: turn, resp: &Response{}, telem: newTurnTelemetry()}, buf
}

// countFrames counts how many SSE frames carry a recognisable slice of text.
func countFrames(out, needle string) int {
	return strings.Count(out, needle)
}

// The reported bug. A tool round carried the answer, the final round came back
// empty, and the loop's captured reply repeated it word for word.
func TestALeadInBubbleDedupsTheReplyThatRepeatsIt(t *testing.T) {
	pr, buf := emitFixture()
	answer := "The standup moved to 9:45 and the room is now Ashby."

	pr.streamHandler(answer)
	pr.onStepHandler(StepInfo{Round: 1, Content: answer, ToolCalls: []ToolCall{{Name: "calendar_list"}}})
	// Final round: tool-free, no text of its own.
	pr.onStepHandler(StepInfo{Round: 2, Done: true})
	pr.emitCapturedAsBubble(answer)

	if n := countFrames(buf.String(), "standup moved to 9:45"); n != 1 {
		t.Fatalf("the answer must render once, got %d:\n%s", n, buf.String())
	}
}

// The hazard the loosening protects. A short lead-in shares its whole text
// with the opening of a much longer reply; suppressing on that prefix leaves
// the user with a sentence of preamble and no answer.
func TestAShortLeadInCannotSwallowTheAnswerThatFollowsIt(t *testing.T) {
	pr, buf := emitFixture()
	lead := "Let me pull the numbers."
	answer := lead + " " + strings.Repeat("Revenue rose in every region. ", 40)

	pr.streamHandler(lead)
	pr.onStepHandler(StepInfo{Round: 1, Content: lead, ToolCalls: []ToolCall{{Name: "sql_query"}}})
	pr.emitCapturedAsBubble(answer)

	if !strings.Contains(buf.String(), "Revenue rose in every region") {
		t.Fatalf("a reply that says far more than the lead-in must still render:\n%s", buf.String())
	}
}

// And the case the dedup was built for stays deduped: the same analysis coming
// back with a rewritten ending is one bubble, not two.
func TestARevisedConclusionIsStillOneBubble(t *testing.T) {
	pr, buf := emitFixture()
	body := strings.Repeat("The contract renews in March and the price is fixed. ", 30)

	pr.streamHandler(body + "So we should renew.")
	pr.onStepHandler(StepInfo{Round: 1, Done: true, Content: body})
	pr.emitCapturedAsBubble(body + "So we should renegotiate first.")

	if n := countFrames(buf.String(), "So we should reneg"); n != 0 {
		t.Fatalf("a revised ending on the same analysis must not open a second bubble:\n%s", buf.String())
	}
}

// The length guard that clears long prose written beside a tool call, and the
// two things that were wrong with it: it fired on the one round shape where
// long prose is correct, and when its bet failed it lost the text in silence.

// A round whose only tools SHOW something is the delivery, not a step before
// the answer. The prose beside it is the explanation that goes with the thing
// shown, and there is no later round that would repeat it.
func TestPresentationRoundsKeepTheirProse(t *testing.T) {
	if !presentationOnlyRound([]ToolCall{{Name: "show_link"}}) {
		t.Error("show_link alone is a presentation round")
	}
	if !presentationOnlyRound([]ToolCall{{Name: "show_html"}, {Name: "show_link"}}) {
		t.Error("two presentation tools are still a presentation round")
	}
	// Mixed: real work happened alongside, so the guard's reasoning applies.
	if presentationOnlyRound([]ToolCall{{Name: "show_link"}, {Name: "web_search"}}) {
		t.Error("a round that also did work is not presentation-only")
	}
	if presentationOnlyRound([]ToolCall{{Name: "web_search"}}) {
		t.Error("an ordinary tool round is not presentation-only")
	}
	// No tools at all is the final round, handled elsewhere entirely.
	if presentationOnlyRound(nil) {
		t.Error("a tool-free round is not a presentation round")
	}
}

func withheldFixture(held, shown string) (*planRun, *syncBuf) {
	buf := &syncBuf{}
	turn := &chatTurn{user: "u", sse: &sseWriter{live: buf}}
	return &planRun{
		t:                 turn,
		resp:              &Response{},
		withheldLeadIn:    held,
		lastFinalizedText: shown,
	}, buf
}

// The live failure. A turn created a Jira issue, linked it, wrote 1109
// characters about what it had done alongside show_link, and finished with a
// 30-character "done". The work happened and the account of it did not
// survive, with no log line and no diagnostic saying so.
func TestWithheldProseComesBackWhenTheReplyDoesNot(t *testing.T) {
	held := strings.Repeat("what I did and why. ", 55) // ~1100 chars
	pr, buf := withheldFixture(held, "Done, link above.")

	pr.restoreWithheldLeadIn("", "Done, link above.")

	if !strings.Contains(buf.String(), "what I did and why") {
		t.Fatalf("the held account must be restored when the reply did not carry it:\n%s", buf.String())
	}
	if pr.withheldLeadIn != "" {
		t.Error("the held text must be released once it is restored")
	}
}

// And the double-emit the guard exists to prevent stays prevented: a model
// that genuinely restated its answer clears the half-length bar easily.
func TestARestatedAnswerIsNotDoubled(t *testing.T) {
	held := strings.Repeat("what I did and why. ", 55)
	restated := strings.Repeat("what I did, said again. ", 50)
	pr, buf := withheldFixture(held, restated)

	pr.restoreWithheldLeadIn("", restated)

	if strings.Contains(buf.String(), "what I did and why") {
		t.Fatalf("a restated answer must not be doubled:\n%s", buf.String())
	}
}

// A turn that ends by ASKING is not one whose answer went missing, and
// dropping a paragraph in underneath the question talks over it.
func TestAQuestionTurnIsLeftAlone(t *testing.T) {
	held := strings.Repeat("what I did and why. ", 55)
	pr, buf := withheldFixture(held, "")

	pr.restoreWithheldLeadIn("Which project should it go in?", "")

	if strings.Contains(buf.String(), "what I did and why") {
		t.Fatalf("a question turn must not have prose appended under it:\n%s", buf.String())
	}
}

// Held text goes back where it was written. The live export: an approval
// card came between the held round and the final reply, the held bubble was
// left open and blank, the final reply streamed into it ABOVE the card, and
// the restore added a new bubble UNDER everything, so the screen showed the
// reply first and the saved transcript showed it second. Holding now closes the
// bubble and frees the next round to open its own; restoring refills that
// bubble, with no new one and no stats footer.
func TestWithheldProseIsRestoredWhereItWasWritten(t *testing.T) {
	held := strings.Repeat("what I did and why. ", 55)
	pr, buf := withheldFixture("", "")
	pr.streamMsgID = "orch-held"

	pr.withholdLeadIn("orch-held", held)
	got := buf.String()
	if !strings.Contains(got, `"kind":"chunk_replace"`) || !strings.Contains(got, `"kind":"message_done"`) {
		t.Fatalf("holding should blank and close the bubble:\n%s", got)
	}
	if pr.streamMsgID != "" || pr.withheldID != "orch-held" || pr.withheldLeadIn != held {
		t.Fatalf("the next round must open its own bubble, and the held text keep its place: stream=%q id=%q", pr.streamMsgID, pr.withheldID)
	}

	before := len(buf.String())
	pr.restoreWithheldLeadIn("", "It's in.")
	restored := buf.String()[before:]
	if !strings.Contains(restored, `"id":"orch-held"`) || !strings.Contains(restored, "what I did and why") {
		t.Fatalf("the held text should refill its own bubble:\n%s", restored)
	}
	if strings.Contains(restored, `"kind":"message"`) || strings.Contains(restored, `"kind":"stats"`) {
		t.Errorf("no new bubble and no stats footer on a restore:\n%s", restored)
	}
	if pr.withheldID != "" {
		t.Error("the held bubble must be released once restored")
	}
}

// Nothing shown at all is the worst case and the clearest restore.
func TestNothingShownRestores(t *testing.T) {
	held := strings.Repeat("what I did and why. ", 55)
	pr, buf := withheldFixture(held, "")

	pr.restoreWithheldLeadIn("", "")

	if !strings.Contains(buf.String(), "what I did and why") {
		t.Fatalf("a turn that showed nothing must get the held text back:\n%s", buf.String())
	}
}

// A reply written beside a note the agent files away is the reply: saving
// what the user said changes nothing in it. Observed live, twice in six
// minutes: 1442 characters beside remember, held back, and a final "Noted."
// that left the restore to put them back; then 838 beside remember, held,
// and the final round's rewrite shown in their place.
func TestAReplyBesideASideNoteIsTheReply(t *testing.T) {
	for _, c := range []struct {
		calls []string
		want  bool
	}{
		{[]string{"remember"}, true},
		{[]string{"remember", "link_entities", "note_to_cortex"}, true},
		{[]string{"remember", "web_search"}, false}, // real work beside it
		{[]string{"propose_rule"}, false},           // its outcome is the owner's answer
		{[]string{"forget"}, false},                 // may find nothing to forget
		{nil, false},
	} {
		var calls []ToolCall
		for _, n := range c.calls {
			calls = append(calls, ToolCall{Name: n})
		}
		if got := sideNoteRound(calls); got != c.want {
			t.Errorf("%v: side notes = %v", c.calls, got)
		}
	}

	long := strings.Repeat("Here is what that means for the rollout. ", 20)
	saved := []ToolResult{{Content: "Saved 120 chars under topic \"x\" in Memory."}}
	remember := []ToolCall{{Name: "remember"}}
	pr, _ := emitFixture()
	if !pr.finishAfterSideNotes(long, remember, saved) {
		t.Error("a reply beside a clean note should end the turn")
	}
	if pr.finishAfterSideNotes("Let me note that.", remember, saved) {
		t.Error("a lead-in is not a reply")
	}
	if pr.finishAfterSideNotes(long, remember, []ToolResult{{Content: "Saved.\n\n" + findingConflictMarker + " this may contradict..."}}) {
		t.Error("a note that may contradict an earlier one is the user's to hear about")
	}
	if pr.finishAfterSideNotes(long, remember, []ToolResult{{Content: "failed", IsError: true}}) {
		t.Error("a note that failed is the user's to hear about")
	}
	if pr.finishAfterSideNotes(long, []ToolCall{{Name: "web_search"}}, saved) {
		t.Error("a round that did work is not ended on its prose")
	}
	pr.holdStream = true
	if pr.finishAfterSideNotes(long, remember, saved) {
		t.Error("an agent whose output a guardrail judges keeps its final round")
	}
}

// On screen: the reply beside a side note is shown, not held back, and an
// ordinary tool round's long prose is still held.
func TestAReplyBesideASideNoteIsShown(t *testing.T) {
	long := strings.Repeat("Here is what that means for the rollout. ", 20)
	pr, buf := emitFixture()
	pr.streamHandler(long)
	pr.onStepHandler(StepInfo{Round: 1, Content: long, ToolCalls: []ToolCall{{Name: "remember"}}})
	if pr.withheldLeadIn != "" || !strings.Contains(buf.String(), "what that means for the rollout") {
		t.Errorf("the reply beside a side note was held back:\n%s", buf.String())
	}

	pr, _ = emitFixture()
	pr.streamHandler(long)
	pr.onStepHandler(StepInfo{Round: 1, Content: long, ToolCalls: []ToolCall{{Name: "web_search"}}})
	if pr.withheldLeadIn == "" {
		t.Error("long prose beside real work should still be held")
	}

	pr, _ = emitFixture()
	pr.holdStream = true
	pr.streamHandler(long)
	pr.onStepHandler(StepInfo{Round: 1, Content: long, ToolCalls: []ToolCall{{Name: "remember"}}})
	if pr.withheldLeadIn == "" {
		t.Error("an agent whose output a guardrail judges should keep the hold")
	}
}

// The chat turn's loop is the one that asks: without the wiring, the reply is
// shown and the model still gets a round to say "Noted." under it.
func TestTheChatLoopAsksWhetherSideNotesEndTheTurn(t *testing.T) {
	src, err := os.ReadFile("runner.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "FinishAfterTools: pr.finishAfterSideNotes,") {
		t.Error("the chat turn's loop no longer ends on a reply beside side notes")
	}
}
