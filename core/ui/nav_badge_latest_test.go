package ui

import (
	"strings"
	"testing"
)

// A nav badge keeps only the answer to its latest request. On load the agent
// view is drawn before and after the host names the agent on screen, and the
// earlier, agent-less fetch counted every agent's rows; landing last, it left
// the Scheduler badge showing the whole fleet's count. Source-scan, like the
// other runtime tests: what this pins is the sequence guard staying in.
func TestANavBadgeKeepsItsLatestAnswer(t *testing.T) {
	src := mustRuntimePart(t, "30_agent_loop_panel.js")
	i := strings.Index(src, "function refreshChannelBadges(")
	if i < 0 {
		t.Fatal("refreshChannelBadges is gone")
	}
	body := src[i:]
	if j := strings.Index(body, "\n      }\n"); j > 0 {
		body = body[:j]
	}
	for _, want := range []string{
		"var seq = badgeSeq[i] = (badgeSeq[i] || 0) + 1;",
		"if (badgeSeq[i] !== seq) return;",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("a stale badge reply can overwrite a newer one: missing %s", want)
		}
	}
}
