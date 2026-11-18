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
	"os"
	"path/filepath"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// appOwnedDirs are the folders of an app's folder that hold what it owns.
var appOwnedDirs = []string{"agents", "pipelines", "machines"}

func init() {
	RegisterAppOwnedChangeHook(appOwnedChanged)
	RegisterAppOwnedKeeper(appOwnedCount, deleteAppOwned)
}

// appOwnedItem is one thing an app owns, as a file of its folder: which kind,
// its id, and the file's bytes.
type appOwnedItem struct {
	kind, id, name string
	data           []byte
}

// appOwnedIndex is owner's app slug's own agents, pipelines and machines, by
// the file each is in its folder. An agent's sub-agents travel in its file,
// as in an export, so only an agent with no parent has one.
func appOwnedIndex(owner, slug string) map[string]appOwnedItem {
	o, ok := agenthostApp()
	if !ok || o.DB == nil || owner == "" || slug == "" {
		return nil
	}
	udb := UserDB(o.DB, owner)
	if udb == nil {
		return nil
	}
	out := map[string]appOwnedItem{}
	put := func(dir, kind, id, name string, v any) {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return
		}
		base := slugify(name)
		if base == "" {
			base = kind
		}
		file := dir + "/" + base + ".json"
		for n := 2; out[file].id != ""; n++ {
			file = fmt.Sprintf("%s/%s-%d.json", dir, base, n)
		}
		out[file] = appOwnedItem{kind: kind, id: id, name: name, data: append(b, '\n')}
	}
	for _, a := range listAgents(udb, owner) {
		if a.OwningApp != slug || strings.TrimSpace(a.OwnedBy) != "" {
			continue
		}
		if exp, ok := buildAgentExport(udb, a.ID, owner); ok {
			put("agents", "agent", a.ID, a.Name, exp)
		}
	}
	pa := &pipelineArtifact{app: o}
	for _, d := range ListPipelineDefs(udb, owner) {
		if d.OwningApp == slug {
			put("pipelines", "pipeline", d.ID, d.Name, ExportPipeline(pa.normalizeStageAgents(d, owner)))
		}
	}
	ma := &machineArtifact{app: o}
	for _, d := range ListMachineDefs(udb, owner) {
		if d.OwningApp == slug {
			put("machines", "machine", d.ID, d.Name, ExportMachine(ma.normalizeDelegates(d, owner)))
		}
	}
	return out
}

// appOwnedFiles are the app's owned items as the files of its folder.
func appOwnedFiles(owner, slug string) map[string][]byte {
	out := map[string][]byte{}
	for f, it := range appOwnedIndex(owner, slug) {
		out[f] = it.data
	}
	return out
}

// appOwnedFolder is what a folder says the app owns: each file of agents/,
// pipelines/ and machines/, read.
type appOwnedFolder struct {
	agents    map[string]agentExport
	pipelines map[string]PipelineDef
	machines  map[string]MachineDef
	raw       map[string][]byte
}

// readAppOwnedFolder reads the folder's owned files. One that does not parse
// is an error naming it: the install stops before anything is saved.
func readAppOwnedFolder(abs, rel string) (appOwnedFolder, error) {
	f := appOwnedFolder{agents: map[string]agentExport{}, pipelines: map[string]PipelineDef{}, machines: map[string]MachineDef{}, raw: map[string][]byte{}}
	for _, dir := range appOwnedDirs {
		entries, err := os.ReadDir(filepath.Join(abs, dir))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			file := dir + "/" + e.Name()
			b, err := os.ReadFile(filepath.Join(abs, dir, e.Name()))
			if err != nil {
				return f, err
			}
			var bad error
			switch dir {
			case "agents":
				var v agentExport
				if bad = json.Unmarshal(b, &v); bad == nil && strings.TrimSpace(v.Name) == "" {
					bad = fmt.Errorf("it names no agent")
				}
				f.agents[file] = v
			case "pipelines":
				var v PipelineDef
				if bad = json.Unmarshal(b, &v); bad == nil && strings.TrimSpace(v.Name) == "" {
					bad = fmt.Errorf("it names no pipeline")
				}
				f.pipelines[file] = v
			case "machines":
				var v MachineDef
				if bad = json.Unmarshal(b, &v); bad == nil && strings.TrimSpace(v.Name) == "" {
					bad = fmt.Errorf("it names no machine")
				}
				f.machines[file] = v
			}
			if bad != nil {
				return f, fmt.Errorf("%s/%s does not read: %v", rel, file, bad)
			}
			f.raw[file] = b
		}
	}
	return f, nil
}

