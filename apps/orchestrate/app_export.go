package orchestrate

// An app's export carries what it owns (docs/design/app-history.md, piece F):
// its agents, pipelines, machines, tools and collections' blueprints, as the
// files of its folder (appOwnedFiles), inside the app's recipe; an import
// makes them the imported app's own through the same path an install of the
// folder takes, credited to the import, and binds the app to its own agent
// and pipeline. What it holds as data (records, memory, what its collections
// gathered) never travels.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	AppOwnedExport = exportAppOwned
	AppOwnedImport = importAppOwned
}

// exportAppOwned is core's AppOwnedExport.
func exportAppOwned(owner, slug string) (map[string][]byte, string, string) {
	o, ok := agenthostApp()
	if !ok || o.DB == nil {
		return nil, "", ""
	}
	spec, ok := LoadAppSpec(owner, slug)
	if !ok {
		return nil, "", ""
	}
	udb := UserDB(o.DB, owner)
	agentName, pipelineName := "", ""
	if a, ok := loadAgent(udb, spec.AgentID); ok && a.OwningApp == slug {
		agentName = a.Name
	}
	if d, ok := LoadPipelineDef(udb, owner, spec.PipelineID); ok && d.OwningApp == slug {
		pipelineName = d.Name
	}
	files := appOwnedFiles(owner, slug)
	// The file stores it was granted, so the importer is asked for theirs
	// (app_needs.go). Export only: never in the app's folder or history.
	if stores := appStoreNeeds(owner, slug); len(stores) > 0 {
		b, _ := json.Marshal(stores)
		files[appStoresFile] = b
	}
	return files, agentName, pipelineName
}

// importAppOwned is core's AppOwnedImport: files written to a folder of
// their own, read and applied as an install applies a folder, the import
// credited with each change, and the app bound to its own agent and pipeline
// by the names its recipe carries.
func importAppOwned(owner, slug string, files map[string][]byte) error {
	o, ok := agenthostApp()
	if !ok || o.DB == nil {
		return fmt.Errorf("agents are not available here")
	}
	dir, err := os.MkdirTemp("", "app-import-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for name, data := range files {
		clean := filepath.Clean(filepath.FromSlash(name))
		if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("the recipe names a file outside the app's folder: %q", name)
		}
		p := filepath.Join(dir, clean)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			return err
		}
	}
	folder, err := readAppOwnedFolder(dir, "the app's recipe")
	if err != nil {
		return err
	}
	t := o.personTurn(context.Background(), owner)
	t.via = "import"
	if _, err := t.applyAppOwned(slug, folder); err != nil {
		return err
	}
	// What it names that only a deployment has: found here mapped to
	// itself, the rest for its owner to answer (app_needs.go).
	defer recordAppNeeds(owner, slug, readAppStoresFile(files))
	bindAppOwnRefs(t.udb, owner, slug, "Point the app at its own agent and pipeline, as imported")
	return nil
}

// bindAppOwnRefs points owner's app slug at its own agent and pipeline where
// it names them by name (as an export does), saved with note as the owner's
// own change, committed at once as theirs (as appOwnedChanged records the
// import's agents): not an agent's work waiting to be finished, and never
// under the note the app carried from where it was exported.
func bindAppOwnRefs(udb Database, owner, slug, note string) {
	spec, ok := LoadAppSpec(owner, slug)
	if !ok {
		return
	}
	changed := false
	if id, ok := appScopedRef(udb, owner, slug, "agent", spec.AgentID); ok && id != spec.AgentID {
		spec.AgentID, changed = id, true
	}
	if id, ok := appScopedRef(udb, owner, slug, "pipeline", spec.PipelineID); ok && id != spec.PipelineID {
		spec.PipelineID, changed = id, true
	}
	if changed {
		spec.ChangeNote = note
		SaveAppSpecBy(spec, AppSaveNoHistory, owner, "")
	}
}
