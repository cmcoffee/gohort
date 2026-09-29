package orchestrate

import (
	"testing"

	"github.com/cmcoffee/gohort/core/replyguard"
)

// A flag is filed against the stored reply it names (newest match first, or
// one that starts the same way), with the message the person sent before it
// and the model that wrote it.
func TestAFlagFindsTheReplyItIsAbout(t *testing.T) {
	sess := ChatSession{Messages: []ChatMessage{
		{Role: "user", Content: "summarise the news"},
		{Role: "assistant", Content: "Here is the summary.", Usage: &ChatMessageUsage{Model: "old-model"}},
		{Role: "user", Content: "and again"},
		{Role: "assistant", Content: "Here is the summary.", Usage: &ChatMessageUsage{Model: "gemini-2.5-flash"}},
		{Role: "user", Content: "one more thing"},
		{Role: "assistant", Content: "A long answer that the bubble shows with a trailing space"},
	}}
	m, asked, ok := findFlaggedReply(sess, "  Here is the summary. ")
	if !ok || asked != "and again" || m.Usage.Model != "gemini-2.5-flash" {
		t.Errorf("the newest matching reply, with what came before it: %q %q %v", asked, m.Content, ok)
	}
	if _, asked, ok := findFlaggedReply(sess, "A long answer that the bubble shows with a trailing space, then edited"); ok {
		t.Errorf("a longer text is not a match: %q", asked)
	}
	if _, _, ok := findFlaggedReply(sess, "never said"); ok {
		t.Error("a reply that is not in the session is not matched")
	}
}

// The early-answer check is a reply guard too: shadow counts it and holds
// nothing back.
func TestTheEarlyAnswerCheckIsAReplyGuard(t *testing.T) {
	if !replyguard.Known(earlyAnswerGuard) {
		t.Fatal("early-answer is not registered")
	}
	if !earlyAnswerActs("any-model", "text") {
		t.Error("on by default")
	}
}
