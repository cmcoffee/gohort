package filestore

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A minted tool is a property of the FOLDER, not of whoever mapped it. Per-user
// would mean every user of a shared store re-mapping the same binary, and an
// admin's binding resolving to nothing for everyone else.
func TestMintedToolsAreGlobalToTheStore(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	app := &FileStoreApp{}
	app.DB = db

	saved, err := SaveStoreTool(db, "bundles", TempTool{
		Name: "unpack_capture", Description: "Unpack a sealed capture.",
		CommandTemplate: "/opt/bin/cap unpack {folder}",
		Params:          map[string]ToolParam{"folder": {Type: "string"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Bound-only is not optional. A tool minted for one folder of captures in
	// every chat the user has is the thing the bundle exists to prevent.
	if !saved.BoundOnly {
		t.Error("a minted tool must be bound-only, or it leaks into every catalog")
	}
	if got := StoreTools(db, "bundles"); len(got) != 1 || got[0].Name != "unpack_capture" {
		t.Fatalf("the store must hold its own tool: %+v", got)
	}
	if got := StoreTools(db, "other"); len(got) != 0 {
		t.Errorf("another store must not see it: %+v", got)
	}
}

// Minting writes a record; it does not grant anything. The agent cannot bind
// its own proposal, so what it produces is a suggestion a person accepts.
func TestAProposalIsInertUntilBound(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	app := &FileStoreApp{}
	app.DB = db
	st := Store{Slug: "bundles", Name: "Support bundles"}

	msg, err := app.proposeTool(st, map[string]any{
		"name": "unpack_capture", "description": "Unpack a sealed capture.",
		"command":    "/opt/bin/cap unpack {folder}",
		"parameters": `{"folder":{"type":"string","description":"the subfolder"}}`,
		"required":   "folder",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "cannot be called yet") {
		t.Errorf("the agent must be told its proposal is inert, or it will report success to the user: %q", msg)
	}
	// The store's Toolset is what makes a tool reachable, and proposing does not
	// touch it.
	if len(st.Toolset) != 0 {
		t.Error("proposing must not bind — binding is the human act that approves it")
	}
}

// A placeholder with no parameter behind it produces a tool that fails the
// first time it is called, and nobody finds out until then.
func TestProposalRejectsAnUndeclaredPlaceholder(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	app := &FileStoreApp{}
	app.DB = db

	_, err := app.proposeTool(Store{Slug: "bundles"}, map[string]any{
		"name": "unpack", "description": "d", "command": "/opt/bin/cap unpack {folder} --key {key}",
		"parameters": `{"folder":{"type":"string"}}`,
	})
	if err == nil {
		t.Fatal("a template using {key} with no key parameter must be refused")
	}
	if !strings.Contains(err.Error(), "key") {
		t.Errorf("the refusal must name the offending placeholder: %v", err)
	}
}

// The fields a tool cannot be useful without.
func TestSaveStoreToolInsistsOnWhatWillBeRead(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	for _, c := range []struct {
		name string
		in   TempTool
	}{
		{"no name", TempTool{Description: "d", CommandTemplate: "x"}},
		{"no description", TempTool{Name: "t", CommandTemplate: "x"}},
		{"no command", TempTool{Name: "t", Description: "d"}},
	} {
		if _, err := SaveStoreTool(db, "bundles", c.in); err == nil {
			t.Errorf("%s: must be refused", c.name)
		}
	}
	if _, err := SaveStoreTool(db, "", TempTool{Name: "t", Description: "d", CommandTemplate: "x"}); err == nil {
		t.Error("a tool with no store is bound to nothing")
	}
}

func TestTemplatePlaceholders(t *testing.T) {
	got := templatePlaceholders("/opt/bin/cap {verb} {folder} --out {dest}")
	want := []string{"verb", "folder", "dest"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
		}
	}
	// An unterminated brace must not hang or panic — it is a typo, not input.
	if p := templatePlaceholders("/opt/bin/cap {folder"); len(p) != 0 {
		t.Errorf("unterminated placeholder yields nothing, got %v", p)
	}
}
