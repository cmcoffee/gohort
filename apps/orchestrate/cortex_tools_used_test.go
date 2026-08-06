package orchestrate

// A channel turn's record in the standing thread used to be the inbound text
// and the reply, and nothing else. Anything the agent DID in between — a
// search, an image edit, a message to somebody else, a monitor armed — left no
// trace in the thread that is supposed to hold the agent's awareness of its
// own actions.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestToolNamesComeFromTheTranscript(t *testing.T) {
	transcript := []Message{
		{Role: "user", Content: "make x sit in y"},
		{Role: "assistant", ToolCalls: []ToolCall{{Name: "image"}, {Name: "web_search"}}},
		{Role: "assistant", ToolCalls: []ToolCall{{Name: "image"}}}, // repeat
		{Role: "assistant", ToolCalls: []ToolCall{{Name: "send_message"}}},
		{Role: "assistant", Content: "done"},
	}
	got := toolNamesFromTranscript(transcript)
	want := []string{"image", "web_search", "send_message"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: got %q, want %q (first-use order, deduped)", i, got[i], want[i])
		}
	}
	if n := len(toolNamesFromTranscript(nil)); n != 0 {
		t.Errorf("a transcript with no calls yielded %d name(s)", n)
	}
	// A turn that only talked must add nothing to the thread.
	if toolsUsedNote(toolNamesFromTranscript([]Message{{Role: "assistant", Content: "hi"}})) != "" {
		t.Error("a tool-free turn produced a 'used:' line")
	}
}

// The standing thread is kept by rolling summary, so the note has to stay one
// line however busy the turn was.
func TestToolsUsedNoteIsBounded(t *testing.T) {
	many := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}
	note := toolsUsedNote(many)
	if strings.Count(note, "\n") != 0 {
		t.Errorf("note spans lines: %q", note)
	}
	if !strings.Contains(note, "and 3 more") {
		t.Errorf("note does not summarize the overflow: %q", note)
	}
	if strings.Contains(note, "k") && !strings.Contains(note, "and 3 more") {
		t.Error("listed every tool despite the cap")
	}
	short := toolsUsedNote([]string{"image", "web_search"})
	if short != "↳ used: image, web_search" {
		t.Errorf("short list rendered as %q", short)
	}
}
