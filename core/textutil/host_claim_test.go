package textutil

import (
	"strings"
	"testing"
)

func TestHostKeyNormalizes(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.moltbook.com/api/v1/posts?x={y}": "moltbook.com",
		"https://moltbook.com":                        "moltbook.com",
		"http://Example.COM:8080/path":                "example.com",
		"https://user:pw@example.com/x":               "example.com",
	} {
		if got := HostKey(in); got != want {
			t.Errorf("HostKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// A host that isn't known until dispatch compares as nothing. Guessing would
// attach a confident pointer to the wrong tool.
func TestHostKeyRefusesTemplatedAndMalformedHosts(t *testing.T) {
	for _, in := range []string{
		"https://{region}.api.example.com/v1", // host decided at dispatch
		"curl https://example.com/x",          // a command line, not a URL
		"/custom/app/",                        // a path on this server
		"", "   ", "https://",
	} {
		if got := HostKey(in); got != "" {
			t.Errorf("HostKey(%q) = %q, want empty", in, got)
		}
	}
}

func TestClaimNoteListsToolsAndCapsActions(t *testing.T) {
	long := []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8"}
	note := ClaimNote("example.com", []ToolClaim{{Tool: "big", Actions: long}})
	if !strings.Contains(note, "+2 more") {
		t.Errorf("action list was not capped: %s", note)
	}
	two := ClaimNote("example.com", []ToolClaim{{Tool: "zed"}, {Tool: "alpha"}})
	if !strings.Contains(two, `"alpha" and "zed"`) {
		t.Errorf("two tools should read as a list: %s", two)
	}
}

func TestClaimNoteEmptyWithoutClaims(t *testing.T) {
	if ClaimNote("example.com", nil) != "" || ClaimNote("", []ToolClaim{{Tool: "x"}}) != "" {
		t.Error("a note was rendered with nothing to say")
	}
}
