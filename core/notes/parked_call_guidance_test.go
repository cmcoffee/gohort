// A working note reading "pending task: get_top_stories with category=all"
// survived into every later session's prompt. It carried the call and its
// arguments; the tool's schema did not travel with it. So the model read an
// outstanding task it had the arguments for and no way to invoke, and wrote
// the call as Python in the sandbox instead — twice, in two sessions, in two
// syntaxes. The failed call had written its own re-trigger.
//
// The read-side block has to say this, because that is where the note is read
// back. Pinned so a later trim of the intro doesn't quietly drop it.
package notes

import (
	"strings"
	"testing"
)

func TestTheNotesBlockForbidsParkingAToolCall(t *testing.T) {
	block := RenderOperatingNotesBlock(OperatingNotes{Text: "mid-draft on section 3"})

	for _, want := range []string{
		"GOAL",               // what a note is FOR
		"cannot call a tool", // what a note is not
	} {
		if !strings.Contains(block, want) {
			t.Errorf("the notes intro must carry %q:\n%s", want, block)
		}
	}
	// The reason has to survive too — "don't do this" without "here is what
	// goes wrong" is the kind of line that reads as boilerplate and gets
	// trimmed by the next person shortening the prompt.
	if !strings.Contains(block, "outlives the tool") {
		t.Errorf("the intro must say WHY a parked call goes bad:\n%s", block)
	}
}

// Empty notes render nothing at all — the guidance must not smuggle a block
// into the prompt for an agent that has never written a note.
func TestNoNotesStillRendersNothing(t *testing.T) {
	if got := RenderOperatingNotesBlock(OperatingNotes{}); got != "" {
		t.Errorf("empty notes must render empty, got:\n%s", got)
	}
	if got := RenderOperatingNotesBlock(OperatingNotes{Text: "   "}); got != "" {
		t.Errorf("blank notes must render empty, got:\n%s", got)
	}
}
