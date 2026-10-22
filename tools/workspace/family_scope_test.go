package workspace

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A workspace an agent family makes is that family's: another family's agent
// cannot switch into it, read its details, delete it, or see it listed. One a
// person made, or one made before families were recorded, stays open to all of
// the owner's agents. Between families nothing is shared; switching in by id
// was the way around that.
func TestAWorkspaceBelongsToTheFamilyThatMadeIt(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })
	prevDir := WorkspacesDir()
	SetWorkspacesDir(t.TempDir())
	t.Cleanup(func() { SetWorkspacesDir(prevDir) })
	prevFam := WorkspaceFamilyOf
	WorkspaceFamilyOf = nil // each agent its own family
	t.Cleanup(func() { WorkspaceFamilyOf = prevFam })

	maker := &ToolSession{Username: "u", AgentID: "researcher"}
	out, err := handleCreate(map[string]any{"name": "drafts"}, maker)
	if err != nil {
		t.Fatal(err)
	}
	id := out[strings.Index(out, "id=")+3 : strings.Index(out, ",")]

	other := &ToolSession{Username: "u", AgentID: "payroll"}
	for name, call := range map[string]func() (string, error){
		"use":    func() (string, error) { return handleUse(map[string]any{"id": id}, other) },
		"info":   func() (string, error) { return handleInfo(map[string]any{"id": id}, other) },
		"delete": func() (string, error) { return handleDelete(map[string]any{"id": id}, other) },
	} {
		if _, err := call(); err == nil {
			t.Errorf("another family's agent could %s it", name)
		}
	}
	if list, _ := handleList(nil, other); strings.Contains(list, id) {
		t.Errorf("another family's agent sees it listed: %s", list)
	}
	if _, err := handleUse(map[string]any{"id": id}, maker); err != nil {
		t.Errorf("its own family must still use it: %v", err)
	}

	// A person's workspace (no family) stays open to every agent.
	open, _, err := CreateManagedWorkspace("project", "u", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handleUse(map[string]any{"id": open.ID}, other); err != nil {
		t.Errorf("a workspace with no family is open to the owner's agents: %v", err)
	}
}
