package textutil

import "testing"

// The contract text and the parser must agree: the instruction handed to
// the model names the same fence SplitDraftReply looks for. They used to
// live in two packages and this is what kept them honest.
func TestDraftReplyContractNamesTheFence(t *testing.T) {
	if !contains(DraftReplyContract, DraftFence) {
		t.Error("the response contract doesn't mention the fence the parser expects")
	}
}

func TestSplitDraftReply(t *testing.T) {
	cases := []struct{ name, in, reply, value string }{
		{"reply plus draft", "Tightened it.\n\n```draft\nBody.\n```", "Tightened it.", "Body."},
		{"answer with no draft", "That section is for constraints.", "That section is for constraints.", ""},
		{"draft with no preamble", "```draft\n- always cite\n```", "", "- always cite"},
		{
			// A document that teaches by example contains its own fences.
			// Closing on the FIRST ``` would truncate it.
			"nested fences survive",
			"Added an example.\n\n```draft\nRun:\n\n```bash\nls\n```\n\nDone.\n```",
			"Added an example.",
			"Run:\n\n```bash\nls\n```\n\nDone.",
		},
		{"unterminated fence keeps body", "Here.\n\n```draft\nBody.", "Here.", "Body."},
		{"empty", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reply, value := SplitDraftReply(c.in)
			if reply != c.reply || value != c.value {
				t.Errorf("SplitDraftReply(%q) = (%q, %q), want (%q, %q)", c.in, reply, value, c.reply, c.value)
			}
		})
	}
}

func TestStripLeadingHeading(t *testing.T) {
	cases := []struct{ name, in, section, want string }{
		{"own heading dropped", "## Rules\n- cite", "Rules", "- cite"},
		{"any level", "#### Rules\n- x", "Rules", "- x"},
		{"leading blanks", "\n\n## Rules\n- x", "Rules", "- x"},
		{"case insensitive", "## rules\n- x", "Rules", "- x"},
		{"no heading untouched", "- cite", "Rules", "- cite"},
		{"other heading kept", "## Hard\n- x", "Rules", "## Hard\n- x"},
		{"heading only", "## Rules", "Rules", ""},
		{"empty", "", "Rules", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := StripLeadingHeading(c.in, c.section); got != c.want {
				t.Errorf("StripLeadingHeading(%q, %q) = %q, want %q", c.in, c.section, got, c.want)
			}
		})
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && indexOf(hay, needle) >= 0
}
func indexOf(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
