package admin

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/recipes"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A connector its template made follows the template's revisions on its own
// while nobody has changed it: at start it takes what the template writes
// now, keeping what it had for one Undo. One somebody changed (by hand, or
// Fix) is left alone, and its button says the update replaces the changes.
// An Undo stops a connector following on its own, so the next start does not
// redo what was undone.
func TestAConnectorNobodyChangedFollowsItsTemplate(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	raw, err := os.ReadFile("../../core/recipes/builtin/elevenlabs-music.json")
	if err != nil {
		t.Fatal(err)
	}
	// An imported template, so the test can revise it: version 1, then 2.
	version := func(path string) {
		t.Helper()
		b := strings.ReplaceAll(string(raw), `"elevenlabs-music"`, `"tunes-tpl"`)
		b = strings.Replace(b, `"/v1/music"`, `"`+path+`"`, 1)
		if _, err := recipes.Import(RootDB, []byte(b), "root"); err != nil {
			t.Fatalf("import: %v", err)
		}
	}
	a := &AdminApp{db: RootDB}
	install := func(name string) {
		t.Helper()
		call := &jsonapi.Call{R: httptest.NewRequest("POST", "/api/templates/install?id=tunes-tpl", nil), User: "root"}
		if _, err := a.handleTemplateInstall(call, struct {
			Answers map[string]string `json:"answers"`
		}{map[string]string{"name": name}}); err != nil {
			t.Fatalf("install %s: %v", name, err)
		}
	}
	spec := func(name string) string {
		c, _ := GetConnector(RootDB, name)
		return string(c.Spec)
	}
	t.Cleanup(func() {
		for _, n := range []string{"tunes", "edited"} {
			UnapproveConnector(RootDB, n)
		}
	})

	version("/v1/music")
	install("tunes")
	install("edited")
	// Adding it keeps what its template wrote, before any start has run.
	if _, ok := templateWrote("tunes"); !ok {
		t.Fatal("an added connector does not keep what its template wrote")
	}
	// Somebody changes one by hand.
	c, _ := GetConnector(RootDB, "edited")
	c.Spec = []byte(strings.Replace(string(c.Spec), `"/v1/music"`, `"/v1/music?fixed=1"`, 1))
	if err := SaveConnector(RootDB, c); err != nil {
		t.Fatal(err)
	}

	version("/v2/music")
	got := updateFromTemplates()
	if strings.Join(got, ",") != "tunes" {
		t.Fatalf("updated %v, want only the one nobody changed", got)
	}
	if !strings.Contains(spec("tunes"), "/v2/music") || !strings.Contains(spec("edited"), "fixed=1") {
		t.Errorf("tunes %s\nedited %s", spec("tunes"), spec("edited"))
	}
	tunes, _ := GetConnector(RootDB, "tunes")
	if update, _, undo := templateRowState(tunes); update || !undo {
		t.Errorf("the updated one: update offered %v, undo %v", update, undo)
	}
	if !asItsTemplateWrote(tunes) || strings.Contains(templateUpdateConfirm(tunes), "replaces") {
		t.Errorf("after its update it holds what its template wrote, for the next revision to follow: %q", templateUpdateConfirm(tunes))
	}
	edited, _ := GetConnector(RootDB, "edited")
	update, confirm, undo := templateRowState(edited)
	if !update || undo || !strings.Contains(confirm, "replaces those changes") {
		t.Errorf("the changed one: update %v, undo %v, asks %q", update, undo, confirm)
	}
	if got := updateFromTemplates(); len(got) != 0 {
		t.Errorf("a second start updated %v again", got)
	}

	// Undo puts it back, and it stays back.
	call := &jsonapi.Call{R: httptest.NewRequest("POST", "/api/connectors/update-template/undo?name=tunes", nil)}
	if _, err := a.handleConnectorTemplateUndo(call, struct{}{}); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if !strings.Contains(spec("tunes"), "/v1/music") {
		t.Errorf("after Undo: %s", spec("tunes"))
	}
	if got := updateFromTemplates(); len(got) != 0 {
		t.Errorf("the start after an Undo redid it: %v", got)
	}
	tunes, _ = GetConnector(RootDB, "tunes")
	if update, confirm, undo := templateRowState(tunes); !update || undo || !strings.Contains(confirm, "replaces those changes") {
		t.Errorf("after Undo its button offers the newer one as a choice: update %v, undo %v, asks %q", update, undo, confirm)
	}

	// Taking the update by its button makes it follow again.
	call = &jsonapi.Call{R: httptest.NewRequest("POST", "/api/connectors/update-template?name=tunes", nil)}
	if _, err := a.handleConnectorTemplateUpdate(call, struct{}{}); err != nil {
		t.Fatalf("update: %v", err)
	}
	version("/v3/music")
	if got := updateFromTemplates(); strings.Join(got, ",") != "tunes" || !strings.Contains(spec("tunes"), "/v3/music") {
		t.Errorf("after its button, the next revision: %v %s", got, spec("tunes"))
	}
}
