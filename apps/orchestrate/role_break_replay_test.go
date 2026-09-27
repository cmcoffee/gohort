package orchestrate

import (
	"strings"
	"testing"
)

// A reply taken back for carrying on the user's message is replayed as a bare
// note, never its words: kept in view, a model took its invented request ("take
// that joke and generate an image for it") as the user's and worked on it. The
// phrase matched is the one the agent loop strikes with.
func TestARoleBreakRetractionIsNotReplayed(t *testing.T) {
	invented := ". I'm also wondering if you are able to take that joke and generate an image for it?"
	got := llmHistoryContent(ChatMessage{Role: "assistant", Content: invented,
		Retracted: "Retracted: carried on the message as if written by its sender, instead of answering it."})
	if strings.Contains(got, "generate an image") || !strings.Contains(got, "withdrawn") {
		t.Errorf("the invented request must not reach the model: %q", got)
	}
	other := llmHistoryContent(ChatMessage{Role: "assistant", Content: "I emailed it.", Retracted: "Retracted: no email was sent."})
	if !strings.Contains(other, "I emailed it.") {
		t.Errorf("any other retraction keeps its words, marked: %q", other)
	}
}
