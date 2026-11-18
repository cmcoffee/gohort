package orchestrate

// What an app owns besides its own parts (docs/design/app-history.md, stage
// 2, pieces A and B): the agents, pipelines and machines made for it
// (owning_app), and its own tool space. They are in the app's folder, as the
// recipes an export writes (agents/<name>.json, pipelines/<name>.json,
// machines/<name>.json) and each tool's definition (tools/<name>.json, its
// script beside it as tools/<name>.py or the like), so they are in its
// history; a change to one is a change to the app, recorded
// there and, for a shared app, taken out of sharing until an administrator
// approves it, as a change to its page or scripts is.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/apptools"
	"github.com/cmcoffee/gohort/core/ui"
)

// appOwnedDirs are the folders of an app's folder that hold what it owns.
var appOwnedDirs = []string{"agents", "pipelines", "machines", "tools", "knowledge"}

func init() {
	RegisterAppOwnedChangeHook(appOwnedChanged)
	RegisterAppOwnedKeeper(appOwnedCount, deleteAppOwned)
}

// appOwnedItem is one thing an app owns, as a file of its folder: which kind,
// its id, and the file's bytes.
type appOwnedItem struct {
	kind, id, name string
	data           []byte
	// script is a tool's script, a file of its own beside data
	// (scriptFile), so it is edited and diffed as code.
	scriptFile string
	script     []byte
	// files are a collection's seed files, by their path in the folder.
	files map[string][]byte
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
	for _, c := range ListCollections(udb, owner) {
		if c.OwningApp == slug && c.Owner == owner {
			put("knowledge", "collection", c.ID, c.Name, collectionFileOf(udb, owner, c))
			// Its seed files go in a folder beside its file, by that file's name.
			for file, it := range out {
				if it.kind == "collection" && it.id == c.ID {
					dir := strings.TrimSuffix(file, ".json") + "/"
					it.files = map[string][]byte{}
					for name, text := range collectionSeeds(c) {
						it.files[dir+name] = []byte(text)
					}
					out[file] = it
				}
			}
		}
	}
	for _, at := range apptools.Load(owner, slug) {
		def, script := appToolFolderForm(at.Tool)
		b, err := json.MarshalIndent(def, "", "  ")
		if err != nil {
			continue
		}
		base := appToolFileBase(at.Tool.Name)
		it := appOwnedItem{kind: "tool", id: at.Tool.Name, name: at.Tool.Name, data: append(b, '\n')}
		if script != "" {
			it.scriptFile, it.script = "tools/"+base+appToolScriptExt(def.ScriptName), []byte(script)
		}
		out["tools/"+base+".json"] = it
	}
	return out
}

// appToolFileBase is the name of a tool's files in the app's folder: its
// own name, which is already a plain identifier, or a slug of it.
func appToolFileBase(name string) string {
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return slugify(name)
		}
	}
	return name
}

// appToolScriptExt is the extension of a tool's script file, from the name
// its command runs it by (script_name), ".py" when that has none.
func appToolScriptExt(scriptName string) string {
	if ext := filepath.Ext(scriptName); ext != "" && len(ext) <= 6 {
		return ext
	}
	return ".py"
}

// appToolFolderForm is t as its folder file holds it: the script apart, and
// the on-disk name the framework derives (canonical_script_name) left out.
func appToolFolderForm(t TempTool) (TempTool, string) {
	script := t.ScriptBody
	t.ScriptBody, t.CanonicalScriptName = "", ""
	return t, script
}

// appOwnedFiles are the app's owned items as the files of its folder.
func appOwnedFiles(owner, slug string) map[string][]byte {
	out := map[string][]byte{}
	for f, it := range appOwnedIndex(owner, slug) {
		out[f] = it.data
		if it.scriptFile != "" {
			out[it.scriptFile] = it.script
		}
		for name, data := range it.files {
			out[name] = data
		}
	}
	return out
}

