package orchestrate

// What an app owns besides its own parts (docs/design/app-history.md, stage
// 2, piece A): the agents, pipelines and machines made for it (owning_app).
// They are in the app's folder, as the recipes an export writes
// (agents/<name>.json, pipelines/<name>.json, machines/<name>.json), so
// they are in its history; a change to one is a change to the app, recorded
// there and, for a shared app, taken out of sharing until an administrator
// approves it, as a change to its page or scripts is.

import (
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// appOwnedDirs are the folders of an app's folder that hold what it owns.
var appOwnedDirs = []string{"agents", "pipelines", "machines"}

func init() {
	RegisterAppOwnedChangeHook(appOwnedChanged)
}

// appOwnedFiles are owner's app slug's own agents, pipelines and machines,
// as the files of its folder. An agent's sub-agents travel in its file, as
// in an export, so only an agent with no parent has one.
func appOwnedFiles(owner, slug string) map[string][]byte {
	o, ok := agenthostApp()
	if !ok || o.DB == nil || owner == "" || slug == "" {
		return nil
	}
	udb := UserDB(o.DB, owner)
	if udb == nil {
		return nil
	}
	out := map[string][]byte{}
	put := func(dir, name string, v any) {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return
		}
		base := slugify(name)
		if base == "" {
			base = dir[:len(dir)-1]
		}
		file := dir + "/" + base + ".json"
		for n := 2; out[file] != nil; n++ {
			file = fmt.Sprintf("%s/%s-%d.json", dir, base, n)
		}
		out[file] = append(b, '\n')
	}
	for _, a := range listAgents(udb, owner) {
		if a.OwningApp != slug || strings.TrimSpace(a.OwnedBy) != "" {
			continue
		}
		if exp, ok := buildAgentExport(udb, a.ID, owner); ok {
			put("agents", a.Name, exp)
		}
	}
	pa := &pipelineArtifact{app: o}
	for _, d := range ListPipelineDefs(udb, owner) {
		if d.OwningApp == slug {
			put("pipelines", d.Name, ExportPipeline(pa.normalizeStageAgents(d, owner)))
		}
	}
	ma := &machineArtifact{app: o}
	for _, d := range ListMachineDefs(udb, owner) {
		if d.OwningApp == slug {
			put("machines", d.Name, ExportMachine(ma.normalizeDelegates(d, owner)))
		}
	}
	return out
}

// appOwnedChanged is a change to what an app owns (an agent, pipeline or
// machine of it), made by appEdit ("Builder", "Anvil", "import"): recorded
// in the app's history as the app's change, and a shared app whose owner is
// not an administrator is taken out of sharing until one approves it.
func appOwnedChanged(owner, slug, kind, name, appEdit string) {
	spec, ok := LoadAppSpec(owner, slug)
	if !ok {
		return
	}
	via := appEdit
	if via == "import" {
		via = ""
	}
	note := fmt.Sprintf("Change the %s %s", kind, name)
	hist := spec
	hist.ChangedBy, hist.ChangedVia, hist.ChangeNote = owner, via, note
	if err := syncAppRepo(hist); err != nil {
		Log("[app-history] %s/%s: the change to %s %q could not be written to its history: %v", owner, slug, kind, name, err)
	}
	if spec.Shared && !UserIsAdmin(owner) {
		spec.Shared = false
		SaveAppSpecBy(spec, AppSaveNoHistory, owner, via)
		Log("[appspec] %s changed the %s %q of shared app %q: unshared until an administrator approves the new version", owner, kind, name, slug)
	}
}
