package filestore

// The row can say THAT a command is mapped and not what it was mapped as.
// This panel answers "do I need to re-map it?" from data already on the
// record — so what it reports has to be the fields an agent actually runs.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func mappedCommandFixture(t *testing.T) (*FileStoreApp, Store) {
	t.Helper()
	app, st, _ := scopeFixture(t)
	if _, err := SaveStoreCommand(app.DB, StoreCommand{
		Slug: st.Slug, Name: "weka", Label: "weka", Command: "/opt/bin/weka",
	}); err != nil {
		t.Fatalf("register command: %v", err)
	}
	_, err := SaveCommandTools(app.DB, st.Slug, "weka", "Reads a diagnostic bundle.", []TempToolAction{{
		Name:            "syshealth",
		Description:     "Disk, memory, CPU, load.",
		CommandTemplate: "/opt/bin/weka syshealth",
		WorkDir:         "folder",
		Params: map[string]ToolParam{
			"folder": {Type: "string", PathScope: "files:" + st.Slug},
		},
		Required: []string{"folder"},
	}})
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	return app, st
}

// realAdmin, because this endpoint is behind adminOnly and the package's
// asAdmin helper only establishes a SESSION — it sets no Admin flag, which is
// all the handlers it was written for needed.
func realAdmin(t *testing.T, r *http.Request) *http.Request {
	t.Helper()
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:boss", AuthUser{Username: "boss", Admin: true})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, "boss")})
	return r
}

func mappingPayload(t *testing.T, app *FileStoreApp, st Store) map[string]any {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/commands/mapping?id="+st.Slug+"/weka", nil)
	w := httptest.NewRecorder()
	app.handleCommandMapping(w, realAdmin(t, r))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestMappingOverviewReportsWhatAnAgentWouldRun(t *testing.T) {
	app, st := mappedCommandFixture(t)
	out := mappingPayload(t, app, st)

	acts, _ := out["actions"].([]any)
	if len(acts) != 1 {
		t.Fatalf("want one action, got %v", out["actions"])
	}
	a, _ := acts[0].(map[string]any)
	if a["command"] != "/opt/bin/weka syshealth" {
		t.Errorf("the command line is the thing being checked: %v", a["command"])
	}
	// Where it runs, and which parameter carries the folder. Both are
	// invisible on the row and both decide whether the mapping is right.
	if a["runs_in"] != "folder" {
		t.Errorf("want the work_dir parameter named, got %v", a["runs_in"])
	}
	takes, _ := a["params"].(string)
	if !strings.Contains(takes, "files:"+st.Slug) {
		t.Errorf("the path scope decides whether a folder name resolves; it must show: %q", takes)
	}
	if !strings.Contains(takes, "[runs here]") {
		t.Errorf("the folder parameter should be marked: %q", takes)
	}
}

// Mapped-but-off looks identical to unmapped from any distance, so the panel
// has to name it, and colour it as not-live.
func TestMappingOverviewNamesTheApprovalState(t *testing.T) {
	app, st := mappedCommandFixture(t)

	out := mappingPayload(t, app, st)
	if !strings.Contains(out["state"].(string), "switched off") {
		t.Errorf("an unapproved mapping must say so: %v", out["state"])
	}
	if out["state_status"] != "warn" {
		t.Errorf("not-live should not read as ok: %v", out["state_status"])
	}

	if _, err := SetCommandApproved(app.DB, st.Slug, "weka", true); err != nil {
		t.Fatal(err)
	}
	out = mappingPayload(t, app, st)
	if out["state_status"] != "ok" {
		t.Errorf("an approved mapping is live: %v", out)
	}
}

// A command nobody mapped still answers, rather than 404-ing an admin who
// opened the panel to find out exactly that.
func TestMappingOverviewOnAnUnmappedCommand(t *testing.T) {
	app, st, _ := scopeFixture(t)
	if _, err := SaveStoreCommand(app.DB, StoreCommand{
		Slug: st.Slug, Name: "raw", Label: "raw", Command: "/opt/bin/raw",
	}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/commands/mapping?id="+st.Slug+"/raw", nil)
	w := httptest.NewRecorder()
	app.handleCommandMapping(w, realAdmin(t, r))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	if !strings.Contains(out["state"].(string), "Not mapped") {
		t.Errorf("want a plain answer, got %v", out["state"])
	}
}
