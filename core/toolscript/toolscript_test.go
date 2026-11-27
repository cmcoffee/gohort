package toolscript

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func readTool(name string, params map[string]ToolParam, required []string, fn func(args map[string]any) (string, error)) AgentToolDef {
	return AgentToolDef{
		Tool:    Tool{Name: name, Description: "Does " + name + ". More advice here.", Parameters: params, Required: required, Caps: []Capability{CapRead}},
		Handler: func(_ context.Context, args map[string]any) (string, error) { return fn(args) },
	}
}

// Only tools that read can be called from a script: one that asks first, or
// writes, runs or reaches out, is refused when the tool is built, naming it.
func TestOnlyToolsThatReadCanBeCalledFromAScript(t *testing.T) {
	ok := readTool("lookup", nil, nil, func(map[string]any) (string, error) { return "", nil })
	asks := ok
	asks.Tool.Name, asks.NeedsConfirm = "asks", true
	writes := ok
	writes.Tool.Name, writes.Tool.Caps = "writes", []Capability{CapRead, CapWrite}
	silent := ok
	silent.Tool.Name, silent.Tool.Caps = "silent", nil
	for _, bad := range []AgentToolDef{asks, writes, silent} {
		if _, err := New(ok, bad); err == nil || !strings.Contains(err.Error(), bad.Tool.Name) {
			t.Errorf("%s: %v", bad.Tool.Name, err)
		}
	}
	if _, err := New(); err == nil {
		t.Error("a tool_script with nothing to call was built")
	}
	def, err := New(ok)
	if err != nil || def.Tool.Name != Name || len(def.Tool.Caps) != 1 || def.Tool.Caps[0] != CapRead {
		t.Fatalf("%+v %v", def.Tool, err)
	}
}

// The model is shown each tool it may call with its arguments, required ones
// first and optional ones marked, in the same order every time.
func TestTheDescriptionListsEachToolsArguments(t *testing.T) {
	a := readTool("read_file", map[string]ToolParam{
		"path":       {Type: "string"},
		"start_line": {Type: "number"},
		"line_count": {Type: "number"},
	}, []string{"path"}, nil)
	g := readTool("git", map[string]ToolParam{"action": {Type: "string", Enum: []string{"status", "diff"}}, "path": {Type: "string"}}, []string{"action"}, nil)
	d1, d2 := description([]AgentToolDef{a, g}), description([]AgentToolDef{g, a})
	if d1 != d2 {
		t.Fatal("the description depends on the order the tools were given")
	}
	for _, want := range []string{
		`- call_tool("git", action: "status"|"diff", path?: string): Does git.`,
		`- call_tool("read_file", path: string, line_count?: number, start_line?: number): Does read_file.`,
		`out = call_tool("git", action=...)`,
	} {
		if !strings.Contains(d1, want) {
			t.Errorf("the description does not say %q:\n%s", want, d1)
		}
	}
	if strings.Contains(d1, "More advice") {
		t.Error("a tool's whole description went in; only its first sentence should")
	}
}

// scriptSession is a turn's session with a workspace the sandbox can see
// (not under the host's temp folder, which it hides).
func scriptSession(t *testing.T) (context.Context, *ToolSession) {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no cache directory for folders outside /tmp")
	}
	root, err := os.MkdirTemp(cache, "gohort-toolscript-test-")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	prev := WorkspacesDir()
	SetWorkspacesDir(filepath.Join(root, "ws"))
	t.Cleanup(func() { SetWorkspacesDir(prev) })
	ws, err := EnsureWorkspaceDir("u")
	if err != nil {
		t.Fatal(err)
	}
	sess := &ToolSession{Ctx: context.Background(), Username: "u", WorkspaceDir: ws}
	return sess.ContextWithSession(context.Background()), sess
}

