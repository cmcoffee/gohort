package orchestrate

// The length guard that clears long prose written beside a tool call, and the
// two things that were wrong with it: it fired on the one round shape where
// long prose is correct, and when its bet failed it lost the text in silence.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

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

// Nothing shown at all is the worst case and the clearest restore.
func TestNothingShownRestores(t *testing.T) {
	held := strings.Repeat("what I did and why. ", 55)
	pr, buf := withheldFixture(held, "")

	pr.restoreWithheldLeadIn("", "")

	if !strings.Contains(buf.String(), "what I did and why") {
		t.Fatalf("a turn that showed nothing must get the held text back:\n%s", buf.String())
	}
}