// appOwnedFolder is what a folder says the app owns: each file of agents/,
// pipelines/ and machines/, read.
type appOwnedFolder struct {
	agents    map[string]agentExport
	pipelines map[string]PipelineDef
	machines  map[string]MachineDef
	// tools are each tool's definition with its script read back in.
	tools map[string]TempTool
	// collections are each collection's blueprint.
	collections map[string]collectionFile
	raw         map[string][]byte
}

// readAppOwnedFolder reads the folder's owned files. One that does not parse
// is an error naming it: the install stops before anything is saved.
func readAppOwnedFolder(abs, rel string) (appOwnedFolder, error) {
	f := appOwnedFolder{agents: map[string]agentExport{}, pipelines: map[string]PipelineDef{}, machines: map[string]MachineDef{}, tools: map[string]TempTool{}, collections: map[string]collectionFile{}, raw: map[string][]byte{}}
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
			case "knowledge":
				var v collectionFile
				if bad = json.Unmarshal(b, &v); bad == nil && strings.TrimSpace(v.Name) == "" {
					bad = fmt.Errorf("it names no collection")
				}
				if bad == nil {
					v.seeds, bad = readCollectionSeeds(filepath.Join(abs, dir, strings.TrimSuffix(e.Name(), ".json")))
				}
				f.collections[file] = v
			case "tools":
				var v TempTool
				if bad = json.Unmarshal(b, &v); bad == nil {
					bad = readAppToolScript(abs, file, &v)
				}
				f.tools[file] = v
			}
			if bad != nil {
				return f, fmt.Errorf("%s/%s does not read: %v", rel, file, bad)
			}
			f.raw[file] = b
		}
	}
	// One name, one tool: two files naming it would leave which one the
	// app has to the order they were read in.
	byName := map[string]string{}
	files := make([]string, 0, len(f.tools))
	for file := range f.tools {
		files = append(files, file)
	}
	sort.Strings(files)
	for _, file := range files {
		name := f.tools[file].Name
		if other, ok := byName[name]; ok {
			return f, fmt.Errorf("%s/%s and %s/%s both name the tool %q: one name, one file", rel, other, rel, file, name)
		}
		byName[name] = file
	}
	return f, nil
}

