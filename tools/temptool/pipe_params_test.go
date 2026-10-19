package temptool

// A response_pipe reads the call's params as a shell tool's command does, by
// name in its environment. It got none of them, so a toolbox could not page a
// long file by start_line, and the only way left was to rebuild the tool as a
// script that fetched and paged in Python.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAResponsePipeReadsTheCallsParams(t *testing.T) {
	f, sess := newFakeAPI(t, "pager")
	tt := &TempTool{
		Name: "read_file", Mode: TempToolModeAPI, Credential: "pager",
		CommandTemplate: f.srv.URL + "/file",
		Params:          map[string]ToolParam{"start_line": {Type: "integer"}, "note": {Type: "string"}},
		ResponsePipe:    `echo "from $start_line: $note"`,
	}
	out, err := dispatchAPIModeTempTool(sess, tt, map[string]any{"start_line": float64(41), "note": "$(echo ran)"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "from 41") {
		t.Errorf("the pipe did not see start_line: %q", out)
	}
	// A value is a value: it is never run as part of the pipe.
	if !strings.Contains(out, "$(echo ran)") {
		t.Errorf("a param's value should reach the pipe as text: %q", out)
	}
}

// The tool test runs the pipe with its case's args too, or a pipe that pages
// by a param passes live and fails its own test.
func TestTheToolTestRunsThePipeWithTheCasesArgs(t *testing.T) {
	f, sess := newFakeAPI(t, "pager")
	if _, err := createGrouped(map[string]any{
		"name": "files", "description": "d", "mode": "toolbox", "credential": "pager",
		"actions": []any{map[string]any{
			"name": "read", "url_template": f.srv.URL + "/file",
			"params": map[string]any{"count": map[string]any{"type": "integer"}},
			// Without count this is limit(; ...), a jq syntax error, which the
			// compile check and the run on the real body both report.
			"response_pipe": `jq "limit(${count}; .items[])"`,
		}},
	}, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	report, err := testGrouped(map[string]any{"name": "files", "cases": []any{
		map[string]any{"action": "read", "args": map[string]any{"count": 1}},
	}}, sess)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if !strings.Contains(report, "response_pipe runs clean on the real response") || strings.Contains(report, "[FAIL]") {
		t.Errorf("the pipe should run with the case's count; report:\n%s", report)
	}
}

func TestAPipeIsNotHandedWhatTheEnvironmentCannotHold(t *testing.T) {
	env := pipeEnv(map[string]any{"q": "x", "GOHORT_HOOK_PATH": "/tmp/s", "big": strings.Repeat("a", maxEnvArgBytes+1)})
	if env["q"] != "x" {
		t.Errorf("a param is missing: %v", env)
	}
	if _, ok := env["GOHORT_HOOK_PATH"]; ok {
		t.Error("a GOHORT_ name belongs to the framework, not a param")
	}
	if _, ok := env["big"]; ok {
		t.Error("a value too large for the environment was handed over")
	}
}
