package readoutput

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The overflow reader finishes reading a result that was too big to return.
// Before it, a capped reply from a sub-agent, a delegated run or an API call
// ended in "[truncated]" and the rest was simply gone: the agent could see
// something was missing and had no move but to ask for the work again.
func TestReadOutputFinishesATruncatedResult(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 400; i++ {
		b.WriteString("finding line ")
		b.WriteString(strings.Repeat("x", i%30))
		b.WriteString("\n")
	}
	full := strings.TrimSpace(b.String())

	// What an overflowing tool now does with its result.
	reply := SpillOutput(full, 2000, "read_output")
	if !strings.Contains(reply, "output_id=") {
		t.Fatalf("an overflowing reply must name the capture:\n%s", reply[len(reply)-200:])
	}
	id := reply[strings.Index(reply, `output_id="`)+11:]
	id = id[:strings.Index(id, `"`)]

	tool := new(ReadOutputTool)
	if !tool.IsFrameworkTool() {
		t.Error("the reader is round-shape plumbing, not a grantable capability")
	}

	// Reading on by offset.
	next := strings.Index(reply, "offset=") + len("offset=")
	off := 0
	for _, c := range reply[next:] {
		if c < '0' || c > '9' {
			break
		}
		off = off*10 + int(c-'0')
	}
	page, err := tool.Run(map[string]any{"output_id": id, "offset": float64(off), "max_chars": float64(2000)})
	if err != nil || strings.TrimSpace(page) == "" {
		t.Fatalf("paging on: %v %q", err, page)
	}

	// Finding one thing without paging to it.
	hits, err := tool.Run(map[string]any{"output_id": id, "grep": "finding line xxxxx$"})
	if err != nil || !strings.Contains(hits, "lines match") {
		t.Fatalf("grep: %v %q", err, hits)
	}

	// The two ways it can be called wrong, each answered usefully.
	if _, err := tool.Run(map[string]any{}); err == nil {
		t.Error("a missing output_id must say where the id comes from")
	}
	if _, err := tool.Run(map[string]any{"output_id": "nope"}); err == nil {
		t.Error("an unknown id must be an error the agent can act on")
	}
}
