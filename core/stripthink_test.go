package core

import "testing"

// TestStripThinkTags covers every leak shape the guard must catch, plus the
// no-op case. The bool must be true whenever a delimiter was present so
// user-facing callers can warn on it.
func TestStripThinkTags(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		want      string
		wantFound bool
	}{
		{"clean passthrough", "just the answer", "just the answer", false},
		{"full block", "<think>reasoning here</think>the answer", "the answer", true},
		{"block with trailing whitespace", "<think>r</think>\n\nanswer", "answer", true},
		{"leading orphan closer", "</think>the answer", "the answer", true},
		{"mid orphan closer, no opener", "reasoning text</think>the answer", "the answer", true},
		// The actual phantom symptom: the answer duplicated around a stray
		// closer (no-think degeneration). Must collapse to ONE clean copy.
		{"duplicated answer around stray closer", "Sounds good, see you at 6.</think>Sounds good, see you at 6.", "Sounds good, see you at 6.", true},
		// Close cousin: trailing stray closer with nothing after — must keep
		// the message, NOT wipe it.
		{"trailing stray closer, nothing after", "On my way.</think>", "On my way.", true},
		{"unclosed opener at start", "<think>reasoning never closed", "", true},
		{"unclosed opener mid content", "partial answer <think>then it spilled", "partial answer", true},
		{"multiple blocks", "<think>a</think>mid<think>b</think>end", "midend", true},
		{"empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found := StripThinkTags(tc.in)
			if got != tc.want || found != tc.wantFound {
				t.Fatalf("StripThinkTags(%q) = (%q, %v), want (%q, %v)", tc.in, got, found, tc.want, tc.wantFound)
			}
		})
	}
}
