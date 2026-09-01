package filestore

import (
	"os"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func mappedToolbox(name, store, cmd string) StoreToolbox {
	return StoreToolbox{
		Tool: TempTool{
			Name: name, Description: "What the capture binary can do.",
			Actions: []TempToolAction{
				{Name: "unpack", Description: "Unpack a capture.", CommandTemplate: "/opt/bin/cap unpack {folder}"},
				{Name: "verify", Description: "Verify a capture.", CommandTemplate: "/opt/bin/cap verify {folder}"},
			},
		},
		FromStore: store, FromCommand: cmd,
	}
}

// The correction to the reverted arrangement: a toolbox is stored ONCE and
// attached to the folders it runs in. A second folder of the same kind costs an
// attachment, not a re-mapping.
func TestOneToolboxServesManyFolders(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	if _, err := SaveToolbox(db, mappedToolbox("capture_tools", "bundles", "decrypt")); err != nil {
		t.Fatal(err)
	}
	if got := ListToolboxes(db); len(got) != 1 {
		t.Fatalf("stored once, got %d", len(got))
	}
	// Two folders naming it is two attachments over one record.
	a := Store{Slug: "bundles", Toolboxes: []string{"capture_tools"}}
	b := Store{Slug: "archive", Toolboxes: []string{"capture_tools"}}
	for _, st := range []Store{a, b} {
		if len(st.Toolboxes) != 1 {
			t.Fatalf("%s should reference the one toolbox", st.Slug)
		}
	}
	// And it still knows where it came from, which is what puts it on the row of
	// the command it maps rather than adrift in a list.
	tb, _ := LoadToolbox(db, "capture_tools")
	if tb.FromStore != "bundles" || tb.FromCommand != "decrypt" {
		t.Errorf("provenance lost: %+v", tb)
	}
	if got := ToolboxesMappedFrom(db, "bundles"); len(got) != 1 {
		t.Errorf("the origin folder must be able to list what was mapped from it: %+v", got)
	}
	if got := ToolboxesMappedFrom(db, "archive"); len(got) != 0 {
		t.Error("attaching a toolbox does not make the folder its origin")
	}
}

// Bound-only is not optional. The reason a folder's bundle can be narrow is
// that its members opt out of the global catalog.
func TestAMappedToolboxNeverJoinsTheGlobalCatalog(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved, err := SaveToolbox(db, mappedToolbox("capture_tools", "bundles", "decrypt"))
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Tool.BoundOnly {
		t.Error("a mapped toolbox must be bound-only, or it turns up in every chat")
	}
	if saved.Tool.Mode != TempToolModeToolbox {
		t.Errorf("mode = %q, want toolbox", saved.Tool.Mode)
	}
}

// Two binaries answering to one name would make an attachment mean something
// other than what the admin who made it read.
func TestANameIsNotSilentlyReassigned(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	first, err := SaveToolbox(db, mappedToolbox("capture_tools", "bundles", "decrypt"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = SaveToolbox(db, mappedToolbox("capture_tools", "archive", "unseal"))
	if err == nil {
		t.Fatal("a different command taking a name already in use must be refused")
	}
	if !strings.Contains(err.Error(), "decrypt") || !strings.Contains(err.Error(), "bundles") {
		t.Errorf("the refusal must say what already holds the name: %v", err)
	}

	// Re-mapping the SAME command is not a collision — it is the fix path — and
	// it keeps the original date rather than presenting as newly created.
	first.Created = time.Now().Add(-72 * time.Hour)
	db.Set(toolboxesTable, first.Tool.Name, first)
	again := mappedToolbox("capture_tools", "bundles", "decrypt")
	saved, err := SaveToolbox(db, again)
	if err != nil {
		t.Fatalf("re-mapping the same command must be allowed: %v", err)
	}
	if !saved.Created.Equal(first.Created) {
		t.Error("a re-map keeps the original date; it is the same toolbox, corrected")
	}
}

// What a toolbox cannot be useful without, refused where an admin can still see
// why rather than at the first call.
func TestSaveToolboxInsistsOnWhatWillBeRun(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	good := mappedToolbox("t", "s", "c")

	noName := good
	noName.Tool.Name = ""
	noActions := good
	noActions.Tool.Actions = nil
	noDesc := good
	noDesc.Tool.Description = ""
	for name, in := range map[string]StoreToolbox{
		"no name": noName, "no actions": noActions, "no description": noDesc,
	} {
		if _, err := SaveToolbox(db, in); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}

	noCmd := mappedToolbox("t2", "s", "c")
	noCmd.Tool.Actions[1].CommandTemplate = ""
	if _, err := SaveToolbox(db, noCmd); err == nil || !strings.Contains(err.Error(), "verify") {
		t.Errorf("an action with nothing to run must be named: %v", err)
	}

	// A mapped command is local. An HTTP action here is a mistake worth catching
	// before it reads as a network failure at the first call.
	asHTTP := mappedToolbox("t3", "s", "c")
	asHTTP.Tool.Actions[0].URLTemplate = "https://example.test/"
	if _, err := SaveToolbox(db, asHTTP); err == nil || !strings.Contains(err.Error(), "local") {
		t.Errorf("an http action on a mapped command must be refused: %v", err)
	}
}

// An attachment naming a toolbox that has since been deleted is skipped, not
// fatal — taking a working folder down because one entry went stale is worse
// than the stale entry.
func TestAStaleAttachmentIsSkipped(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	app := &FileStoreApp{}
	app.DB = db
	if _, err := SaveToolbox(db, mappedToolbox("capture_tools", "bundles", "decrypt")); err != nil {
		t.Fatal(err)
	}
	s := storeSource{app: app}
	st := Store{Slug: "bundles", Toolboxes: []string{"capture_tools", "deleted_one", "", "capture_tools"}}

	defs := s.attachedToolboxDefs(&ToolSession{Username: "u"}, "u", st)
	// One toolbox: the missing name skipped, the blank ignored, the duplicate
	// not handed over twice.
	if len(defs) != 1 {
		t.Fatalf("expected the one live toolbox, got %d: %+v", len(defs), defs)
	}
	if defs[0].Tool.Name != "capture_tools" {
		t.Errorf("wrong tool: %q", defs[0].Tool.Name)
	}

	if got := s.attachedToolboxDefs(&ToolSession{Username: "u"}, "u", Store{Slug: "none"}); len(got) != 0 {
		t.Errorf("a folder with no attachments contributes nothing, got %d", len(got))
	}
}

// The folder panel is one expander over four subjects. What makes it work is
// that every part addresses the SAME folder — a source that forgot {slug} would
// silently show another folder's commands, which is the failure the old layout
// had in a different form.
func TestFolderPanelIsScopedToItsFolder(t *testing.T) {
	raw, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)

	// One "Manage" expander, not four separate ones. The four were what let a
	// mapped toolbox land somewhere with no visible relationship to the folder.
	for _, gone := range []string{`ui.Expand("Edit"`, `ui.Expand("Add command"`, `ui.Expand("Assigned to"`} {
		if strings.Contains(src, gone) {
			t.Errorf("%s is still a separate row action; the folder panel replaced it", gone)
		}
	}
	if !strings.Contains(src, `ui.Expand("Manage"`) {
		t.Fatal("the folder panel is gone")
	}

	// Every source inside the panel carries the folder.
	for _, want := range []string{
		`"/filestore/api/commands?slug={slug}"`,
		`"/filestore/api/toolboxes?slug={slug}"`,
		`"/filestore/api/stores?slug={slug}"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("panel source %s is missing or unscoped — an unscoped one shows another folder's rows", want)
		}
	}
	// Detach has to name both the folder and the toolbox: without the name it
	// would clear the wrong row, and without the slug the wrong folder.
	if !strings.Contains(src, `"/filestore/api/toolboxes?slug={slug}&name={name}"`) {
		t.Error("detach must name both the folder and the toolbox")
	}
	// The command row shows what mapping produced. That column is what makes an
	// orphan impossible: a toolbox is always visible from its own command.
	if !strings.Contains(src, `{Field: "mapped"`) {
		t.Error("the commands table must show mapped state, or a toolbox can exist with nothing on screen explaining it")
	}
}
