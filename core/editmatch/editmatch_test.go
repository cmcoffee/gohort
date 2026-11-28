package editmatch

import "testing"

// An anchor that differs from the file only in what the model cannot see or
// does not reproduce lands anyway, and says how loosely it was read: CRLF
// line endings (which the new text takes on), curly quotes and dashes, spaces
// at line ends. Leading indentation is never loosened.
func TestALooseAnchorLandsAndSaysHow(t *testing.T) {
	cases := []struct {
		name, file, old, new, want string
		tier                       Tier
	}{
		{"exact", "a\nb\nc\n", "b\n", "B\n", "a\nB\nc\n", Exact},
		{"crlf", "a\r\nb\r\nc\r\n", "a\nb\n", "x\ny\n", "x\r\ny\r\nc\r\n", LineEndings},
		{"curly quotes in the file", "say(\u201Chi\u201D) \u2013 ok\n", `say("hi") - ok`, `say("bye")`, "say(\"bye\")\n", Typography},
		{"curly quotes in the anchor", "it's here\n", "it\u2019s here", "it is here", "it is here\n", Typography},
		{"trailing spaces", "x := 1   \ny := 2\t\nz := 3\n", "x := 1\ny := 2\n", "x, y := 1, 2\n", "x, y := 1, 2\nz := 3\n", TrailingSpace},
		{"trailing spaces with crlf", "x := 1  \r\ny := 2\r\n", "x := 1\ny := 2", "x := 9\ny := 8", "x := 9\r\ny := 8\r\n", TrailingSpace},
	}
	for _, tc := range cases {
		m := Find(tc.file, tc.old, tc.new)
		if len(m.Spans) != 1 {
			t.Errorf("%s: %d matches", tc.name, len(m.Spans))
			continue
		}
		if m.Tier != tc.tier {
			t.Errorf("%s: tier %d, want %d", tc.name, m.Tier, tc.tier)
		}
		if got := m.Apply(tc.file); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
		if (m.Tier == Exact) != (m.Tier.Note() == "") {
			t.Errorf("%s: note %q", tc.name, m.Tier.Note())
		}
	}

	for _, miss := range []struct{ file, old string }{
		{"def f():\n    return 1\n", "def f():\n  return 1\n"}, // indentation is code
		{"a\nb\n", "b\n\n"}, // no break after the last line
		{"abc\n", "zzz"},
	} {
		if m := Find(miss.file, miss.old, "x"); len(m.Spans) != 0 {
			t.Errorf("%q matched in %q", miss.old, miss.file)
		}
	}
}
