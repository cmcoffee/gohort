package systemhook

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/flows"
	"github.com/cmcoffee/snugforge/kvlite"
)

// setup gives the tool a root store, a work folder outside /tmp for flows,
// and a session workspace.
func setup(t *testing.T) (*ToolSession, string) {
	t.Helper()
	prev := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prev })
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no folder outside /tmp")
	}
	root, err := os.MkdirTemp(cache, "gohort-systemhook-test-")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	flows.SetWorkBase(filepath.Join(root, "data", "workspaces", "_flows"))
	t.Cleanup(func() { flows.SetWorkBase("") })
	ws := filepath.Join(root, "ws")
	os.MkdirAll(ws, 0o700)
	return &ToolSession{WorkspaceDir: ws}, ws
}

func needSandbox(t *testing.T) {
	t.Helper()
	a := flows.Adapted{Takes: []string{"text/plain"}, Becomes: flows.KindText, Command: "cat {in}"}
	if _, err := flows.RunAdapted(context.Background(), "probe", a, flows.Input{Data: []byte("x"), From: "text/plain"}); err != nil && strings.Contains(err.Error(), "sandbox") {
		t.Skipf("no sandbox here: %v", err)
	}
}

// added saves and turns on a format an admin added.
func added(t *testing.T, name string, a flows.Adapted) {
	t.Helper()
	spec, _ := json.Marshal(a)
	RootDB.Set("connectors", name, Connector{Name: name, Kind: flows.ConnectorKind, Spec: spec, Approved: true})
	if err := flows.SetAdapted(name, a); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { flows.RemoveAdapted(name) })
}

// shout is an added format: text in, the same text in capitals out.
var shout = flows.Adapted{Takes: []string{flows.Any}, Becomes: flows.KindText, Command: `sh -c 'tr a-z A-Z < "$0"' {in}`}

// Only the formats an admin added are offered, every one that is on, to every
// agent: there is no switch per format or agent. The tool is in no catalog
// until one is on, and gohort's own jobs are not offered: agents reach those
// through gohort's own tools.
func TestOnlyAddedFormatsAreOffered(t *testing.T) {
	sess, _ := setup(t)
	if _, params := (&HookTool{}).SchemaWithSession(sess); params != nil || ChatToolAvailable(&HookTool{}, sess) || Available(true) {
		t.Fatal("gohort's own jobs should not be offered")
	}
	added(t, "shout", shout)
	desc, params := (&HookTool{}).SchemaWithSession(sess)
	if params == nil || !strings.Contains(desc, "shout") || len(params["action"].Enum) != 1 || !ChatToolAvailable(&HookTool{}, sess) || !Available(false) {
		t.Fatalf("the added format should be the one offered: %q %+v", desc, params["action"])
	}
	if caps := (&HookTool{}).ActionCaps(); hasCap(caps["shout"], CapNetwork) {
		t.Error("an added format does not go online")
	}
	if !IsFrameworkTool(&HookTool{}) {
		t.Error("convert_file is every agent's: it belongs in no picker")
	}
	var c Connector
	RootDB.Get("connectors", "shout", &c)
	c.Approved = false
	RootDB.Set("connectors", "shout", c)
	if _, params := (&HookTool{}).SchemaWithSession(sess); params != nil || Available(true) {
		t.Error("turned off, the tool should be gone")
	}
}

// An agent runs an added format on a workspace file: the result is saved
// beside it, never over a file that is there, and text comes back inline.
func TestAnAgentRunsAnAddedFormat(t *testing.T) {
	needSandbox(t)
	sess, ws := setup(t)
	added(t, "shout", shout)
	os.WriteFile(filepath.Join(ws, "notes.txt"), []byte("hello there"), 0o600)
	out, err := (&HookTool{}).RunWithSession(map[string]any{"action": "shout", "path": "notes.txt"}, sess)
	if err != nil || !strings.Contains(out, "notes-2.txt") || !strings.Contains(out, "HELLO THERE") {
		t.Fatalf("want the result beside the source, not over it, and inline: %v %q", err, out)
	}
	if got, _ := os.ReadFile(filepath.Join(ws, "notes.txt")); string(got) != "hello there" {
		t.Error("the source should be untouched")
	}
	out, _ = (&HookTool{}).RunWithSession(map[string]any{"action": "shout", "path": "notes.txt"}, sess)
	if !strings.Contains(out, "notes-3.txt") {
		t.Errorf("a second run should not overwrite the first: %q", out)
	}
}

// A hook that is not offered, a path out of the workspace, or a missing
// file are refused.
func TestTheToolRefusesWhatItShould(t *testing.T) {
	sess, ws := setup(t)
	added(t, "shout", shout)
	os.WriteFile(filepath.Join(ws, "a.txt"), []byte("x"), 0o600)
	for name, args := range map[string]map[string]any{
		"not offered":   {"action": "read.pdf", "path": "a.txt"},
		"out of bounds": {"action": "shout", "path": "../a.txt"},
		"absolute":      {"action": "shout", "path": "/etc/passwd"},
		"missing":       {"action": "shout", "path": "nope.txt"},
	} {
		if _, err := (&HookTool{}).RunWithSession(args, sess); err == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
	// A real file just outside the workspace: refused for where it is, not
	// for not being there.
	os.WriteFile(filepath.Join(filepath.Dir(ws), "outside.txt"), []byte("secret"), 0o600)
	if _, err := (&HookTool{}).RunWithSession(map[string]any{"action": "shout", "path": "../outside.txt"}, sess); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("a file outside the workspace should be refused as such: %v", err)
	}
}

// An added export format: a Markdown document from the workspace written as
// EPUB.
func TestAnAddedExportIsOffered(t *testing.T) {
	needSandbox(t)
	if !flows.Installed("pandoc") {
		t.Skip("pandoc is not installed here")
	}
	sess, ws := setup(t)
	added(t, "epub-pandoc", flows.Adapted{Takes: []string{"text/markdown"}, Gives: "epub", Command: "pandoc -f markdown {in} -o {out}"})
	if desc, _ := (&HookTool{}).SchemaWithSession(sess); !strings.Contains(desc, "epub-pandoc") || !strings.Contains(desc, "EPUB") {
		t.Fatalf("the added export should be listed: %q", desc)
	}
	os.WriteFile(filepath.Join(ws, "notes.md"), []byte("# Notes\n\nSome **text**."), 0o600)
	out, err := (&HookTool{}).RunWithSession(map[string]any{"action": "epub-pandoc", "path": "notes.md"}, sess)
	if err != nil || !strings.Contains(out, "notes.epub") {
		t.Fatalf("want the document as EPUB in the workspace: %v %q", err, out)
	}
}

func hasCap(caps []Capability, c Capability) bool {
	for _, x := range caps {
		if x == c {
			return true
		}
	}
	return false
}

// The tool is named for what it does.
func TestTheToolIsConvertFile(t *testing.T) {
	if (&HookTool{}).Name() != "convert_file" {
		t.Errorf("name = %q", (&HookTool{}).Name())
	}
}