// readAppToolScript reads into t the script beside its file (tools/<name>
// and the extension of its script_name), when there is one. A tool named in
// no file, or with its script both inside and beside, does not read.
func readAppToolScript(abs, file string, t *TempTool) error {
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" {
		return fmt.Errorf("it names no tool")
	}
	if t.ScriptName == "" {
		return nil
	}
	side := strings.TrimSuffix(file, ".json") + appToolScriptExt(t.ScriptName)
	b, err := os.ReadFile(filepath.Join(abs, filepath.FromSlash(side)))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	case t.ScriptBody != "":
		return fmt.Errorf("its script is both in it (script_body) and beside it (%s): keep one", side)
	}
	t.ScriptBody = string(b)
	return nil
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
	var changed, gone []string
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
	for file, tool := range f.tools {
		cur, had := apptools.ByName(owner, slug, tool.Name)
		if prev, ok := have[file]; ok && prev.name != tool.Name {
			// The file now holds another name: a rename, the old one goes.
			if err := apptools.DeleteBy(owner, slug, prev.name, via); err != nil {
				return "", fmt.Errorf("the tool in %s: %w", file, err)
			}
		}
		if had && cur.Tool.ScriptBody == tool.ScriptBody {
			tool.CanonicalScriptName = cur.Tool.CanonicalScriptName
		}
		if had && appToolJSONEqual(cur.Tool, tool) {
			continue
		}
		if err := apptools.SaveBy(owner, slug, tool, via); err != nil {
			return "", fmt.Errorf("the tool in %s: %w", file, err)
		}
		changed = append(changed, "tool "+tool.Name)
	}
	for file, cf := range f.collections {
		id := have[file].id
		if cur, ok := have[file]; !ok || string(cur.data) != string(f.raw[file]) {
			saved, err := applyCollectionFile(udb, owner, slug, via, id, cf)
			if err != nil {
				return "", fmt.Errorf("the collection in %s: %w", file, err)
			}
			id = saved
			changed = append(changed, "collection "+cf.Name)
		}
		// Its seed files, whether or not its blueprint changed.
		if c, ok := loadCollection(udb, owner, id); ok {
			indexed, kept := applyCollectionSeeds(c, cf.seeds)
			for _, title := range indexed {
				changed = append(changed, fmt.Sprintf("%s's document %q", cf.Name, title))
			}
			for _, title := range kept {
				gone = append(gone, fmt.Sprintf("%s's document %q", cf.Name, title))
			}
			if len(indexed) > 0 {
				t.seedsIndexed = true
			}
		}
	}
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
	for _, kind := range []string{"agent", "pipeline", "machine", "tool", "collection"} {
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
		case "tool":
			// The app's space goes whole, below.
			continue
		case "collection":
			deleteCollection(udb, o.DB, owner, it.id)
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
	for _, name := range apptools.DeleteAll(owner, slug) {
		gone = append(gone, fmt.Sprintf("tool %q", name))
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
	case "collection":
		// An agent of the person's that reads it, outside the app.
		for _, a := range listAgents(udb, owner) {
			if self["agent:"+a.Name] {
				continue
			}
			for _, id := range a.AttachedCollections {
				if id == it.id {
					return fmt.Sprintf("agent %q", a.Name)
				}
			}
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

// loadAppSpaceTools loads into sess the tools of the app t's agent is part
// of (its own tool space, owner's), as the agent's own kit, and returns
// every name the space holds, loaded or not: a tool of the space that is held
// (an administrator's stop, a reach not yet approved) is not stood in for by
// the pool's tool of its name.
func (t *chatTurn) loadAppSpaceTools(sess *ToolSession, owner string) map[string]bool {
	slug := strings.TrimSpace(t.agent.OwningApp)
	if sess == nil || slug == "" || owner == "" {
		return nil
	}
	tools := apptools.Load(owner, slug)
	names := make(map[string]bool, len(tools))
	// An app that holds its own tools (AppSpec.OwnTools, piece D) cuts the
	// person's pool for its agents (t.appOwnsTools, read by the pool loop).
	if spec, ok := LoadAppSpec(owner, slug); ok && spec.OwnTools {
		t.appOwnsTools = true
	}
	if len(tools) == 0 {
		return names
	}
	review := apptools.LoadReview(owner, slug)
	for _, at := range tools {
		tool := at.Tool
		names[tool.Name] = true
		if tool.Disabled || (t.privateMode && tool.Mode == TempToolModeAPI) {
			continue
		}
		if why := apptools.Held(owner, slug, tool, review); why != "" {
			Log("[orchestrate.tools] app %s/%s: tool %q not loaded: %s", owner, slug, tool.Name, why)
			continue
		}
		if err := sess.AppendTempTool(&tool); err != nil {
			Log("[orchestrate.tools] app %s/%s: tool %q failed to load: %v", owner, slug, tool.Name, err)
			continue
		}
		t.agentOwnTools[tool.Name] = true
	}
	return names
}

// appToolJSONEqual reports whether two tools are one as stored.
func appToolJSONEqual(a, b TempTool) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// wireAppToolSpace gives sess (Builder's) the owner's apps' own tool spaces,
// which tool_def reaches by naming an app; each change is Builder's change to
// the app, through its boundary and history.
func wireAppToolSpace(sess *ToolSession, owner string) {
	if sess == nil || owner == "" {
		return
	}
	own := func(slug string) error {
		if _, ok := LoadAppSpec(owner, slug); !ok {
			return fmt.Errorf("there is no app %q of yours: name an app by its id, as app_def(action=\"list\") shows it", slug)
		}
		return nil
	}
	sess.AppTools = func(slug string) ([]TempTool, error) {
		if err := own(slug); err != nil {
			return nil, err
		}
		var out []TempTool
		for _, at := range apptools.Load(owner, slug) {
			out = append(out, at.Tool)
		}
		return out, nil
	}
	sess.SaveAppTool = func(slug string, t TempTool) error {
		if err := own(slug); err != nil {
			return err
		}
		return apptools.SaveBy(owner, slug, t, "Builder")
	}
	sess.DeleteAppTool = func(slug, name string) error {
		if err := own(slug); err != nil {
			return err
		}
		return apptools.DeleteBy(owner, slug, name, "Builder")
	}
}

// collectionFile is an app's collection as its folder holds it
// (knowledge/<name>.json): its blueprint, never its documents. Submitters
// are named by agent name, as a recipe names agents, so the file reads the
// same wherever the app goes.
type collectionFile struct {
	Name           string            `json:"name"`
	Description    string            `json:"description,omitempty"`
	FilterRules    string            `json:"filter_rules,omitempty"`
	SearchTerms    []string          `json:"search_terms,omitempty"`
	DocsPerRun     int               `json:"docs_per_run,omitempty"`
	Articles       []string          `json:"articles,omitempty"`
	Refresh        string            `json:"refresh,omitempty"`
	Submitters     map[string]string `json:"submitters,omitempty"`
	SuggestionsOff bool              `json:"suggestions_off,omitempty"`
	// seeds are its seed files, read from the folder beside it: file name
	// to text. Not part of the JSON.
	seeds map[string]string
}

// collectionFileOf is c's blueprint, its submitters by name and its refresh
// from the schedule that keeps it.
func collectionFileOf(udb Database, owner string, c Collection) collectionFile {
	f := collectionFile{Name: c.Name, Description: c.Description, FilterRules: c.FilterRules, SearchTerms: c.SearchTerms,
		DocsPerRun: c.DocsPerRun, Articles: c.Articles, SuggestionsOff: c.SuggestionsOff}
	if task, _, _, ok := refreshScheduled(owner, c.ID); ok {
		f.Refresh = task.Every
	}
	for id, mode := range c.Submitters {
		name := id
		if a, ok := loadAgent(udb, id); ok && strings.TrimSpace(a.Name) != "" {
			name = a.Name
		}
		if f.Submitters == nil {
			f.Submitters = map[string]string{}
		}
		f.Submitters[name] = mode
	}
	return f
}

// applyCollectionFile makes owner's app slug's collection id (a new one when
// id is "") what cf says, as via's change: its blueprint, its submitters
// resolved among the app's agents first, and its refresh. Its documents are
// not touched. It returns the collection's id.
func applyCollectionFile(udb Database, owner, slug, via, id string, cf collectionFile) (string, error) {
	var c Collection
	if id != "" {
		cur, ok := loadCollection(udb, owner, id)
		if !ok {
			return "", fmt.Errorf("its collection is gone")
		}
		c = cur
	} else {
		if why := duplicateCollectionName(udb, owner, cf.Name, ""); why != "" {
			return "", errors.New(why)
		}
		c = Collection{ID: UUIDv4(), Owner: owner, Created: time.Now()}
	}
	c.Name, c.Description, c.FilterRules = strings.TrimSpace(cf.Name), cf.Description, cf.FilterRules
	c.SearchTerms, c.DocsPerRun, c.Articles, c.SuggestionsOff = cf.SearchTerms, cf.DocsPerRun, cf.Articles, cf.SuggestionsOff
	c.OwningApp = slug
	c.Submitters = nil
	for name, mode := range cf.Submitters {
		ref := name
		if aid, ok := appScopedRef(udb, owner, slug, "agent", name); ok {
			ref = aid
		} else if a, ok := findAgentByNameOrID(udb, owner, name); ok {
			ref = a.ID
		} else {
			return "", fmt.Errorf("its submitter %q is no agent of yours", name)
		}
		if c.Submitters == nil {
			c.Submitters = map[string]string{}
		}
		c.Submitters[ref] = normalTrust(mode)
	}
	SaveCollectionBy(udb, c, via)
	return c.ID, setRefresh(owner, c.ID, cf.Refresh)
}

// Seed files (docs/design/app-history.md, piece E2 step 2): the documents
// someone added to an app's collection by hand, an upload or a paste. Nothing
// could gather them again, so they travel with the app, as their text: the
// original file is not kept, and the text is rebuilt from what was indexed.
// Each is knowledge/<collection>/<title>.txt, its title on its first line.

// seedReportPrefixes are the report ids a collection's hand-added documents
// are indexed under: an upload, and a paste.
func seedReportPrefixes(c Collection) []string {
	return []string{"file-" + c.ID + "-", "paste-" + c.ID + "-"}
}

// collectionSeeds is c's hand-added documents as seed files: file name to
// text, rebuilt from their indexed passages.
func collectionSeeds(c Collection) map[string]string {
	if VectorDB == nil {
		return nil
	}
	src := collectionSource(c.ID)
	prefixes := seedReportPrefixes(c)
	byDoc := map[string][]EmbeddedChunk{}
	for _, ch := range ChunksWhere(VectorDB, func(ch EmbeddedChunk) bool {
		if ch.Source != src {
			return false
		}
		for _, p := range prefixes {
			if strings.HasPrefix(ch.ReportID, p) {
				return true
			}
		}
		return false
	}) {
		byDoc[ch.ReportID] = append(byDoc[ch.ReportID], ch)
	}
	out := map[string]string{}
	for _, chunks := range byDoc {
		text := AssembleChunkDoc(chunks, 1<<30)
		title := seedTitle(text, "")
		out[slugForReportID(title)+".txt"] = text + "\n"
	}
	return out
}

// seedTitle is a seed file's title: its first line, when that is a "# "
// heading, else the file's name.
func seedTitle(text, file string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if t := strings.TrimSpace(strings.TrimPrefix(first, "# ")); strings.HasPrefix(first, "# ") && t != "" {
		return t
	}
	return strings.TrimSuffix(file, filepath.Ext(file))
}

// readCollectionSeeds reads the seed files in dir (a missing dir has none).
func readCollectionSeeds(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out[e.Name()] = string(b)
	}
	return out, nil
}

// applyCollectionSeeds indexes into c each seed file that is new or differs
// from what c holds under its title, replacing the document of that title,
// and says which it indexed and which of c's hand-added documents have no
// file (kept: a document is removed in the collection, never by leaving out
// its file).
func applyCollectionSeeds(c Collection, seeds map[string]string) (indexed, kept []string) {
	if VectorDB == nil {
		return nil, nil
	}
	have := collectionSeeds(c)
	for file, text := range seeds {
		if strings.TrimSpace(have[file]) == strings.TrimSpace(text) {
			continue
		}
		title := seedTitle(text, file)
		body := strings.TrimSpace(text)
		if first, rest, ok := strings.Cut(body, "\n"); ok && strings.HasPrefix(first, "# ") {
			body = strings.TrimSpace(rest)
		}
		reportID := "file-" + c.ID + "-" + slugForReportID(title)
		if countReportChunks(VectorDB, "paste-"+c.ID+"-"+slugForReportID(title)) > 0 {
			reportID = "paste-" + c.ID + "-" + slugForReportID(title)
		}
		IngestDocument(context.WithoutCancel(AppContext()), VectorDB, collectionSource(c.ID), reportID, title, body)
		indexed = append(indexed, title)
	}
	for file, text := range have {
		if _, ok := seeds[file]; !ok {
			kept = append(kept, seedTitle(text, file))
		}
	}
	sort.Strings(indexed)
	sort.Strings(kept)
	return indexed, kept
}
