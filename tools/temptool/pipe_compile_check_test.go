package temptool

// A write endpoint is not fired by test, so its response_pipe never meets a
// real reply there: a filter that does not compile is caught by the compile
// check or not at all, and the first live write is when it breaks.

import (
	"strings"
	"testing"
)

func TestABrokenPipeOnAWriteFailsTheTest(t *testing.T) {
	f, sess := newFakeAPI(t, "notes")
	if _, err := createGrouped(map[string]any{
		"name": "notes", "description": "d", "mode": "toolbox", "credential": "notes",
		"actions": []any{map[string]any{
			"name": "add_note", "method": "POST", "url_template": f.srv.URL + "/notes",
			"body_template": `{"text": {text}}`,
			"params":        map[string]any{"text": map[string]any{"type": "string"}},
			"response_pipe": `jq '.items['`,
		}},
	}, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	report, err := testGrouped(map[string]any{"name": "notes", "cases": []any{
		map[string]any{"action": "add_note", "args": map[string]any{"text": "hi"}},
	}}, sess)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if !strings.Contains(report, "[FAIL] add_note") || !strings.Contains(report, "response_pipe has a syntax/compile error") {
		t.Errorf("a pipe that does not compile should fail the endpoint; report:\n%s", report)
	}
	if n := f.count(); n != 0 {
		t.Errorf("a write endpoint must not be fired by test; %d call(s) made", n)
	}
}
