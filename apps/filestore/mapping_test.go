package filestore

import (
	"os"
	"strings"
	"testing"
)

// mappingFixture is a folder with one command registered against it, reached
// through the tools the mapping conversation actually hands the agent.
func mappingFixture(t *testing.T) (*FileStoreApp, Store, StoreCommand) {
	t.Helper()
	return commandFixture(t)
}

// What mapping produces lands on the command it came from, because there is
// nowhere else for it to land: the actions are fields on that record.
func TestAMappingLandsOnItsCommandRecord(t *testing.T) {
	app, st, cmd := mappingFixture(t)

	msg, err := app.proposeTools(st, cmd, map[string]any{
		"description": "What the capture binary does.",
		"actions": `[{"name":"unpack","description":"Unpack a capture.",
		             "command_template":"/opt/bin/cap unpack {folder}",
		             "params":{"folder":{"type":"string","description":"subfolder"}},
		             "required":["folder"]}]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := LoadStoreCommand(app.DB, st.Slug, cmd.Name)
	if !ok || len(got.Tools) != 1 {
		t.Fatalf("the mapping was not written onto the command: %+v", got)
	}

	// The agent must be told its work is inert, or it will report success to a
	// person who then waits for something to happen — and it must name the one
	// switch that changes that.
	if !strings.Contains(msg, "cannot be called yet") || !strings.Contains(msg, "Agents") {
		t.Errorf("the reply must say it is inert and what makes it live: %q", msg)
	}
	// And the tool name it reports has to be the name in force.
	if !strings.Contains(msg, got.ToolName()) {
		t.Errorf("the reply must name the tool it made: %q", msg)
	}
}

// A correction to an approved command is LIVE on save. Telling the agent it is
// parked would leave an admin believing agents are still calling the old one.
func TestTheReplySaysWhenACorrectionIsAlreadyLive(t *testing.T) {
	app, st, cmd := mappingFixture(t)
	mapIt(t, app, st, cmd)
	if _, err := SetCommandApproved(app.DB, st.Slug, cmd.Name, true); err != nil {
		t.Fatal(err)
	}
	msg, err := app.proposeTools(st, cmd, map[string]any{
		"description": "d", "actions": `[{"name":"unpack","command_template":"/opt/bin/cap unpack"}]`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "LIVE") {
		t.Errorf("a correction to an approved command must say it is already in force: %q", msg)
	}
}

// Re-mapping the same command is the fix path — the first --help rarely says
// everything — so it must replace rather than collide.
func TestReMappingReplacesTheProposal(t *testing.T) {
	app, st, cmd := mappingFixture(t)
	mk := func(actions string) error {
		_, err := app.proposeTools(st, cmd, map[string]any{"description": "d", "actions": actions})
		return err
	}
	if err := mk(`[{"name":"unpack","command_template":"/opt/bin/cap unpack"}]`); err != nil {
		t.Fatal(err)
	}
	if err := mk(`[{"name":"unpack","command_template":"/opt/bin/cap unpack"},
	               {"name":"verify","command_template":"/opt/bin/cap verify"}]`); err != nil {
		t.Fatalf("re-mapping the same command must be allowed: %v", err)
	}
	got, _ := LoadStoreCommand(app.DB, st.Slug, cmd.Name)
	if len(got.Tools) != 2 {
		t.Errorf("the correction must replace the proposal, got %d actions", len(got.Tools))
	}
}

// A placeholder with no parameter behind it produces an action that fails the
// first time it is called, and nobody finds out until then.
func TestProposalRejectsAnUndeclaredPlaceholder(t *testing.T) {
	app, st, cmd := mappingFixture(t)
	_, err := app.proposeTools(st, cmd, map[string]any{
		"description": "d",
		"actions": `[{"name":"unpack","command_template":"/opt/bin/cap unpack {folder} --key {key}",
		              "params":{"folder":{"type":"string"}}}]`,
	})
	if err == nil || !strings.Contains(err.Error(), "key") {
		t.Fatalf("the refusal must name the offending placeholder: %v", err)
	}
	if got, _ := LoadStoreCommand(app.DB, st.Slug, cmd.Name); got.Mapped() {
		t.Error("a refused proposal must not have been stored")
	}
}

// Malformed input from a model is expected, not exceptional: it has to come back
// as a sentence the agent can act on rather than as a stored half-mapping.
func TestProposalHandlesBadActions(t *testing.T) {
	app, st, cmd := mappingFixture(t)
	for name, actions := range map[string]string{
		"not json":     `unpack the capture`,
		"not an array": `{"name":"unpack"}`,
		"empty":        ``,
	} {
		if _, err := app.proposeTools(st, cmd, map[string]any{
			"description": "d", "actions": actions}); err == nil {
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
	if !strings.Contains(src, `"/filestore/api/map/chat/send?id={id}"`) {
		t.Error("the mapping conversation must name the command it was opened on, by the composite id its row carries")
	}
	if !strings.Contains(src, `ui.Expand("Map"`) {
		t.Error("Map belongs on the command's own row — that is what makes the result land where it came from")
	}
}

// Mapping has a known shape — look, map, correct — so the way in is buttons,
// not a blank box. An empty composer asks an admin to guess the wording of a
// job the page already knows, and the wording is what decides whether the agent
// probes before it proposes.
func TestMappingIsGuidedRatherThanTyped(t *testing.T) {
	raw, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	for _, want := range []string{"Map it", "Show its help", "Refine"} {
		if !strings.Contains(src, `Label: "`+want+`"`) {
			t.Errorf("the mapping panel must offer %q as a button", want)
		}
	}
	if strings.Count(src, "Prompt:") < 3 {
		t.Error("each mapping button has to carry what it says — a label with no Prompt is a button that does nothing")
	}
	// The panel opens inside a row of a table inside a row of another table.
	// At the viewport-tall default it buries the folder it belongs to.
	if !strings.Contains(src, `Height: "420px"`) {
		t.Error("a conversation mounted in a row expander must be bounded, or it takes the whole page")
	}
}
