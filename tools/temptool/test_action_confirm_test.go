package temptool

import (
	"strings"
	"testing"
)

// tool_def(action="test") fired the authored tool from inside tool_def, whose
// own action asks nobody, so a tool that asks before each call ran with no
// confirmation (and, on an unattended fire, past a block). A gated tool is
// reported for a direct call instead of fired.
func TestTestActionDoesNotFireAConfirmGatedTool(t *testing.T) {
	sess := newTestSession()
	tool := injectShellTool(t, sess, "gated_send", "print('sent')\n")
	tool.ConfirmInChat = true

	report, err := testGrouped(map[string]any{
		"name":  "gated_send",
		"cases": []any{map[string]any{"args": map[string]any{"summary": "x"}}},
	}, sess)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if strings.Contains(report, "live run succeeded") || strings.Contains(report, "live run FAILED") || strings.Contains(report, "live run returned") {
		t.Fatalf("a confirm-gated tool was fired by test; report:\n%s", report)
	}
	if !strings.Contains(report, "asks for confirmation") || !strings.Contains(report, "NOT VERIFIED") {
		t.Fatalf("the report should say why the tool was not run; report:\n%s", report)
	}
}