// A chain in one call: the script searches, reads each file the search
// found, and prints a line from each. The tools run through their own
// handlers; a failed call raises in the script and is named after the
// output; the count of calls is given; and the script's folder is gone
// afterwards.
func TestAScriptChainsToolCalls(t *testing.T) {
	ctx, sess := scriptSession(t)
	files := map[string]string{"a.go": "package a\nfunc A() {}\n", "b.go": "package b\nfunc B() {}\n"}
	search := readTool("search_code", map[string]ToolParam{"pattern": {Type: "string"}}, []string{"pattern"}, func(args map[string]any) (string, error) {
		return "a.go:2:func A() {}\nb.go:2:func B() {}", nil
	})
	read := readTool("read_file", map[string]ToolParam{"path": {Type: "string"}}, []string{"path"}, func(args map[string]any) (string, error) {
		p, _ := args["path"].(string)
		body, ok := files[p]
		if !ok {
			return "", fmt.Errorf("no file %s", p)
		}
		return body, nil
	})
	def, err := New(search, read)
	if err != nil {
		t.Fatal(err)
	}
	code := `
hits = call_tool("search_code", pattern="func ")
for path in sorted({h.split(":")[0] for h in hits.splitlines()}):
    print(path, "->", call_tool("read_file", path=path).splitlines()[0])
try:
    call_tool("read_file", path="missing.go")
except Exception as e:
    print("missing:", "no file missing.go" in str(e))
try:
    call_tool("write_file", path="x")
except Exception as e:
    print("unknown:", "no tool named" in str(e))
`
	out, err := def.Handler(ctx, map[string]any{"code": code})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "sandbox") && !strings.Contains(out, "a.go ->") {
		t.Skipf("no sandbox here: %.300s", out)
	}
	for _, want := range []string{
		"a.go -> package a", "b.go -> package b", "missing: True", "unknown: True",
		"(4 tool calls: read_file x3, search_code x1.)", "Failed: read_file: no file missing.go",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the result does not say %q:\n%s", want, out)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(sess.WorkspaceDir, ".tool_script-*")); len(left) != 0 {
		t.Errorf("the script's folder was left behind: %v", left)
	}
}

// A script stops getting tool calls at the limit; a traceback names
// script.py and its line; and the network is closed, so a listener on the
// host's loopback cannot be reached from inside.
func TestAScriptIsHeldToItsLimits(t *testing.T) {
	ctx, _ := scriptSession(t)
	n := 0
	ping := readTool("ping", nil, nil, func(map[string]any) (string, error) { n++; return "pong", nil })
	def, err := New(ping)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	port := l.Addr().(*net.TCPAddr).Port
	code := fmt.Sprintf(`
import socket
refused = 0
for i in range(%d):
    try:
        call_tool("ping")
    except Exception as e:
        refused += 1
print("refused", refused)
try:
    socket.create_connection(("127.0.0.1", %d), timeout=3).close()
    print("network: open")
except OSError:
    print("network: closed")
raise ValueError("boom")
`, MaxCalls+5, port)
	out, err := def.Handler(ctx, map[string]any{"code": code})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "sandbox") && !strings.Contains(out, "refused") {
		t.Skipf("no sandbox here: %.300s", out)
	}
	for _, want := range []string{"refused 5", "network: closed", `File "script.py", line 15`, "ValueError: boom", "The script failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("the result does not say %q:\n%s", want, out)
		}
	}
	if n != MaxCalls {
		t.Errorf("ping ran %d times, limit %d", n, MaxCalls)
	}
}

// A call outside a conversation, or with no code, is refused saying why.
func TestAScriptNeedsAConversationAndCode(t *testing.T) {
	def, _ := New(readTool("ping", nil, nil, func(map[string]any) (string, error) { return "", nil }))
	if _, err := def.Handler(context.Background(), map[string]any{"code": "print(1)"}); err == nil || !strings.Contains(err.Error(), "conversation") {
		t.Errorf("no session: %v", err)
	}
	ctx, _ := scriptSession(t)
	if _, err := def.Handler(ctx, map[string]any{"code": "  "}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("no code: %v", err)
	}
}
