package anvil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// tool_script is offered beside the read tools, in plan mode and on a
// read-only project too since it only reads, and left out for a measure run
// without it. A script searches the project and reads what the search found
// through Anvil's own tools, in one call.
func TestToolScriptReadsThroughAnvilsTools(t *testing.T) {
	c, _ := checkCtx(t, Project{Kind: KindClone})
	for _, mode := range []struct {
		name string
		set  func(*toolCtx)
		want bool
	}{
		{"default", func(*toolCtx) {}, true},
		{"plan mode", func(c *toolCtx) { c.planning = true }, true},
		{"read-only", func(c *toolCtx) { c.readOnly = true }, true},
		{"measure without it", func(c *toolCtx) { c.noScript = true }, false},
	} {
		cc, _ := checkCtx(t, Project{Kind: KindClone})
		mode.set(cc)
		if _, ok := toolNamed(t, cc, "tool_script"); ok != mode.want {
			t.Errorf("%s: tool_script offered = %v", mode.name, ok)
		}
	}

	mustWrite(t, filepath.Join(c.base, "a.go"), "package a\n\nfunc Alpha() {}\n")
	mustWrite(t, filepath.Join(c.base, "b.go"), "package b\n\nfunc Beta() {}\n")
	script, _ := toolNamed(t, c, "tool_script")

	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no cache directory for folders outside /tmp")
	}
	root, err := os.MkdirTemp(cache, "gohort-anvil-script-")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	prev := WorkspacesDir()
	SetWorkspacesDir(filepath.Join(root, "ws"))
	t.Cleanup(func() { SetWorkspacesDir(prev) })
	sess := &ToolSession{Ctx: context.Background(), Username: "u"}
	code := `
hits = call_tool("search_code", pattern="^func ")
for path in sorted({h.split(":")[0] for h in hits.splitlines() if ":" in h}):
    body = call_tool("read_file", path=path)
    print(path, [l for l in body.splitlines() if "func " in l])
`
	out, err := script.Handler(sess.ContextWithSession(context.Background()), map[string]any{"code": code})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "sandbox") && !strings.Contains(out, "a.go") {
		t.Skipf("no sandbox here: %.300s", out)
	}
	for _, want := range []string{"a.go", "Alpha", "b.go", "Beta", "(3 tool calls: read_file x2, search_code x1.)"} {
		if !strings.Contains(out, want) {
			t.Errorf("the result does not say %q:\n%s", want, out)
		}
	}
}
