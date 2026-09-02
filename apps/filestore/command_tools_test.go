package filestore

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// commandFixture is a folder with one command registered against it.
func commandFixture(t *testing.T) (*FileStoreApp, Store, StoreCommand) {
	t.Helper()
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	st, err := SaveStore(app.DB, Store{Name: "Support bundles", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := SaveStoreCommand(app.DB, StoreCommand{
		Slug: st.Slug, Name: "decrypt", Label: "Decrypt bundle", Command: "/opt/bin/cap"})
	if err != nil {
		t.Fatal(err)
	}
	return app, st, cmd
}

func mapIt(t *testing.T, app *FileStoreApp, st Store, cmd StoreCommand) StoreCommand {
	t.Helper()
	saved, err := SaveCommandTools(app.DB, st.Slug, cmd.Name, "What the capture binary can do.", []TempToolAction{
		{Name: "unpack", Description: "Unpack a capture.", CommandTemplate: "/opt/bin/cap unpack {folder}"},
		{Name: "verify", Description: "Verify a capture.", CommandTemplate: "/opt/bin/cap verify {folder}"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

// One record. The mapping is fields on the command, so there is no state in
// which a mapping exists apart from the command it describes — which is the
// whole reason the toolbox table went away.
func TestAMappingLivesOnItsCommand(t *testing.T) {
	app, st, cmd := commandFixture(t)
	mapIt(t, app, st, cmd)

	got, ok := LoadStoreCommand(app.DB, st.Slug, "decrypt")
	if !ok || len(got.Tools) != 2 {
		t.Fatalf("the mapping must be on the command: %+v", got)
	}
	if got.ToolDesc == "" {
		t.Error("the description is what an agent reads before opening it")
	}
	// Deleting the command takes its tools with it. Nothing to orphan.
	DeleteStoreCommand(app.DB, st.Slug, "decrypt")
	if _, ok := LoadStoreCommand(app.DB, st.Slug, "decrypt"); ok {
		t.Fatal("the command survived its own delete")
	}
	s := storeSource{app: app}
	if defs := s.commandToolDefs(&ToolSession{Username: "u"}, "u", st); len(defs) != 0 {
		t.Errorf("a deleted command still hands out %d tool(s)", len(defs))
	}
}

// A mapping is a proposal, and a proposal is not an approval. What a model
// wrote about a binary it probed cannot become a live capability by being
// written down.
func TestAMappingIsInertUntilApproved(t *testing.T) {
	app, st, cmd := commandFixture(t)
	saved := mapIt(t, app, st, cmd)
	if saved.Approved {
		t.Fatal("mapping must never approve")
	}
	s := storeSource{app: app}
	if defs := s.commandToolDefs(&ToolSession{Username: "u"}, "u", st); len(defs) != 0 {
		t.Fatalf("an unapproved command handed out %d tool(s)", len(defs))
	}

	if _, err := SetCommandApproved(app.DB, st.Slug, "decrypt", true); err != nil {
		t.Fatal(err)
	}
	defs := s.commandToolDefs(&ToolSession{Username: "u"}, "u", st)
	if len(defs) == 0 {
		t.Fatal("an approved command hands out nothing")
	}
	// Switching it off keeps the mapping and takes the capability away.
	if _, err := SetCommandApproved(app.DB, st.Slug, "decrypt", false); err != nil {
		t.Fatal(err)
	}
	after, _ := LoadStoreCommand(app.DB, st.Slug, "decrypt")
	if len(after.Tools) != 2 {
		t.Error("withdrawing approval must not throw away the mapping")
	}
	if defs := s.commandToolDefs(&ToolSession{Username: "u"}, "u", st); len(defs) != 0 {
		t.Error("a withdrawn command still hands out tools")
	}
}

// A switch that says "agents: on" while handing out nothing is worse than one
// that refuses.
func TestApprovingSomethingUnmappedIsRefused(t *testing.T) {
	app, st, _ := commandFixture(t)
	if _, err := SetCommandApproved(app.DB, st.Slug, "decrypt", true); err == nil {
		t.Error("approving an unmapped command must be refused")
	}
	if _, err := SetCommandApproved(app.DB, st.Slug, "nope", true); err == nil {
		t.Error("approving a command that does not exist must be refused")
	}
}

// A correction to an already-approved command replaces what agents are calling
// right now. Silently disarming it instead would be a surprise in the other
// direction — an admin fixing a description does not expect the capability to
// go away.
func TestReMappingKeepsTheApproval(t *testing.T) {
	app, st, cmd := commandFixture(t)
	mapIt(t, app, st, cmd)
	if _, err := SetCommandApproved(app.DB, st.Slug, "decrypt", true); err != nil {
		t.Fatal(err)
	}
	again, err := SaveCommandTools(app.DB, st.Slug, cmd.Name, "Corrected.", []TempToolAction{
		{Name: "unpack", Description: "Unpack a capture.", CommandTemplate: "/opt/bin/cap unpack {folder}"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !again.Approved {
		t.Error("a re-map must keep the approval it already had")
	}
	if len(again.Tools) != 1 {
		t.Errorf("a re-map replaces rather than appends: %d actions", len(again.Tools))
	}
}

// What a mapping cannot be useful without, refused where an admin can still see
// why rather than at the first call.
func TestMappingInsistsOnWhatWillBeRun(t *testing.T) {
	app, st, cmd := commandFixture(t)
	ok := []TempToolAction{{Name: "unpack", CommandTemplate: "/opt/bin/cap unpack"}}
	for name, tc := range map[string]struct {
		desc string
		acts []TempToolAction
	}{
		"no description": {"", ok},
		"no actions":     {"d", nil},
		"unnamed action": {"d", []TempToolAction{{CommandTemplate: "/opt/bin/cap"}}},
		"nothing to run": {"d", []TempToolAction{{Name: "unpack"}}},
		// A local command mapped into an HTTP call is a mistake that would
		// otherwise surface as a network failure at the first call.
		"an http action": {"d", []TempToolAction{{Name: "unpack", CommandTemplate: "/opt/bin/cap", URLTemplate: "https://x"}}},
	} {
		if _, err := SaveCommandTools(app.DB, st.Slug, cmd.Name, tc.desc, tc.acts); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
	// And it is refused wholesale — a half-written mapping is not left behind.
	if got, _ := LoadStoreCommand(app.DB, st.Slug, cmd.Name); got.Mapped() {
		t.Error("a refused mapping was stored anyway")
	}
}

// The tool a command becomes carries the folder's slug, so two folders holding
// commands of the same name cannot collide in one agent's catalog.
func TestToolNamesCannotCollideAcrossFolders(t *testing.T) {
	a := StoreCommand{Slug: "bundles", Name: "decrypt"}
	b := StoreCommand{Slug: "archive", Name: "decrypt"}
	if a.ToolName() == b.ToolName() {
		t.Fatalf("both folders mint %q", a.ToolName())
	}
	if a.ToolName() != "decrypt_bundles" {
		t.Errorf("got %q, want verb_noun", a.ToolName())
	}
}

// A mapped command is a capability of a FOLDER, not a tool of the installation.
// It must never appear in the deployment tool list — true by construction (it
// lives on the command record, never in the persistent temp-tool pool the admin
// tool pages enumerate) and worth pinning, because the way this regresses is
// somebody "helpfully" also writing it to the pool so it shows up somewhere
// familiar.
func TestAMappedCommandIsNotADeploymentTool(t *testing.T) {
	app, st, cmd := commandFixture(t)
	saved := mapIt(t, app, st, cmd)
	if tools := LoadPersistentTempTools(app.DB, "u"); len(tools) != 0 {
		t.Errorf("mapping wrote %d tool(s) into the user pool", len(tools))
	}
	// And it stays bound-only, so it would still be hidden if one were ever
	// copied there.
	if !saved.asTempTool().BoundOnly {
		t.Error("bound-only is the belt to that braces")
	}
}

// The folder is the only place a mapped command can be seen, so the stores list
// has to say. Without it a folder handing agents four capabilities looks
// identical to one handing them none — and mapped-but-off looks identical to
// never mapped, which is the state an admin most needs to spot.
func TestStoresListSaysWhatAFolderCarries(t *testing.T) {
	app, st, cmd := commandFixture(t)
	if got := storeCarriesLabel(app.DB, st.Slug); got != "—" {
		t.Errorf("a folder carrying nothing reads as nothing, got %q", got)
	}
	mapIt(t, app, st, cmd)
	if got := storeCarriesLabel(app.DB, st.Slug); !strings.Contains(got, "none approved") {
		t.Errorf("got %q, want the mapped-but-off state named", got)
	}
	if _, err := SetCommandApproved(app.DB, st.Slug, cmd.Name, true); err != nil {
		t.Fatal(err)
	}
	if got := storeCarriesLabel(app.DB, st.Slug); got != "1 command" {
		t.Errorf("got %q, want the singular", got)
	}

	second, err := SaveStoreCommand(app.DB, StoreCommand{
		Slug: st.Slug, Name: "verify", Label: "Verify bundle", Command: "/opt/bin/cap"})
	if err != nil {
		t.Fatal(err)
	}
	mapIt(t, app, st, second)
	if got := storeCarriesLabel(app.DB, st.Slug); got != "1 command of 2 approved" {
		t.Errorf("got %q, want both halves", got)
	}
}

// The folder panel is one expander over one subject. What makes it work is that
// every part addresses the SAME folder — a source that forgot {slug} would
// silently show another folder's commands.
func TestFolderPanelIsScopedToItsFolder(t *testing.T) {
	raw, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	for _, want := range []string{
		`"/filestore/api/commands?slug={slug}"`,
		`"/filestore/api/stores?slug={slug}"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("panel source %s is missing or unscoped — an unscoped one shows another folder's rows", want)
		}
	}
	// The approval is on the row it approves, addressed by {id} rather than
	// {name} — see TestNestedRowURLsDoNotAddressRowsByName.
	if !strings.Contains(src, `"/filestore/api/commands/approve?id={id}"`) {
		t.Error("the Agents switch must address the command it approves")
	}
	// And it is offered only once there is something to approve.
	if !strings.Contains(src, `OnlyIf: "mapped_ok"`) {
		t.Error("a switch offering to enable nothing is a question the reader cannot answer")
	}
	// The command row shows what mapping produced. That column is what makes an
	// orphan impossible to overlook: the mapping is always visible on its own
	// command.
	if !strings.Contains(src, `{Field: "mapped"`) {
		t.Error("the commands table must show mapped state")
	}
	// The second noun is gone. A folder panel with a Toolboxes table again
	// means the collapse was undone.
	if strings.Contains(src, "/filestore/api/toolboxes") {
		t.Error("toolboxes are folded onto commands; there is no separate table to attach from")
	}
}