// keepAgentUse carries onto next what is not the agent's definition: its
// identity and what its people chose while using it (agentDefinitionChanged
// names the same fields).
func keepAgentUse(next *AgentRecord, cur AgentRecord) {
	next.ID, next.Owner, next.Created = cur.ID, cur.Owner, cur.Created
	next.OwnedBy, next.BuildNotes, next.PendingApproval = cur.OwnedBy, cur.BuildNotes, cur.PendingApproval
	next.OverriddenFields, next.OverlayRev, next.ShapeID = cur.OverriddenFields, cur.OverlayRev, cur.ShapeID
	next.Hidden, next.ShowOnDashboard, next.Everyone = cur.Hidden, cur.ShowOnDashboard, cur.Everyone
	next.AllowedUsers, next.Maintainers = cur.AllowedUsers, cur.Maintainers
	next.ShareHoldCortex, next.ShareHoldReference, next.ShareMemoryExplicit, next.ShareNoUploads = cur.ShareHoldCortex, cur.ShareHoldReference, cur.ShareMemoryExplicit, cur.ShareNoUploads
	next.ShareCortex, next.ShareReference, next.ShareNotes, next.ShareUploads = cur.ShareCortex, cur.ShareReference, cur.ShareNotes, cur.ShareUploads
	next.AutoApproveTools, next.NoUnattendedTools, next.AuthorizedIdentities = cur.AutoApproveTools, cur.NoUnattendedTools, cur.AuthorizedIdentities
	next.GuardrailExceptions, next.GuardrailDeclines = cur.GuardrailExceptions, cur.GuardrailDeclines
}

// applyAppOwned makes the app slug of t's person own what the folder says:
// each file that differs from what the app owns now is saved, as the item
// that file holds (matched by file, so a rename is an update) or as a new
// one. An item whose file is gone is left, and said. Saves go as t's (Anvil
// or Builder), through the same boundary and history as any change to them.
func (t *chatTurn) applyAppOwned(slug string, f appOwnedFolder) (string, error) {
	if len(f.raw) == 0 {
		return "", nil
	}
	owner, via, udb := t.user, t.changedVia(), t.udb
	have := appOwnedIndex(owner, slug)
	var changed []string
	for file, exp := range f.agents {
		if cur, ok := have[file]; ok && string(cur.data) == string(f.raw[file]) {
			continue
		}
		if err := t.applyOwnedAgent(udb, owner, slug, via, have[file].id, exp); err != nil {
			return "", fmt.Errorf("the agent in %s: %w", file, err)
		}
		changed = append(changed, "agent "+exp.Name)
	}
	for file, def := range f.pipelines {
		if cur, ok := have[file]; ok && string(cur.data) == string(f.raw[file]) {
			continue
		}
		def.OwningApp, def.Owner = slug, owner
		if id := have[file].id; id != "" {
			cur, _ := LoadPipelineDef(udb, owner, id)
			def.ID, def.Created = cur.ID, cur.Created
			def.AllowedUsers, def.Editors, def.Published, def.Global = cur.AllowedUsers, cur.Editors, cur.Published, cur.Global
		} else {
			def.ID = ""
		}
		if err := def.Validate(); err != nil {
			return "", fmt.Errorf("the pipeline in %s: %w", file, err)
		}
		if _, err := SavePipelineDefBy(udb, def, "update", via); err != nil {
			return "", fmt.Errorf("the pipeline in %s: %w", file, err)
		}
		changed = append(changed, "pipeline "+def.Name)
	}
	for file, def := range f.machines {
		if cur, ok := have[file]; ok && string(cur.data) == string(f.raw[file]) {
			continue
		}
		def.OwningApp, def.Owner = slug, owner
		if id := have[file].id; id != "" {
			cur, _ := LoadMachineDef(udb, owner, id)
			def.ID, def.Created = cur.ID, cur.Created
			def.AllowedUsers, def.Editors, def.Published = cur.AllowedUsers, cur.Editors, cur.Published
		} else {
			def.ID = ""
		}
		if _, err := SaveMachineDefBy(udb, def, "update", via); err != nil {
			return "", fmt.Errorf("the machine in %s: %w", file, err)
		}
		changed = append(changed, "machine "+def.Name)
	}
	var gone []string
	for file, it := range have {
		if _, ok := f.raw[file]; !ok {
			gone = append(gone, fmt.Sprintf("%s %q (%s)", it.kind, it.name, file))
		}
	}
	sort.Strings(changed)
	sort.Strings(gone)
	var b strings.Builder
	if len(changed) > 0 {
		b.WriteString(" Also saved: " + strings.Join(changed, ", ") + ".")
	}
	if len(gone) > 0 {
		b.WriteString(" Not in the folder, and kept: " + strings.Join(gone, ", ") + " (an item is removed with Builder, never by leaving out its file).")
	}
	return b.String(), nil
}

