package displayname

import (
	"strings"
	"testing"
)

func TestCleanKeepsOneLineOfPlainText(t *testing.T) {
	for in, want := range map[string]string{
		"  Pat   Smith ": "Pat Smith",
		"Dr. Lee":        "Dr. Lee",
		"Ana María":      "Ana María",
	} {
		if got, err := Clean(in); err != nil || got != want {
			t.Errorf("Clean(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "   ", "Pat\u0000", "Pat\u0007Smith", strings.Repeat("x", MaxLen+1)} {
		if got, err := Clean(bad); err == nil {
			t.Errorf("Clean(%q) = %q, want refused", bad, got)
		}
	}
	// A line break inside is space, not two lines.
	for _, in := range []string{"Pat\nSmith", "Pat\u2028Smith", "Pat\r\n Smith"} {
		if got, _ := Clean(in); got != "Pat Smith" {
			t.Errorf("Clean(%q) reads %q", in, got)
		}
	}
	if got, err := Clean(strings.Repeat("é", MaxLen)); err != nil || got == "" {
		t.Errorf("a name of %d characters (more bytes) was refused: %v", MaxLen, err)
	}
}

func TestOthersSeeTheUsernameBesideTheName(t *testing.T) {
	if got := Shown("Pat Smith", "pat@example.test"); got != "Pat Smith (pat@example.test)" {
		t.Errorf("Shown = %q", got)
	}
	for _, name := range []string{"", "  ", "PAT@example.test"} {
		if got := Shown(name, "pat@example.test"); got != "pat@example.test" {
			t.Errorf("Shown(%q) = %q, want the username alone", name, got)
		}
	}
}
