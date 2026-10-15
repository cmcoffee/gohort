package filestore

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A run is watched for growth where its output lands: each folder it was
// given, the folder of each file it was given, or its working folder when it
// was given neither.
func TestARunWatchesWhereItsOutputLands(t *testing.T) {
	root := t.TempDir()
	caseDir := filepath.Join(root, "CASE-1")
	os.MkdirAll(caseDir, 0o755)
	archive := filepath.Join(root, "drop", "dump.zip")
	os.MkdirAll(filepath.Dir(archive), 0o755)
	os.WriteFile(archive, []byte("x"), 0o644)
	act := TempToolAction{Params: map[string]ToolParam{
		"dst": {PathScope: "files:intake"}, "archive": {PathScope: "files:intake"}, "source": {}}}
	got := growthDirs(act, map[string][]string{"dst": {caseDir}, "archive": {archive}, "source": {"remote/file"}}, root)
	sort.Strings(got)
	want := []string{caseDir, filepath.Dir(archive)}
	sort.Strings(want)
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("watched %v, want %v", got, want)
	}
	if got := growthDirs(act, map[string][]string{"source": {"x"}}, root); len(got) != 1 || got[0] != root {
		t.Errorf("with no folder given: %v", got)
	}
}

// The stored setting is "always" or nothing, however it was typed.
func TestBackgroundSettingIsNormalized(t *testing.T) {
	for in, want := range map[string]string{"always": "always", " Always ": "always", "": "", "sometimes": ""} {
		if got := backgroundMode(in); got != want {
			t.Errorf("backgroundMode(%q) = %q, want %q", in, got, want)
		}
	}
	if (StoreCommand{}).detachAfter() != 0 {
		t.Error("a command left to the deployment set its own detach time")
	}
}

// The Background switch on a mapped command's row turns it on and off without
// touching Agents, and the row reports it so the switch shows its state.
func TestTheBackgroundSwitch(t *testing.T) {
	app, st, cmd := mappingFixture(t)
	if _, err := app.proposeTools(st, cmd, map[string]any{"description": "d", "actions": `[{"name":"get","description":"Get.","command_template":"` + cmd.Command + ` get {folder}","params":{"folder":{"type":"string","path_scope":"files"}},"required":["folder"]}]`}); err != nil {
		t.Fatal(err)
	}
	SetCommandApproved(app.DB, st.Slug, cmd.Name, true)
	flip := func(on string) *httptest.ResponseRecorder {
		r := asAdmin(t, httptest.NewRequest(http.MethodPost, "/filestore/api/commands/background?id="+st.Slug+"/"+cmd.Name, strings.NewReader(`{"background_on":`+on+`}`)), "admin")
		AuthDB().Set(AuthTable, "user:admin", AuthUser{Username: "admin", Admin: true})
		w := httptest.NewRecorder()
		app.handleCommandBackground(w, r)
		return w
	}
	if w := flip("true"); w.Code != 200 {
		t.Fatalf("switch on: %d %s", w.Code, w.Body.String())
	}
	got, _ := LoadStoreCommand(app.DB, st.Slug, cmd.Name)
	if got.Background != "always" || !got.Approved {
		t.Errorf("after switching on: background=%q approved=%v", got.Background, got.Approved)
	}
	flip("false")
	if got, _ := LoadStoreCommand(app.DB, st.Slug, cmd.Name); got.Background != "" || !got.Approved {
		t.Errorf("after switching off: background=%q approved=%v", got.Background, got.Approved)
	}
}

// Mapping a command that runs long has Builder suggest the switch, which it
// cannot flip itself; once it is on, the suggestion stops.
func TestMappingALongCommandSuggestsBackground(t *testing.T) {
	app, st, cmd := mappingFixture(t)
	acts := `[{"name":"get","description":"Get.","command_template":"` + cmd.Command + ` get {folder}","params":{"folder":{"type":"string","path_scope":"files"}},"required":["folder"]}]`
	msg, err := app.proposeTools(st, cmd, map[string]any{"description": "d", "actions": acts, "runs_long": true})
	if err != nil || !strings.Contains(msg, "suggest to the admin that they turn on Background") {
		t.Errorf("no suggestion: %q %v", msg, err)
	}
	if msg, _ := app.proposeTools(st, cmd, map[string]any{"description": "d", "actions": acts}); strings.Contains(msg, "Background") {
		t.Errorf("a command not marked long was given the suggestion: %q", msg)
	}
	c, _ := LoadStoreCommand(app.DB, st.Slug, cmd.Name)
	c.Background = "always"
	app.DB.Set(commandsTable, commandKey(c.Slug, c.Name), c)
	if msg, _ := app.proposeTools(st, cmd, map[string]any{"description": "d", "actions": acts, "runs_long": "true"}); strings.Contains(msg, "suggest") {
		t.Errorf("suggested a switch that is already on: %q", msg)
	}
}
