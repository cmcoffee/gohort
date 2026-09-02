package filestore

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A mapping conversation is real work. Coming up to find the folder empty with
// no error is how an operator rebuilds by hand what was already there.
func TestAnExistingMappingSurvivesTheCollapse(t *testing.T) {
	app, st, cmd := commandFixture(t)
	// A deployment as it stood: the mapping in its own table, the approval
	// expressed as an attachment on the folder.
	app.DB.Set(legacyToolboxesTable, "capture_tools", legacyToolbox{
		Tool: TempTool{
			Name: "capture_tools", Description: "What the capture binary can do.",
			Actions: []TempToolAction{
				{Name: "unpack", CommandTemplate: "/opt/bin/cap unpack {folder}"},
			},
		},
		FromStore: st.Slug, FromCommand: cmd.Name,
	})
	st.Toolboxes = []string{"capture_tools"}
	app.DB.Set(storesTable, st.Slug, st)

	if moved := MigrateToolboxesOntoCommands(app.DB); moved != 1 {
		t.Fatalf("moved %d, want 1", moved)
	}
	got, ok := LoadStoreCommand(app.DB, st.Slug, cmd.Name)
	if !ok || len(got.Tools) != 1 || got.ToolDesc == "" {
		t.Fatalf("the mapping did not land on its command: %+v", got)
	}
	// The attachment WAS the approval. An admin who approved something should
	// not have to approve it again under a new name.
	if !got.Approved {
		t.Error("an attached toolbox must come across approved")
	}
	// Both halves of the old arrangement are emptied, so a second boot has
	// nothing to redo and nothing points at what is gone.
	if len(app.DB.Keys(legacyToolboxesTable)) != 0 {
		t.Error("the old table still has records")
	}
	after, _ := LoadStore(app.DB, st.Slug)
	if len(after.Toolboxes) != 0 {
		t.Error("the attachment list still names something")
	}
	if moved := MigrateToolboxesOntoCommands(app.DB); moved != 0 {
		t.Errorf("a second run moved %d — the migration must be idempotent", moved)
	}
}

// Mapped but attached nowhere was mapped and never approved. Carrying it across
// switched ON would grant, in a migration, something nobody granted.
func TestAnUnattachedToolboxArrivesUnapproved(t *testing.T) {
	app, st, cmd := commandFixture(t)
	app.DB.Set(legacyToolboxesTable, "capture_tools", legacyToolbox{
		Tool: TempTool{
			Name: "capture_tools", Description: "d",
			Actions: []TempToolAction{{Name: "unpack", CommandTemplate: "/opt/bin/cap unpack"}},
		},
		FromStore: st.Slug, FromCommand: cmd.Name,
	})
	MigrateToolboxesOntoCommands(app.DB)

	got, _ := LoadStoreCommand(app.DB, st.Slug, cmd.Name)
	if !got.Mapped() {
		t.Fatal("the mapping was lost")
	}
	if got.Approved {
		t.Error("a toolbox attached to nothing must not arrive approved")
	}
}

// A toolbox whose command was deleted has nothing left to be part of. It is
// dropped rather than kept as a record no interface can reach.
func TestAToolboxWithNoCommandIsDropped(t *testing.T) {
	app, st, _ := commandFixture(t)
	app.DB.Set(legacyToolboxesTable, "orphan", legacyToolbox{
		Tool:      TempTool{Name: "orphan", Description: "d"},
		FromStore: st.Slug, FromCommand: "long_gone",
	})
	if moved := MigrateToolboxesOntoCommands(app.DB); moved != 0 {
		t.Errorf("moved %d, want none", moved)
	}
	if len(app.DB.Keys(legacyToolboxesTable)) != 0 {
		t.Error("the old record must still be cleared")
	}
}