// applyOwnedAgent saves the agent recipe exp as the app's: an update of the
// agent id (keeping what its people chose), or a new one when id is "", and
// its sub-agents by name.
func (t *chatTurn) applyOwnedAgent(udb Database, owner, slug, via, id string, exp agentExport) error {
	rec := exp.AgentRecord
	rec.OwningApp, rec.appEdit = slug, via
	// Tools a recipe carries inline are not kept on the record, as an
	// import does not keep them: an app's tools are its tool space's.
	rec.Tools = nil
	if id != "" {
		cur, _ := loadAgent(udb, id)
		keepAgentUse(&rec, cur)
	} else {
		rec = stripAgentIdentity(rec)
		rec.OwningApp, rec.appEdit, rec.Owner = slug, via, owner
	}
	saved, err := saveAgent(udb, rec)
	if err != nil {
		return err
	}
	subs := map[string]AgentRecord{}
	for _, k := range udb.Keys(agentsTable) {
		var s AgentRecord
		if udb.Get(agentsTable, k, &s) && s.OwnedBy == saved.ID {
			subs[strings.ToLower(strings.TrimSpace(s.Name))] = s
		}
	}
	for _, sub := range exp.SubAgents {
		next := sub
		next.OwningApp, next.appEdit = slug, via
		next.Tools = nil
		if cur, ok := subs[strings.ToLower(strings.TrimSpace(sub.Name))]; ok {
			keepAgentUse(&next, cur)
		} else {
			next = stripAgentIdentity(next)
			next.OwningApp, next.appEdit, next.Owner, next.OwnedBy = slug, via, owner, saved.ID
		}
		if _, err := saveAgent(udb, next); err != nil {
			return fmt.Errorf("its sub-agent %s: %w", sub.Name, err)
		}
	}
	return nil
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

// appOwnedCount says what owner's app slug owns, as a phrase for the delete
// confirmation ("2 agents and a pipeline"), "" for nothing. Sub-agents go
// with their agent and are not counted apart.
func appOwnedCount(owner, slug string) string {
	n := map[string]int{}
	for _, it := range appOwnedIndex(owner, slug) {
		n[it.kind]++
	}
	var parts []string
	for _, kind := range []string{"agent", "pipeline", "machine"} {
		switch c := n[kind]; {
		case c == 1:
			parts = append(parts, "1 "+kind)
		case c > 1:
			parts = append(parts, fmt.Sprintf("%d %ss", c, kind))
		}
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// deleteAppOwned deletes what owner's app slug owns, for a person deleting
// it (docs/design/app-history.md, decision 9). An item something outside
// the app uses (another app, a pipeline or machine not the app's) is kept,
// and said: until an app copies what it uses, two can point at one agent.
func deleteAppOwned(owner, slug string) (gone, kept []string) {
	o, ok := agenthostApp()
	if !ok || o.DB == nil {
		return nil, nil
	}
	udb := UserDB(o.DB, owner)
	if udb == nil {
		return nil, nil
	}
	items := appOwnedIndex(owner, slug)
	// What goes with the app does not count as a use of what it owns.
	self := map[string]bool{}
	if spec, ok := LoadAppSpec(owner, slug); ok {
		self["app:"+chFirst(spec.Name, spec.Slug)] = true
	}
	for _, it := range items {
		self[it.kind+":"+it.name] = true
	}
	pipelineApp := map[string]string{}
	for _, s := range ListAppSpecs(owner) {
		if s.Slug != slug && s.PipelineID != "" {
			pipelineApp[s.PipelineID] = chFirst(s.Name, s.Slug)
		}
	}
	files := make([]string, 0, len(items))
	for f := range items {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		it := items[f]
		label := fmt.Sprintf("%s %q", it.kind, it.name)
		if user := ownedItemUser(udb, owner, it, self, pipelineApp); user != "" {
			kept = append(kept, fmt.Sprintf("%s (%s uses it)", label, user))
			continue
		}
		var err error
		switch it.kind {
		case "agent":
			err = deleteAgent(udb, it.id, owner)
		case "pipeline":
			DeletePipelineDef(udb, it.id)
		case "machine":
			DeleteMachineDef(udb, it.id)
		}
		if err != nil {
			kept = append(kept, fmt.Sprintf("%s (%v)", label, err))
			continue
		}
		gone = append(gone, label)
	}
	if len(gone) > 0 {
		Log("[appspec] %s deleted app %q and what it owned: %s", owner, slug, strings.Join(gone, ", "))
	}
	return gone, kept
}

// ownedItemUser names something outside the app that uses it ("app
// \"Desk\"", "pipeline \"Brief\""), "" when nothing does.
func ownedItemUser(udb Database, owner string, it appOwnedItem, self map[string]bool, pipelineApp map[string]string) string {
	switch it.kind {
	case "agent":
		a, ok := loadAgent(udb, it.id)
		if !ok {
			return ""
		}
		for _, u := range agentUsedBy(udb, owner, a) {
			if !self[u.Kind+":"+u.Name] {
				return fmt.Sprintf("%s %q", u.Kind, u.Name)
			}
		}
	case "pipeline":
		if app := pipelineApp[it.id]; app != "" {
			return fmt.Sprintf("app %q", app)
		}
	}
	return ""
}

// appOwnedNotice is the section an editor opens with when what it edits is
// an app's (owning_app), so the refusal on Save is not the first a person
// hears of it. nil when it is no app's.
func appOwnedNotice(owner, slug, kind string) *ui.Section {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return nil
	}
	name := slug
	if spec, ok := LoadAppSpec(owner, slug); ok && strings.TrimSpace(spec.Name) != "" {
		name = spec.Name
	}
	keep := "Sharing it stays yours."
	if kind == "agent" {
		keep = "Sharing it, and your choices while using it (always allowing a tool, hiding it), stay yours."
	}
	return &ui.Section{
		Title: "Part of the app " + name,
		Subtitle: "This " + kind + " is " + name + "'s: what it is changes only with Builder or Anvil, which run the app's checks and keep its history, so a change saved here is refused. " + keep +
			" Deleting the app deletes it too.",
	}
}

// withAppOwnedNotice puts the notice first in sections, when there is one.
func withAppOwnedNotice(sections []ui.Section, owner, slug, kind string) []ui.Section {
	if n := appOwnedNotice(owner, slug, kind); n != nil {
		return append([]ui.Section{*n}, sections...)
	}
	return sections
}

// errAppSwitchedOff is a run refused because the agent's app is switched
// off (AppOwnedStopped). Typed so a channel can answer its contact plainly
// rather than with the generic failure note.
type errAppSwitchedOff struct{ why string }

func (e errAppSwitchedOff) Error() string { return e.why }

// agentRunStopped is the refusal for running agent a while the app that owns
// it is switched off; nil when it may run. Every way an agent run starts
// asks (chat, sync and continuing dispatch, scheduled fires, agents(run),
// an app's Ask); pipelines and machines are stopped in core, where every
// run of theirs passes.
func agentRunStopped(a AgentRecord) error {
	if why := AppOwnedStopped(a.Owner, a.OwningApp, "agent", chFirst(a.Name, a.ID)); why != "" {
		return errAppSwitchedOff{why: why}
	}
	return nil
}
