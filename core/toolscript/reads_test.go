package toolscript

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A tool that reads and writes is refused whole and accepted as a reading
// view: only its reading actions get through, with only their arguments, and
// a grouped tool's reading actions are the ones it declares as reading alone.
func TestAReadingViewLetsOnlyReadsThrough(t *testing.T) {
	g := NewGroupedTool("files_test", "Read and write files.")
	echo := func(args map[string]any, _ *ToolSession) (string, error) { return "ran " + args["action"].(string), nil }
	g.AddAction("cat", &GroupedToolAction{Description: "Read a file.", Params: map[string]ToolParam{"path": {Type: "string"}}, Caps: []Capability{CapRead}, Handler: echo})
	g.AddAction("write", &GroupedToolAction{Description: "Write a file.", Params: map[string]ToolParam{"path": {Type: "string"}, "content": {Type: "string"}}, Caps: []Capability{CapWrite}, Handler: echo})
	g.AddAction("peek", &GroupedToolAction{Description: "Read, asking first.", Caps: []Capability{CapRead}, NeedsConfirm: true, Handler: echo})
	g.AddAction("fetch", &GroupedToolAction{Description: "Read from the web.", Caps: []Capability{CapRead, CapNetwork}, Handler: echo})
	def := AgentToolDef{
		Tool: Tool{Name: "files_test", Description: "Read and write files. More advice.", Parameters: g.Params(), Caps: []Capability{CapRead, CapWrite, CapNetwork}},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return g.RunWithSession(args, nil)
		},
		NeedsConfirm: true,
	}
	if _, err := New(def); err == nil {
		t.Fatal("a tool that writes was accepted whole")
	}

	view, ok := GroupedReads(def, g)
	if !ok {
		t.Fatal("no reading view")
	}
	if got := view.Tool.Parameters["action"].Enum; strings.Join(got, ",") != "cat" {
		t.Errorf("reading actions %v, want only cat (write writes, peek asks, fetch reaches the network)", got)
	}
	if _, has := view.Tool.Parameters["content"]; has {
		t.Error("the view offers write's content argument")
	}
	if _, has := view.Tool.Parameters["path"]; !has {
		t.Error("the view dropped cat's path argument")
	}
	if out, err := view.Handler(context.Background(), map[string]any{"action": "cat", "path": "a"}); err != nil || out != "ran cat" {
		t.Errorf("cat through the view: %q %v", out, err)
	}
	for _, a := range []string{"write", "peek", ""} {
		if _, err := view.Handler(context.Background(), map[string]any{"action": a}); err == nil || !strings.Contains(err.Error(), "reading actions only") {
			t.Errorf("action %q through the view: %v", a, err)
		}
	}
	script, err := New(view)
	if err != nil {
		t.Fatalf("the reading view was refused: %v", err)
	}
	if !strings.Contains(script.Tool.Description, `call_tool("files_test", action: "cat"`) {
		t.Errorf("the script's description should show the view's signature:\n%s", script.Tool.Description)
	}

	// An action the mounted tool no longer offers (turned off for this
	// agent) stays out of the view.
	off := def
	off.Tool.Parameters = map[string]ToolParam{"action": {Type: "string", Enum: []string{"write"}}, "path": {Type: "string"}}
	if _, ok := GroupedReads(off, g); ok {
		t.Error("a reading action the tool no longer offers came back in the view")
	}

	named := Reads(def, ReadAction{Name: "cat", Params: []string{"path", "nope"}})
	if _, has := named.Tool.Parameters["nope"]; has {
		t.Error("a named argument the tool does not have was offered")
	}
}
