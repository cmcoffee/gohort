package core

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsNonPublicHost(t *testing.T) {
	nonPublic := []string{"", "localhost", "127.0.0.1", "::1", "10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.1.1", "0.0.0.0", "foo.local", "bar.internal"}
	for _, h := range nonPublic {
		if !IsNonPublicHost(h) {
			t.Errorf("%q should be non-public", h)
		}
	}
	public := []string{"i.redd.it", "example.com", "8.8.8.8", "1.1.1.1", "graph.microsoft.com"}
	for _, h := range public {
		if IsNonPublicHost(h) {
			t.Errorf("%q should be public", h)
		}
	}
}

// The oddjob python helper is deployed best-effort, and every failure
// path used to be Debug-only or silent. That is the wrong volume for
// this particular failure: the only symptom that reaches anyone is a
// ModuleNotFoundError on the first line of a script, which names the
// script's import rather than the deployment that never happened.

func TestOddjobLibReportsWhyItCouldNotDeploy(t *testing.T) {
	// A run with no workspaces dir configured said nothing at all, at
	// any level — the case that leaves an operator with a broken tool
	// and an empty log.
	prevDir := WorkspacesDir()
	SetWorkspacesDir("")
	oddjobLibDirMu.Lock()
	oddjobLibDirPath, oddjobLibWarned = "", false
	oddjobLibDirMu.Unlock()
	t.Cleanup(func() {
		SetWorkspacesDir(prevDir)
		oddjobLibDirMu.Lock()
		oddjobLibDirPath, oddjobLibWarned = "", false
		oddjobLibDirMu.Unlock()
	})

	var lines []string
	prevLog := Log
	Log = func(v ...any) {
		if len(v) > 0 {
			lines = append(lines, fmt.Sprint(v[0]))
		}
	}
	t.Cleanup(func() { Log = prevLog })

	if got := EnsureOddjobLibDir(); got != "" {
		t.Fatalf("with no workspaces dir there is nowhere to deploy, got %q", got)
	}
	if len(lines) == 0 {
		t.Fatal("the failure was silent — the only symptom left is ModuleNotFoundError inside a script")
	}
	joined := strings.Join(lines, "\n")
	// It must name the consequence, not just the fault: whoever reads
	// this is about to be told by an agent that a tool is broken.
	for _, want := range []string{"ModuleNotFoundError", "workspaces directory"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the warning should mention %q: %s", want, joined)
		}
	}

	// Once per process, not once per dispatch: this runs on every
	// sandboxed tool call.
	before := len(lines)
	EnsureOddjobLibDir()
	EnsureOddjobLibDir()
	if len(lines) != before {
		t.Errorf("warned again on later dispatches: %d → %d lines", before, len(lines))
	}
}

// And the happy path still deploys something importable.
func TestOddjobLibDeploysAnImportablePackage(t *testing.T) {
	dir := t.TempDir()
	prevDir := WorkspacesDir()
	SetWorkspacesDir(filepath.Join(dir, "workspaces"))
	oddjobLibDirMu.Lock()
	oddjobLibDirPath, oddjobLibWarned = "", false
	oddjobLibDirMu.Unlock()
	t.Cleanup(func() {
		SetWorkspacesDir(prevDir)
		oddjobLibDirMu.Lock()
		oddjobLibDirPath, oddjobLibWarned = "", false
		oddjobLibDirMu.Unlock()
	})

	lib := EnsureOddjobLibDir()
	if lib == "" {
		t.Fatal("deployment failed on a writable path")
	}
	// PYTHONPATH points at the directory CONTAINING the package, so
	// `import oddjob` resolves the package dir beneath it.
	if _, err := os.Stat(filepath.Join(lib, "oddjob", "__init__.py")); err != nil {
		t.Errorf("no importable oddjob package under %s: %v", lib, err)
	}
}

// A unix socket path over 107 bytes fails with a bare EINVAL — "invalid
// argument" — which names neither the limit nor the path. It cost a live
// deployment a working tool and an operator a hunt through permissions and
// filesystems before the length was the suspect. These pin the arithmetic so it
// cannot come back quietly.

