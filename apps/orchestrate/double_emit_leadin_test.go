package orchestrate

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

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

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
