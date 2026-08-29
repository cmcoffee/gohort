package orchestrate

import "testing"

// TestExportTextAppliesTheDeliveryScrub pins the gap that made a bad export
// readable as evidence.
//
// The transcript was written straight from stored content. The model emits
// em-dashes despite the prompt rule; the save path and the browser renderer
// both strip them, and the export did neither, so a pasted transcript showed
// a character the user was never shown. The literal reply below is the one
// that surfaced it.
func TestExportTextAppliesTheDeliveryScrub(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"the observed reply",
			"Ha! That's a classic tech dad joke",
			"Ha! That's a tech dad joke"},
		{"em-dash from the same session",
			"If you need anything before bed, I'm around—otherwise, hope you sleep well!",
			"If you need anything before bed, I'm around, otherwise, hope you sleep well!"},
		{"framework markers never reach a transcript",
			"visible<gohort-meta>internal note</gohort-meta>",
			"visible"},
		{"both rules at once",
			"That's a classic slip — easy to make",
			"That's a slip, easy to make"},
		{"ordinary text is untouched",
			"nothing here needs changing",
			"nothing here needs changing"},
		{"literal sense survives",
			"he restored a classic car",
			"he restored a classic car"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := exportText(tc.in); got != tc.want {
				t.Errorf("exportText(%q)\n  got  %q\n  want %q", tc.in, got, tc.want)
			}
		})
	}
}