func TestHookSocketFitsUnderADeepWorkspace(t *testing.T) {
	// The real path from the failure, rebuilt: a per-agent workspace is
	// <root>/.agents/<email>/<uuid>/, which is 92 characters before the socket
	// name has even started.
	deep := "/opt/oddjob/data/workspaces/.agents/owner@example.test/45dbd021-4c1d-494b-a2ab-6416c355cbd8"
	old := filepath.Join(deep, ".oddjob_hook_084099fb5aee06bf.sock")
	if len(old) <= maxUnixSocketPath {
		t.Fatalf("the path that failed is %d bytes — this test has lost its subject", len(old))
	}

	got, err := hookSocketPath(deep, "084099fb5aee06bf")
	if err != nil {
		t.Fatalf("a deep workspace must still get a socket: %v", err)
	}
	if len(got) > maxUnixSocketPath {
		t.Errorf("still too long at %d bytes: %s", len(got), got)
	}
	// And it is NOT in the workspace, which is the whole point — shortening
	// the name could never have been enough, since the prefix alone leaves 15
	// characters and ".oddjob_hook_.sock" is 18 with no token.
	if strings.HasPrefix(got, deep) {
		t.Errorf("a deep workspace cannot host the socket: %s", got)
	}
}

func TestHookSocketNameIsUnique(t *testing.T) {
	// Two hooks alive at once must not collide — the token is what separates
	// them, and moving to a shared directory is exactly where a dropped token
	// would start mattering.
	a, err := hookSocketPath(t.TempDir(), "aaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	b, err := hookSocketPath(t.TempDir(), "bbbbbbbbbbbbbbbb")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("two tokens produced one path: %s", a)
	}
	if filepath.Dir(a) != filepath.Dir(b) {
		t.Errorf("both should sit in the same short dir, got %s and %s", a, b)
	}
	// 0700 on the directory: the sockets are 0600 already, but a listable
	// directory would hand every token to anyone on the host.
	if fi, err := os.Stat(filepath.Dir(a)); err == nil {
		if perm := fi.Mode().Perm(); perm != 0700 {
			t.Errorf("hook dir perms are %o, want 700", perm)
		}
	}
}

// The end-to-end check the arithmetic above cannot make: that a hook actually
// BINDS under a workspace path long enough to have failed before. This is the
// regression test for the live failure — everything else here is the reasoning
// that led to it.
func TestHookActuallyListensUnderADeepWorkspace(t *testing.T) {
	deep := filepath.Join(t.TempDir(),
		".agents", "owner@example.test", "45dbd021-4c1d-494b-a2ab-6416c355cbd8")
	h, err := NewSandboxHook(deep, []string{"log"}, &ToolSession{})
	if err != nil {
		t.Fatalf("a hook under a deep workspace must still listen: %v", err)
	}
	if h == nil {
		t.Fatal("capabilities were declared, so there should be a hook")
	}
	defer h.Close()
	if strings.HasPrefix(h.SocketPath, deep) {
		t.Errorf("the socket cannot live in a workspace this deep: %s", h.SocketPath)
	}
	if len(h.SocketPath) > maxUnixSocketPath {
		t.Errorf("bound path is %d bytes: %s", len(h.SocketPath), h.SocketPath)
	}
}

// No declared capabilities means no socket at all — the privacy posture the
// hook was built with, and a path that must not start binding things now that
// binding is cheaper.
func TestNoCapabilitiesStillMeansNoSocket(t *testing.T) {
	h, err := NewSandboxHook(t.TempDir(), nil, &ToolSession{})
	if err != nil || h != nil {
		t.Errorf("a tool with no capabilities gets no hook, got %v / %v", h, err)
	}
}

