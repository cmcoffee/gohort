package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestRunSandboxedScriptStdinStdout proves the framework feeds {prior,current}
// to a format_script's stdin and captures its stdout — i.e. an "empty output"
// suppression is the SCRIPT's doing, not a stdin/stdout wiring bug.
func TestRunSandboxedScriptStdinStdout(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{
		"prior":   "No clients connected.",
		"current": `[{"clid":"170","client_nickname":"SouthPawn"}]`,
	})

	// A correct join/leave-style script: parse stdin, print something.
	script := `import sys, json
d = json.load(sys.stdin)
print("prior_len=%d current_len=%d" % (len(d["prior"]), len(d["current"])))`

	res := RunSandboxedScript(context.Background(), "python3", script, string(payload))
	t.Logf("sandbox=%v err=%v stdout=%q stderr=%q", res.Sandbox, res.Err, res.Stdout, res.Stderr)

	if res.Err != nil {
		t.Fatalf("script errored (interpreter/sandbox problem?): %v stderr=%q", res.Err, res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) == "" {
		t.Fatalf("FRAMEWORK BUG: well-formed script produced EMPTY stdout — stdin not piped or stdout not captured. stderr=%q", res.Stderr)
	}
	if !strings.Contains(res.Stdout, "current_len=") {
		t.Fatalf("stdout missing expected content: %q", res.Stdout)
	}

	// And confirm formatWatchAlert (the watcher's actual call) returns that text.
	summary, suppress := formatWatchAlert(context.Background(), "tester", "probe", script, "No clients connected.", `[{"clid":"170"}]`, false)
	t.Logf("formatWatchAlert summary=%q suppress=%v", summary, suppress)
	if suppress || strings.TrimSpace(summary) == "" {
		t.Fatalf("formatWatchAlert suppressed a script that prints output: suppress=%v summary=%q", suppress, summary)
	}
}
