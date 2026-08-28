package browser

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// stubBrowser swaps the routed fetch for a canned body and restores it after.
func stubBrowser(t *testing.T, body string) {
	t.Helper()
	prev := BrowserFetchFunc
	BrowserFetchFunc = func(string, int) (string, error) { return body, nil }
	t.Cleanup(func() { BrowserFetchFunc = prev })
}

// End-to-end through the tool: the two signals have to reach the string the
// model actually reads. Both were computed correctly and dropped on the floor
// in an earlier draft, which is exactly what this covers.
func TestBrowsePageAnnotatesLowYieldAndNamesTheCoveringTool(t *testing.T) {
	stubBrowser(t, "moltbook - the front page of the agent internet\n\n"+
		"We've updated our Terms of Service and Privacy Policy! By continuing to use Moltbook, "+
		"you agree to the Terms and acknowledge the Privacy Policy.")

	sess := &ToolSession{TempTools: []*TempTool{{
		Name: "moltbook", Mode: "api", Actions: []TempToolAction{
			{Name: "get_feed", URLTemplate: "https://www.moltbook.com/api/v1/posts"},
		},
	}}}

	out, err := (&BrowsePageTool{}).RunWithSession(
		map[string]any{"url": "https://www.moltbook.com/philosophy/feed"}, sess)
	if err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	if !strings.HasPrefix(out, "[Heads up:") {
		t.Errorf("low-yield note is not at the top where it gets read:\n%s", out)
	}
	if !strings.Contains(out, `"moltbook"`) {
		t.Errorf("result does not name the tool that covers this host:\n%s", out)
	}
}

// The overwhelmingly common path: a real page, no covering tool. It must come
// back exactly as it always did — this runs on every browse the system does.
func TestBrowsePageLeavesOrdinaryResultsAlone(t *testing.T) {
	body := strings.Repeat("A genuine article about thermal routing on a small fleet. ", 40)
	stubBrowser(t, body)

	out, err := (&BrowsePageTool{}).RunWithSession(
		map[string]any{"url": "https://example.com/article"}, &ToolSession{})
	if err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	if strings.Contains(out, "Heads up") || strings.Contains(out, "[Note:") {
		t.Errorf("an ordinary page picked up an annotation:\n%s", out[:200])
	}
	if !strings.HasPrefix(out, "Fetched https://example.com/article") {
		t.Errorf("result shape changed: %s", out[:80])
	}
}