// The hook counts what a script asked of it, so a run killed at its timeout
// with nothing asked can say it never reached its service.
func TestTheHookCountsTheScriptsCalls(t *testing.T) {
	h, err := NewSandboxHook(t.TempDir(), []string{"log"}, &ToolSession{})
	if err != nil || h == nil {
		t.Fatalf("hook: %v", err)
	}
	defer h.Close()
	if h.Calls.Load() != 0 {
		t.Fatal("no call yet")
	}
	conn, err := net.Dial("unix", h.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(`{"method":"log","params":{"message":"hi"}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if n := h.Calls.Load(); n != 1 {
		t.Errorf("one request made, counted %d", n)
	}
}

// call_tool runs only a tool the script names in an exact tool:<name>
// capability, and hands back the tool's output.
func TestHookCallsOnlyDeclaredTools(t *testing.T) {
	var ran []string
	sess := &ToolSession{Username: "owner", CallTool: func(name string, args map[string]any) (string, error) {
		ran = append(ran, name+":"+fmt.Sprint(args["city"]))
		return "sunny", nil
	}}
	call := func(caps []string, name string) string {
		h := &SandboxHook{Capabilities: caps, Sess: sess}
		a, b := net.Pipe()
		go func() { h.handleTool(a, map[string]interface{}{"name": name, "args": map[string]interface{}{"city": "Reno"}}); a.Close() }()
		out, _ := io.ReadAll(b)
		return string(out)
	}
	if out := call([]string{"tool:get_weather"}, "get_weather"); !strings.Contains(out, `"output":"sunny"`) {
		t.Fatalf("declared tool: %s", out)
	}
	if out := call([]string{"tool:get_weather"}, "send_email"); !strings.Contains(out, "does not declare") {
		t.Fatalf("undeclared tool: %s", out)
	}
	if out := call([]string{"tool"}, "get_weather"); !strings.Contains(out, "does not declare") {
		t.Fatalf("a bare tool capability granted a call: %s", out)
	}
	if len(ran) != 1 || ran[0] != "get_weather:Reno" {
		t.Fatalf("ran %v", ran)
	}
	// Outside an app script (a chat tool) there is no caller to reach.
	h := &SandboxHook{Capabilities: []string{"tool:get_weather"}, Sess: &ToolSession{Username: "owner"}}
	a, b := net.Pipe()
	go func() { h.handleTool(a, map[string]interface{}{"name": "get_weather"}); a.Close() }()
	if out, _ := io.ReadAll(b); !strings.Contains(string(out), "only for a custom app") {
		t.Fatalf("a session without CallTool: %s", out)
	}
}

// The whole path a script takes: python's oddjob.call_tool over the real
// hook socket, back with the tool's output. Skips without python3.
func TestPythonCallToolReachesTheHook(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	dir := t.TempDir()
	h, err := NewSandboxHook(dir, []string{"tool:get_weather"}, &ToolSession{Username: "owner", CallTool: func(name string, args map[string]any) (string, error) {
		return fmt.Sprintf("%s for %v, forecast=%v", name, args["city"], args["forecast"]), nil
	}})
	if err != nil || h == nil {
		t.Fatalf("hook: %v", err)
	}
	defer h.Close()
	lib := t.TempDir()
	if err := os.WriteFile(filepath.Join(lib, "oddjob.py"), []byte(SandboxHookPythonShim), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `from oddjob import call_tool, HookError
print(call_tool("get_weather", city="Reno", forecast=True))
try:
    call_tool("send_email", to="x")
except HookError as e:
    print("refused:", e)
`
	cmd := exec.Command(py, "-c", script)
	cmd.Env = append(os.Environ(), "ODDJOB_HOOK_PATH="+h.SocketPath, "PYTHONPATH="+lib)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("python: %v\n%s", err, out)
	}
	got := string(out)
	if !strings.Contains(got, "get_weather for Reno, forecast=true") || !strings.Contains(got, "refused:") || !strings.Contains(got, "does not declare") {
		t.Fatalf("output:\n%s", got)
	}
}

// A script that calls a tool it did not declare is told which entry to add,
// not handed the list of everything it does have.
func TestAnUndeclaredToolCallNamesTheGrant(t *testing.T) {
	h := &SandboxHook{Capabilities: []string{"fetch", "log"}, Sess: &ToolSession{Username: "owner"}}
	a, b := net.Pipe()
	go h.handleConn(a)
	fmt.Fprintln(b, `{"method":"tool","params":{"name":"get_weather","args":{}}}`)
	out, _ := io.ReadAll(b)
	if !strings.Contains(string(out), `tool:get_weather`) || !strings.Contains(string(out), "does not declare") {
		t.Fatalf("refusal: %s", out)
	}
}

// oddjob.ask in a script, over the real hook: granted by the "ask"
// capability, answered by the session's Ask, refused without the grant.
func TestPythonAskReachesTheHook(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	lib := t.TempDir()
	if err := os.WriteFile(filepath.Join(lib, "oddjob.py"), []byte(SandboxHookPythonShim), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(caps []string) string {
		h, err := NewSandboxHook(t.TempDir(), caps, &ToolSession{Username: "owner", Ask: func(p string, j bool) (string, error) {
			return fmt.Sprintf("answer to %q json=%v", p, j), nil
		}})
		if err != nil || h == nil {
			t.Fatalf("hook: %v", err)
		}
		defer h.Close()
		cmd := exec.Command(py, "-c", "from oddjob import ask, HookError\ntry:\n    print(ask('hello', json=True))\nexcept HookError as e:\n    print('refused:', e)\n")
		cmd.Env = append(os.Environ(), "ODDJOB_HOOK_PATH="+h.SocketPath, "PYTHONPATH="+lib)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("python: %v\n%s", err, out)
		}
		return string(out)
	}
	if got := run([]string{"fetch", "ask"}); !strings.Contains(got, `answer to "hello" json=true`) {
		t.Fatalf("granted: %s", got)
	}
	if got := run([]string{"fetch"}); !strings.Contains(got, "refused:") {
		t.Fatalf("not granted: %s", got)
	}
}

// call_tool's output is text that is also its JSON: .get, [] and iteration
// read the parsed value, and json.loads still takes it. Every build reached
// for out.get("current") and got "'str' object has no attribute 'get'".
func TestPythonToolOutputIsTextAndJSON(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	lib := t.TempDir()
	if err := os.WriteFile(filepath.Join(lib, "oddjob.py"), []byte(SandboxHookPythonShim), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := NewSandboxHook(t.TempDir(), []string{"tool:get_weather"}, &ToolSession{Username: "owner", CallTool: func(name string, args map[string]any) (string, error) {
		return `{"current": {"temperature_f": 71.8}, "forecast": [{"date": "2026-10-08"}]}`, nil
	}})
	if err != nil || h == nil {
		t.Fatalf("hook: %v", err)
	}
	defer h.Close()
	script := `import json
from oddjob import call_tool
out = call_tool("get_weather", city="Reno")
print(out.get("current")["temperature_f"])
print(out["forecast"][0]["date"])
print("current" in out, sorted(out)[0])
print(json.loads(out)["current"]["temperature_f"])
print(isinstance(out, str), out.startswith("{"))
`
	cmd := exec.Command(py, "-c", script)
	cmd.Env = append(os.Environ(), "ODDJOB_HOOK_PATH="+h.SocketPath, "PYTHONPATH="+lib)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("python: %v\n%s", err, b)
	}
	want := "71.8\n2026-10-08\nTrue current\n71.8\nTrue True\n"
	if string(b) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", b, want)
	}
}

// oddjob.run_agent and oddjob.run_pipeline in a script, over the real hook:
// each granted by its own capability, answered by the session's RunAgent /
// RunPipeline with the agent or pipeline named (or "" for the app's own), and
// a JSON reply reads as its JSON.
func TestPythonRunAgentAndPipelineReachTheHook(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	lib := t.TempDir()
	if err := os.WriteFile(filepath.Join(lib, "oddjob.py"), []byte(SandboxHookPythonShim), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(caps []string, code string) string {
		h, err := NewSandboxHook(t.TempDir(), caps, &ToolSession{Username: "owner",
			RunAgent: func(agent, p string) (string, error) {
				return fmt.Sprintf(`{"agent": %q, "said": %q}`, agent, p), nil
			},
			RunPipeline: func(pipeline, in string) (string, error) {
				return fmt.Sprintf("pipeline %q ran on %q", pipeline, in), nil
			}})
		if err != nil || h == nil {
			t.Fatalf("hook: %v", err)
		}
		defer h.Close()
		cmd := exec.Command(py, "-c", "from oddjob import run_agent, run_pipeline, HookError\ntry:\n"+code+"except HookError as e:\n    print('refused:', e)\n")
		cmd.Env = append(os.Environ(), "ODDJOB_HOOK_PATH="+h.SocketPath, "PYTHONPATH="+lib)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("python: %v\n%s", err, out)
		}
		return string(out)
	}
	agent := "    r = run_agent('roll for it')\n    print(r.get('agent') == '' and r['said'])\n    print(run_agent('x', agent='Quartermaster').get('agent'))\n"
	if got := run([]string{"fetch", "run_agent"}, agent); !strings.Contains(got, "roll for it") || !strings.Contains(got, "Quartermaster") {
		t.Fatalf("run_agent granted: %s", got)
	}
	if got := run([]string{"fetch", "ask"}, agent); !strings.Contains(got, "refused:") {
		t.Fatalf("run_agent without its grant: %s", got)
	}
	pipe := "    print(run_pipeline('a topic'))\n"
	if got := run([]string{"run_pipeline"}, pipe); !strings.Contains(got, `pipeline "" ran on "a topic"`) {
		t.Fatalf("run_pipeline granted: %s", got)
	}
	if got := run([]string{"run_agent"}, pipe); !strings.Contains(got, "refused:") {
		t.Fatalf("run_pipeline on run_agent's grant: %s", got)
	}
}
