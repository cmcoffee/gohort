package core

import (
	"strings"
	"testing"
)

// sessWithTools builds a session holding just the tools a case needs. The
// claim is read from the turn-resolved set, so this is the whole fixture.
func sessWithTools(tools ...*TempTool) *ToolSession {
	return &ToolSession{TempTools: tools}
}

// The shape that started this: an api-mode toolbox whose actions all point at
// one host. The claim has to fall out of the URL templates the tool already
// stores — nothing is authored for it, and no host is named in framework code.
func TestToolClaimsHostFromItsOwnURLTemplates(t *testing.T) {
	sess := sessWithTools(&TempTool{Name: "moltbook", Mode: "api", Actions: []TempToolAction{
		{Name: "reply_to_post", URLTemplate: "https://www.moltbook.com/api/v1/comments"},
		{Name: "get_feed", URLTemplate: "https://www.moltbook.com/api/v1/posts?submolt={submolt}"},
	}})
	note := ToolClaimNote(sess, "https://www.moltbook.com/philosophy/feed")
	for _, want := range []string{`"moltbook"`, "get_feed", "reply_to_post", "moltbook.com"} {
		if !strings.Contains(note, want) {
			t.Errorf("note does not mention %q: %s", want, note)
		}
	}
	// Sorted, so the note reads the same way every time and doesn't churn the
	// prompt prefix between otherwise identical rounds.
	if strings.Index(note, "get_feed") > strings.Index(note, "reply_to_post") {
		t.Errorf("actions are not sorted: %s", note)
	}
}

// A tool written against the apex host still covers the www one.
func TestClaimIgnoresWWWPrefix(t *testing.T) {
	sess := sessWithTools(&TempTool{Name: "api", Mode: "api",
		CommandTemplate: "https://example.com/v1/{path}"})
	if ToolClaimNote(sess, "https://www.example.com/page") == "" {
		t.Error("a tool on example.com should claim www.example.com")
	}
}

// A tool the caller cannot run must not be advertised — that is the whole
// reason the claim is read from the turn's resolved set.
func TestDisabledToolAndActionDoNotClaim(t *testing.T) {
	off := sessWithTools(&TempTool{Name: "off", Mode: "api", Disabled: true,
		CommandTemplate: "https://example.com/v1"})
	if note := ToolClaimNote(off, "https://example.com/x"); note != "" {
		t.Errorf("disabled tool claimed a host: %s", note)
	}
	quarantined := sessWithTools(&TempTool{Name: "box", Mode: "api", Actions: []TempToolAction{
		{Name: "gone", URLTemplate: "https://example.com/v1", Disabled: true},
	}})
	if note := ToolClaimNote(quarantined, "https://example.com/x"); note != "" {
		t.Errorf("quarantined action claimed a host: %s", note)
	}
}

// The common case is no claim, and it must say nothing — this runs on every
// fetch the system makes.
func TestUnclaimedHostGetsNoNote(t *testing.T) {
	sess := sessWithTools(&TempTool{Name: "moltbook", Mode: "api", Actions: []TempToolAction{
		{Name: "get_feed", URLTemplate: "https://www.moltbook.com/api/v1/posts"},
	}})
	if note := ToolClaimNote(sess, "https://en.wikipedia.org/wiki/Go"); note != "" {
		t.Errorf("unrelated host got a note: %s", note)
	}
	if note := ToolClaimNote(nil, "https://www.moltbook.com/"); note != "" {
		t.Errorf("nil session got a note: %s", note)
	}
}

// A shell tool's CommandTemplate is a command line, not a URL. Reading a host
// out of it would claim on the strength of a URL sitting in an argument.
func TestShellModeToolClaimsNothingFromItsCommand(t *testing.T) {
	sess := sessWithTools(&TempTool{Name: "curler",
		CommandTemplate: "curl https://example.com/v1/{thing}"})
	if note := ToolClaimNote(sess, "https://example.com/v1/x"); note != "" {
		t.Errorf("shell tool claimed a host from its command line: %s", note)
	}
}
