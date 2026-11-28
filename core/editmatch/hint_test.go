package editmatch

import (
	"strings"
	"testing"
)

// A missed edit shows where the anchor's lines really are, so the agent
// copies from the file instead of guessing again: blocks it thought were
// neighbours, a changed middle, spacing alone, or text the file lacks.
func TestAMissedEditShowsWhereTheAnchorIs(t *testing.T) {
	file := strings.Join([]string{
		"function restore() {", // 1
		"  load();",            // 2
		"  renderRecent();",    // 3
		"}",                    // 4
		"document.addEventListener('click', e => {", // 5
		"  const c = e.target.closest('.chip');",    // 6
		"});",                                       // 7
	}, "\n")

	// Two blocks written as neighbours, in the wrong order (the run that
	// gave up).
	got := Hint(file, "document.addEventListener('click', e => {\n  const c = e.target.closest('.chip');\n});\nfunction restore() {")
	if !strings.Contains(got, "first line is at line 5") || !strings.Contains(got, "at line 1, before it") {
		t.Errorf("out of order:\n%s", got)
	}
	got = Hint(file, "function restore() {\n  load(); restore2();\n  renderRecent();")
	if !strings.Contains(got, "first line is at line 1 and its last at line 3") || !strings.Contains(got, "     2\t  load();") {
		t.Errorf("a changed middle:\n%s", got)
	}
	got = Hint(file, "function restore() {\n\tload();")
	if !strings.Contains(got, "apart from spaces") || !strings.Contains(got, "line 1") {
		t.Errorf("spacing alone:\n%s", got)
	}
	got = Hint(file, "const nothing = here;\nlet more = none;")
	if !strings.Contains(got, "Neither its first nor its last line") {
		t.Errorf("text the file lacks:\n%s", got)
	}
}
