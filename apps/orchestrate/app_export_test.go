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

// An import is its importer's own change, and its history says so at once:
// nothing waits as an agent's unfinished work, and the note the app's last
// change carried where it was exported is never what a commit here says.
func TestAnImportedAppsHistoryIsItsImportersAtOnce(t *testing.T) {
	if appGit() == "" {
		t.Skip("git is not installed")
	}
	turn, udb := copyTestTurn(t)
	copyTestApp(t, turn, nil)
	clerk, err := saveAgent(udb, AgentRecord{Owner: "u", Name: "Clerk", OrchestratorPrompt: "Help at the desk.", OwningApp: "desk", appEdit: "Builder"})
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := LoadAppSpec("u", "desk")
	spec.AgentID, spec.ChangeNote = clerk.ID, "Exporter's own last change"
	SaveAppSpec(spec)
	bundle, err := ExportArtifactBundleAsUser(RootDB, "u", []ArtifactSel{{Type: "custom_app", Name: "desk", Owner: "u"}}, UserExportOptions{IncludeDeps: true})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(bundle)
	if res, err := ImportArtifactBundleAsUser(RootDB, data, "v"); err != nil || res.Imported == 0 {
		t.Fatalf("import: %v %+v", err, res)
	}
	dir, _ := appRepoDir("v", "desk")
	if p := loadAppPending(dir); p.By != "" {
		t.Errorf("the import left saves waiting as unfinished work: %+v", p)
	}
	if st, _ := appRepoGit(dir, "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Errorf("the import left changes out of the history: %q", st)
	}
	log, _ := appRepoGit(dir, "log", "--format=%an|%s|%b")
	if strings.Contains(log, "Exporter's own last change") {
		t.Errorf("a commit here carries the exporter's note:\n%s", log)
	}
	if !strings.Contains(log, "v|Point the app at its own agent and pipeline, as imported") {
		t.Errorf("the import's re-pointing is not the importer's commit:\n%s", log)
	}

	// The importer's next change of their own carries no stale note either.
	got, _ := LoadAppSpec("v", "desk")
	got.Disabled = false
	SaveAppSpec(got)
	if log, _ := appRepoGit(dir, "log", "-1", "--format=%s"); strings.Contains(log, "Exporter") {
		t.Errorf("the importer's own save is recorded under the exporter's note: %q", log)
	}

	// An app with nothing of its own to point at: no save of the import's
	// replaces the note, so it must not have travelled in the first place.
	copyTestApp(t, turn, map[string]any{"name": "Plain"})
	plain, _ := LoadAppSpec("u", "plain")
	plain.ChangeNote = "Exporter's own last change"
	SaveAppSpec(plain)
	bundle, _ = ExportArtifactBundleAsUser(RootDB, "u", []ArtifactSel{{Type: "custom_app", Name: "plain", Owner: "u"}}, UserExportOptions{IncludeDeps: true})
	data, _ = json.Marshal(bundle)
	if res, err := ImportArtifactBundleAsUser(RootDB, data, "v"); err != nil || res.Imported == 0 {
		t.Fatalf("import: %v %+v", err, res)
	}
	got, _ = LoadAppSpec("v", "plain")
	got.Desc = "Mine now."
	SaveAppSpec(got)
	pdir, _ := appRepoDir("v", "plain")
	if log, _ := appRepoGit(pdir, "log", "--format=%s"); strings.Contains(log, "Exporter") {
		t.Errorf("the importer's own save is recorded under the exporter's note:\n%s", log)
	}
}
