package orchestrate

import "testing"

// normalizeAgentKey collapses case + separator drift so a schedule target
// stored as one spelling resolves to the agent's actual Name.
func TestNormalizeAgentKey(t *testing.T) {
	cases := [][2]string{
		{"wiwee-summary", "wiwee summary"},
		{"WiWee Summary", "wiwee summary"},
		{"wiwee_summary", "wiwee summary"},
		{"  WiWee--Summary  ", "wiwee summary"},
		{"daily_news-brief", "daily news brief"},
		{"already normal", "already normal"},
	}
	for _, c := range cases {
		if got := normalizeAgentKey(c[0]); got != c[1] {
			t.Errorf("normalizeAgentKey(%q) = %q, want %q", c[0], got, c[1])
		}
	}
	// Distinct agents must not collide under normalization.
	if normalizeAgentKey("wiwee-summary") == normalizeAgentKey("wiwee-digest") {
		t.Error("distinct names collided under normalization")
	}
}

// canonicalToolName rewrites a retired tool name in an agent's allowlist to
// the live one; unknown names pass through unchanged.
func TestCanonicalToolName(t *testing.T) {
	if got := canonicalToolName("read_phantom_chat"); got != "read_chat" {
		t.Errorf("read_phantom_chat -> %q, want read_chat", got)
	}
	if got := canonicalToolName("list_phantom_chats"); got != "list_chats" {
		t.Errorf("list_phantom_chats -> %q, want list_chats", got)
	}
	if got := canonicalToolName("web_search"); got != "web_search" {
		t.Errorf("live name should pass through, got %q", got)
	}
}
