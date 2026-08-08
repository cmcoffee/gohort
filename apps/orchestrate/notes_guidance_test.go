// The write side of the parked-call rule. The read-side block (core/notes)
// tells the model how to interpret a note it finds; this tells it what not to
// write in the first place, which is where the "pending task: get_top_stories
// with category=all" note came from.
package orchestrate

import (
	"strings"
	"testing"
)

func TestUpdateNotesRefusesToBeAToolQueue(t *testing.T) {
	desc := (&chatTurn{}).updateNotesToolDef().Tool.Description

	for _, want := range []string{
		"NEVER park a tool call",
		"pending task", // the exact shape observed, so the model recognizes it
		"Record the GOAL",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("update_notes must warn against %q:\n%s", want, desc)
		}
	}
	// store_fact stays the named alternative for durable rules — the warning
	// must not leave the model with nowhere to put anything.
	if !strings.Contains(desc, "store_fact") {
		t.Errorf("the description must still route durable rules to store_fact:\n%s", desc)
	}
}
