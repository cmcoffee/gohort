package filestore

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func mappingFixture(t *testing.T) (*FileStoreApp, Store, StoreCommand) {
	t.Helper()
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	return app,
		Store{Slug: "bundles", Name: "Support bundles"},
		StoreCommand{Slug: "bundles", Name: "decrypt", Label: "Decrypt bundle", Command: "/opt/bin/cap"}
}

// What mapping produces lands on the command it came from. That round trip is
// the whole correction: the reverted arrangement wrote loose tools into the
// global tool list, where they read as orphaned until someone found a different
// expander and bound them.
func TestAMappedToolboxLandsOnItsCommand(t *testing.T) {
	app, st, cmd := mappingFixture(t)

	msg, err := app.proposeToolbox(st, cmd, map[string]any{
		"name": "capture_tools", "description": "What the capture binary does.",
		"actions": `[{"name":"unpack","description":"Unpack a capture.",
		             "command_template":"/opt/bin/cap unpack {folder}",
		             "params":{"folder":{"type":"string","description":"subfolder"}},
		             "required":["folder"]}]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	tb, ok := LoadToolbox(app.DB, "capture_tools")
	if !ok {
		t.Fatal("the toolbox was not stored")
	}
	if tb.FromStore != "bundles" || tb.FromCommand != "decrypt" {
		t.Errorf("provenance is what puts it on the command's row: %+v", tb)
	}
	if got := ToolboxesMappedFrom(app.DB, "bundles"); len(got) != 1 {
		t.Errorf("the command's folder must list it: %+v", got)
	}

	// The agent must be told its work is inert, or it will report success to a
	// person who then waits for something to happen.
	if !strings.Contains(msg, "cannot be called yet") || !strings.Contains(msg, "attach") {
		t.Errorf("the reply must say it is inert and what makes it live: %q", msg)
	}
}

// Re-mapping the same command is the fix path — the first --help rarely says
// everything — so it must replace rather than collide.
func TestReMappingReplacesTheProposal(t *testing.T) {
	app, st, cmd := mappingFixture(t)
	mk := func(actions string) error {
		_, err := app.proposeToolbox(st, cmd, map[string]any{
			"name": "capture_tools", "description": "d", "actions": actions})
		return err
	}
	if err := mk(`[{"name":"unpack","command_template":"/opt/bin/cap unpack"}]`); err != nil {
		t.Fatal(err)
	}
	if err := mk(`[{"name":"unpack","command_template":"/opt/bin/cap unpack"},
	               {"name":"verify","command_template":"/opt/bin/cap verify"}]`); err != nil {
		t.Fatalf("re-mapping the same command must be allowed: %v", err)
	}
	tb, _ := LoadToolbox(app.DB, "capture_tools")
	if len(tb.Tool.Actions) != 2 {
		t.Errorf("the correction must replace the proposal, got %d actions", len(tb.Tool.Actions))
	}
}

// A placeholder with no parameter behind it produces an action that fails the
// first time it is called, and nobody finds out until then.
func TestProposalRejectsAnUndeclaredPlaceholder(t *testing.T) {
	app, st, cmd := mappingFixture(t)
	_, err := app.proposeToolbox(st, cmd, map[string]any{
		"name": "capture_tools", "description": "d",
		"actions": `[{"name":"unpack","command_template":"/opt/bin/cap unpack {folder} --key {key}",
		              "params":{"folder":{"type":"string"}}}]`,
	})
	if err == nil || !strings.Contains(err.Error(), "key") {
		t.Fatalf("the refusal must name the offending placeholder: %v", err)
	}
	if _, ok := LoadToolbox(app.DB, "capture_tools"); ok {
		t.Error("a refused proposal must not have been stored")
	}
}

// Malformed input from a model is expected, not exceptional: it has to come back
// as a sentence the agent can act on rather than as a stored half-toolbox.
func TestProposalHandlesBadActions(t *testing.T) {
	app, st, cmd := mappingFixture(t)
	for name, actions := range map[string]string{
		"not json":     `unpack the capture`,
		"not an array": `{"name":"unpack"}`,
		"empty":        ``,
	} {
		if _, err := app.proposeToolbox(st, cmd, map[string]any{
			"name": "t", "description": "d", "actions": actions}); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// The conversation is opened from a command and carries it. Without the command
// in the URL the agent would be mapping "some command on this folder", which is
// the ambiguity the whole arrangement exists to remove.
func TestMapConversationIsScopedToOneCommand(t *testing.T) {
	raw, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	if !strings.Contains(src, `"/filestore/api/map/chat/send?slug={slug}&command={name}"`) {
		t.Error("the mapping conversation must name both the folder and the command it was opened on")
	}
	if !strings.Contains(src, `ui.Expand("Map"`) {
		t.Error("Map belongs on the command's own row — that is what makes the result land where it came from")
	}
}
