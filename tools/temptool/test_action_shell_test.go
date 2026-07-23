package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// injectShellTool persists a shell-mode tool the way createGrouped stores one:
// Mode left EMPTY (the legacy spelling of shell), script_body on the record.
func injectShellTool(t *testing.T, sess *ToolSession, name, body string) *TempTool {
	t.Helper()
	tool := &TempTool{
		Name:                name,
		Description:         "a shell tool",
		CommandTemplate:     "python3 {workspace_dir}/" + name + ".py",
		ScriptName:          name + ".py",
		CanonicalScriptName: name + ".py",
		ScriptBody:          body,
		Params: map[string]ToolParam{
			"summary": {Type: "string"},
		},
		Required: []string{"summary"},
	}
	if err := sess.AppendTempTool(tool); err != nil {
		t.Fatalf("inject: %v", err)
	}
	return tool
}

// TestShellToolIsNotProbedAsHTTP is the regression for the CalDAV loop: a
// shell tool stored with Mode=="" was swept into the api branch and
// "verified" by HTTP-GETting its command_template, reporting
// `unsupported protocol scheme ""` on a script that had nothing to do with
// HTTP. The author then edited command_template repeatedly trying to make
// a python3 invocation parse as a URL.
func TestShellToolIsNotProbedAsHTTP(t *testing.T) {
	sess := newTestSession()
	injectShellTool(t, sess, "cal_create", "print('ok')\n")

	report, err := testGrouped(map[string]any{"name": "cal_create"}, sess)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if strings.Contains(report, "unsupported protocol scheme") ||
		strings.Contains(report, "live probe errored") ||
		strings.Contains(report, "(GET)") {
		t.Fatalf("shell tool must not be HTTP-probed; report:\n%s", report)
	}
	if !strings.Contains(report, "shell tool") {
		t.Fatalf("expected a shell-tool report header; report:\n%s", report)
	}
	// Required params reach a shell script via env vars, not the URL/body
	// templates — the api-only "sent NOWHERE" verdict must not appear.
	if strings.Contains(report, "sent NOWHERE") || strings.Contains(report, "the API will never receive them") {
		t.Fatalf("api param-wiring verdicts must not apply to a shell tool; report:\n%s", report)
	}
}

// TestShellToolWithoutCasesIsUnverified confirms a shell tool that was never
// executed does not get signed off. Running it is the only proof, so the
// report must say UNVERIFIED and tell the author to pass cases.
func TestShellToolWithoutCasesIsUnverified(t *testing.T) {
	sess := newTestSession()
	injectShellTool(t, sess, "cal_list", "print('ok')\n")

	report, err := testGrouped(map[string]any{"name": "cal_list"}, sess)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if !strings.Contains(report, "UNVERIFIED") {
		t.Fatalf("expected UNVERIFIED without cases; report:\n%s", report)
	}
	if !strings.Contains(report, "cases=") {
		t.Fatalf("expected the report to name the fix (pass cases); report:\n%s", report)
	}
}

// TestShellToolEnvOnlyParamsAreNoted covers the delivery-route note: a
// required param absent from command_template still arrives as a lowercase
// env var, so it must be reported as a route — never as a failure.
func TestShellToolEnvOnlyParamsAreNoted(t *testing.T) {
	sess := newTestSession()
	injectShellTool(t, sess, "cal_env", "print('ok')\n")

	report, err := testGrouped(map[string]any{"name": "cal_env"}, sess)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if !strings.Contains(report, "env var") {
		t.Fatalf("expected the env-var delivery note for 'summary'; report:\n%s", report)
	}
	if strings.Contains(report, "FAIL  required param") {
		t.Fatalf("an env-delivered param must not FAIL; report:\n%s", report)
	}
}

// TestEffectiveTempToolMode pins the mode resolution the routing depends on:
// empty means shell, unless the command_template is plainly an http(s) URL
// (an api tool recorded before Mode was populated).
func TestEffectiveTempToolMode(t *testing.T) {
	cases := []struct {
		name string
		tt   TempTool
		want string
	}{
		{"empty mode + shell command", TempTool{CommandTemplate: "python3 x.py"}, TempToolModeShell},
		{"empty mode + url", TempTool{CommandTemplate: "https://x.test/api"}, TempToolModeAPI},
		{"explicit shell", TempTool{Mode: TempToolModeShell, CommandTemplate: "uname -a"}, TempToolModeShell},
		{"explicit api", TempTool{Mode: TempToolModeAPI, CommandTemplate: "/v1/posts"}, TempToolModeAPI},
		{"toolbox", TempTool{Mode: TempToolModeToolbox}, TempToolModeToolbox},
		{"persistent", TempTool{Mode: TempToolModePersistent}, TempToolModePersistent},
	}
	for _, c := range cases {
		if got := effectiveTempToolMode(c.tt); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// TestShellRunFailed covers the exit-status detection: dispatchTempTool
// returns a non-zero exit as OUTPUT with a trailing marker, not as an error,
// so a verifier that only checks err calls a script that died a success.
func TestShellRunFailed(t *testing.T) {
	if !shellRunFailed("Traceback...\n[exit: exit status 1]") {
		t.Error("non-zero exit must be detected as a failure")
	}
	if !shellRunFailed("[TIMED OUT after 1m30s — command killed.]") {
		t.Error("timeout must be detected as a failure")
	}
	if shellRunFailed(`{"uid": "abc123"}`) {
		t.Error("clean output must not be flagged as a failure")
	}
}

// TestScriptSyntaxCheckUnknownLanguage confirms an unrecognized script type
// yields no verdict rather than a false syntax-error accusation.
func TestScriptSyntaxCheckUnknownLanguage(t *testing.T) {
	_, problem, checked := scriptSyntaxCheck(TempTool{
		ScriptName: "thing.zig", ScriptBody: "not a language we check",
	})
	if checked || problem != "" {
		t.Fatalf("unknown language must report no verdict; problem=%q checked=%v", problem, checked)
	}
}

// TestScriptSyntaxCheckCatchesUnterminatedString is the check against the
// exact bug that shipped: a python script with an unterminated f-string, so
// every dispatch returned "SyntaxError: EOL while scanning string literal"
// and nothing in the tool ever ran. Skips where the sandbox can't reach a
// python3 to ask (the checker reports no verdict rather than guessing).
func TestScriptSyntaxCheckCatchesUnterminatedString(t *testing.T) {
	lang, problem, checked := scriptSyntaxCheck(TempTool{
		ScriptName: "cal.py",
		ScriptBody: "ical = ''\nical += f\"DESCRIPTION:{description}\nprint(ical)\n",
	})
	if !checked {
		t.Skipf("no %s syntax checker reachable in this environment", lang)
	}
	if problem == "" {
		t.Fatalf("expected the unterminated f-string to be reported as a syntax error")
	}
}

// TestScriptSyntaxCheckPassesValidScript confirms a clean script is not
// accused of a syntax error.
func TestScriptSyntaxCheckPassesValidScript(t *testing.T) {
	lang, problem, checked := scriptSyntaxCheck(TempTool{
		ScriptName: "ok.py",
		ScriptBody: "import os\nprint(os.environ.get('summary', ''))\n",
	})
	if !checked {
		t.Skipf("no %s syntax checker reachable in this environment", lang)
	}
	if problem != "" {
		t.Fatalf("valid script reported as broken: %s", problem)
	}
}
