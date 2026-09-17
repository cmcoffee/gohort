package readoutput

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// keptID spills a text and digs the handle out of the trailer, the same way
// the agent reads it.
func keptID(t *testing.T) string {
	t.Helper()
	reply := SpillOutput(strings.Repeat("a line that is long enough to matter\n", 700), 2000, "read_output")
	const marker = `output_id="`
	i := strings.Index(reply, marker)
	if i < 0 {
		t.Fatalf("no output_id in the spilled reply:\n%s", reply)
	}
	rest := reply[i+len(marker):]
	return rest[:strings.Index(rest, `"`)]
}

func TestReleaseOutputRefusesWhatItCannotRelease(t *testing.T) {
	tool := new(ReleaseOutputTool)
	if _, err := tool.Run(map[string]any{}); err == nil {
		t.Fatal("a call with no output_id was accepted")
	}
	// An id nobody kept releases nothing. Reporting success there would
	// leave the agent believing it made room it never made.
	if _, err := tool.Run(map[string]any{"output_id": "not-a-real-id"}); err == nil {
		t.Fatal("an unknown output_id was accepted")
	}
}

func TestReleaseOutputAcceptsAKeptCapture(t *testing.T) {
	id := keptID(t)
	out, err := new(ReleaseOutputTool).Run(map[string]any{"output_id": id})
	if err != nil {
		t.Fatalf("a kept capture was refused: %v", err)
	}
	if !strings.Contains(out, id) {
		t.Fatalf("the reply does not name the id:\n%s", out)
	}
	if !strings.Contains(out, "read_output") {
		t.Fatalf("the reply does not say the release is reversible:\n%s", out)
	}
	// The reply must NOT print the handle in the form a spilled trailer
	// uses, or the loop would release the confirmation along with the
	// result it is confirming.
	if strings.Contains(out, `output_id="`) {
		t.Fatalf("the reply carries a releasable reference to itself:\n%s", out)
	}
}

// A partial call still does the part it can, and says what it could not.
func TestReleaseOutputMixedIDs(t *testing.T) {
	id := keptID(t)
	out, err := new(ReleaseOutputTool).Run(map[string]any{"output_id": id + ", ghost-id"})
	if err != nil {
		t.Fatalf("a call naming one good id was refused: %v", err)
	}
	if !strings.Contains(out, "ghost-id") || !strings.Contains(out, "Not released") {
		t.Fatalf("the reply hides the id it could not release:\n%s", out)
	}
}

// Both halves of the paging pair must be reachable as tool defs — the
// trailers name them, and a named tool nobody holds is an invitation to
// improvise.
func TestPagingToolsAreRegistered(t *testing.T) {
	defs := OutputPagingToolDefs()
	if len(defs) != 2 {
		t.Fatalf("OutputPagingToolDefs returned %d defs, want read_output and release_output", len(defs))
	}
	want := map[string]bool{"read_output": true, "release_output": true}
	for _, d := range defs {
		if !want[d.Tool.Name] {
			t.Fatalf("unexpected tool in the paging pair: %q", d.Tool.Name)
		}
		delete(want, d.Tool.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing from the paging pair: %v", want)
	}
}
