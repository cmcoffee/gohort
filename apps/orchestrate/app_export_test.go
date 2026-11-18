package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/apptools"
)

// An app's export carries what it owns inside the app, and an import makes
// it the importer's app's own: its agent bound by name to the importer's copy
// of it, its tools in the importer's app's space, its collections' blueprints
// as the importer's app's collections. Nothing it owns rides beside it as a
// dependency, and the app lands switched off.
func TestAnAppsExportCarriesWhatItOwns(t *testing.T) {
	turn, udb := copyTestTurn(t)
	copyTestApp(t, turn, nil)
	clerk, err := saveAgent(udb, AgentRecord{Owner: "u", Name: "Clerk", OrchestratorPrompt: "Help at the desk.", AllowedTools: []string{"web_search"}, OwningApp: "desk", appEdit: "Builder"})
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := LoadAppSpec("u", "desk")
	spec.AgentID = clerk.ID
	SaveAppSpec(spec)
	apptools.SaveBy("u", "desk", TempTool{Name: "lookup", Description: "Look it up.", CommandTemplate: "echo hi"}, "Builder")
	SaveCollectionBy(udb, Collection{ID: "c-1", Owner: "u", Name: "Manual", Description: "The manual.", OwningApp: "desk"}, "Builder")

	bundle, err := ExportArtifactBundleAsUser(RootDB, "u", []ArtifactSel{{Type: "custom_app", Name: "desk", Owner: "u"}}, UserExportOptions{IncludeDeps: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range bundle.Artifacts {
		if a.Type == "agent" {
			t.Errorf("the app's own agent rode beside it as a dependency: %+v", a)
		}
	}
	data, _ := json.Marshal(bundle)
	if !strings.Contains(string(data), `"owned"`) {
		t.Fatalf("the export carries nothing the app owns: %s", data)
	}

	res, err := ImportArtifactBundleAsUser(RootDB, data, "v")
	if err != nil || res.Imported == 0 {
		t.Fatalf("import: %v %+v", err, res)
	}
	got, ok := LoadAppSpec("v", "desk")
	if !ok || !got.Disabled {
		t.Fatalf("the imported app: %v %+v", ok, got)
	}
	vdb := UserDB(orchestrateBaseDB, "v")
	a, ok := loadAgent(vdb, got.AgentID)
	if !ok || a.Owner != "v" || a.OwningApp != "desk" || a.Name != "Clerk" || a.OrchestratorPrompt != "Help at the desk." {
		t.Errorf("the imported app's agent: %v %+v", ok, a)
	}
	if _, ok := apptools.ByName("v", "desk", "lookup"); !ok {
		t.Error("the imported app has no tool of its own")
	}
	var manual Collection
	for _, c := range ListCollections(vdb, "v") {
		if c.Name == "Manual" {
			manual = c
		}
	}
	if manual.OwningApp != "desk" || manual.Description != "The manual." {
		t.Errorf("the imported app's collection: %+v", manual)
	}
}
