package core

import "testing"

// A tool-results message must directly follow an assistant tool-call message
// (or another tool-result message). Breaking it is a hard 400 from the
// provider's chat template, not a soft degradation — and the natural place to
// inject a mid-round correction sits inside exactly that gap.
func TestFirstToolOrderViolation(t *testing.T) {
	assistantCall := Message{Role: "assistant", ToolCalls: []ToolCall{{Name: "fetch_url"}}}
	results := Message{Role: "user", ToolResults: []ToolResult{{Content: "ok"}}}
	note := Message{Role: "user", Content: "a mid-round correction"}

	cases := []struct {
		name    string
		history []Message
		want    int
	}{
		{"well formed", []Message{{Role: "user"}, assistantCall, results}, -1},
		{"chained results follow results", []Message{assistantCall, results, results}, -1},
		{"no tool traffic at all", []Message{{Role: "user"}, {Role: "assistant"}}, -1},
		// The live bug: the failure-shape guard appended its note between the
		// assistant's tool calls and the results.
		{"correction wedged before results", []Message{assistantCall, note, results}, 2},
		// The fix: same note, after the results.
		{"correction after results", []Message{assistantCall, results, note}, -1},
		{"results with no preceding call", []Message{{Role: "user"}, results}, 1},
		{"results first", []Message{results}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FirstToolOrderViolation(tc.history); got != tc.want {
				t.Errorf("FirstToolOrderViolation = %d, want %d", got, tc.want)
			}
		})
	}
}
