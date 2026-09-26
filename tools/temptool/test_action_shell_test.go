package temptool

import (
	"os"
	"path/filepath"
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
// scriptSyntaxCheckForTest exercises the checker with no session, which is
// also the real shape of an unattended caller: no human behind it, so no admin
// stamp. The checker's verdict must not depend on who asked.
func scriptSyntaxCheckForTest(tt TempTool) (string, string, bool) {
	return scriptSyntaxCheck(tt, nil)
}

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
	_, problem, checked := scriptSyntaxCheckForTest(TempTool{
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
	lang, problem, checked := scriptSyntaxCheckForTest(TempTool{
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
	lang, problem, checked := scriptSyntaxCheckForTest(TempTool{
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

// Re-running the same test on the same unchanged tool returns the last result
// instead of running again. Observed: the same failing test, re-run four
// seconds apart on an unchanged tool, until the loop guard blocked tool_def.
func TestAnUnchangedTestIsNotRunAgain(t *testing.T) {
	sess := newTestSession()
	sess.ChatSessionID = "rerun-" + t.Name()
	sess.WorkspaceDir = t.TempDir()
	injectShellTool(t, sess, "echo_it", "import os\nprint(os.environ.get('summary'))\n")
	args := map[string]any{"name": "echo_it", "cases": []any{map[string]any{"args": map[string]any{"summary": "hi"}}}}

	first, err := testGrouped(args, sess)
	if err != nil || strings.Contains(first, "UNCHANGED") {
		t.Fatalf("the first test runs: %v\n%s", err, first)
	}
	again, _ := testGrouped(args, sess)
	if !strings.HasPrefix(again, "UNCHANGED") || !strings.Contains(again, first) {
		t.Errorf("an identical re-run should return the last result, marked:\n%s", again)
	}
	other := map[string]any{"name": "echo_it", "cases": []any{map[string]any{"args": map[string]any{"summary": "bye"}}}}
	if out, _ := testGrouped(other, sess); strings.HasPrefix(out, "UNCHANGED") {
		t.Error("different cases are a different test")
	}
	forced := map[string]any{"name": "echo_it", "rerun": true, "cases": args["cases"]}
	if out, _ := testGrouped(forced, sess); strings.HasPrefix(out, "UNCHANGED") {
		t.Error("rerun=true runs it again")
	}
	forgetToolTest(sess, "echo_it")
	if out, _ := testGrouped(args, sess); strings.HasPrefix(out, "UNCHANGED") {
		t.Error("a save forgets the last result")
	}
}

// A value too big for an environment variable reaches the script as a file,
// and a string param can name a workspace file. Observed: a pipeline handed a
// 262 KB response to its extraction step, which failed with "Argument list too
// long" on every run. Tested at the hand-off: the sandbox hides the host's
// /tmp, so a script cannot run in a test's temp workspace.
func TestALargeValueReachesTheScriptAsAFile(t *testing.T) {
	ws := t.TempDir()
	sess := &ToolSession{WorkspaceDir: ws}
	tt := &TempTool{Name: "size_it", Params: map[string]ToolParam{"summary": {Type: "string"}, "opts": {Type: "object"}}}

	big := strings.Repeat("x", 300*1024)
	args := map[string]any{"summary": big, "small": "ok"}
	env := buildEnvArgs(args)
	moved, err := passLargeArgs(tt, args, env, sess, ws)
	if err != nil {
		t.Fatal(err)
	}
	if env["summary"] != "" || env["small"] != "ok" || len(moved) != 1 {
		t.Fatalf("only the oversized value moves, and $summary is left empty: %d moved, summary=%d bytes", len(moved), len(env["summary"]))
	}
	data, err := os.ReadFile(env["summary_file"])
	if err != nil || string(data) != big {
		t.Fatalf("$summary_file should hold the whole value: %v", err)
	}
	if !strings.HasPrefix(env["summary_file"], filepath.Join(ws, ".tool_args")) {
		t.Errorf("the file belongs in the run's workspace: %s", env["summary_file"])
	}
	if note := fileArgsNote(moved); !strings.Contains(note, "summary_file") || !strings.Contains(note, "300 KB") {
		t.Errorf("a failed run should say how the value arrived: %q", note)
	}

	if err := os.MkdirAll(filepath.Join(ws, ".tool_spill"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".tool_spill", "resp.json"), []byte(`{"ok":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []any{map[string]any{"file": ".tool_spill/resp.json"}, `{"file": ".tool_spill/resp.json"}`} {
		args = map[string]any{"summary": ref}
		env = buildEnvArgs(args)
		if _, err := passLargeArgs(tt, args, env, sess, ws); err != nil || env["summary"] != `{"ok":true}` {
			t.Errorf("a named workspace file arrives as the value (%T): %q %v", ref, env["summary"], err)
		}
	}
	args = map[string]any{"opts": map[string]any{"file": "x"}}
	env = buildEnvArgs(args)
	if _, err := passLargeArgs(tt, args, env, sess, ws); err != nil || env["opts"] != `{"file":"x"}` {
		t.Errorf("an object param keeps its object, even one shaped like a file reference: %q %v", env["opts"], err)
	}
	args = map[string]any{"summary": map[string]any{"file": "../../etc/passwd"}}
	if _, err := passLargeArgs(tt, args, buildEnvArgs(args), sess, ws); err == nil {
		t.Error("a file outside the workspace must be refused")
	}
}
