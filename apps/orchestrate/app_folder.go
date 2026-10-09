package orchestrate

// An app as a project folder in the author's workspace.
//
// An app was authored as one tool call carrying everything: the page's HTML,
// every Python script and the settings, escaped into JSON strings. A change
// meant re-sending all of it or patching by exact text, and most of what went
// wrong was the format, not the app: an escaped quote that broke a script, a
// re-sent list that dropped a data source, a patch whose find text had
// drifted. A folder is how a developer works instead: one file per part,
// edited in place (workspace write / edit), a backend file run on its own
// (app_def run), and the whole folder published through the same create /
// update, checks and finish check as before (app_def publish). app_def stays
// the one engine; the folder is only how its input is written.
//
//	<slug>.app/
//	  app.json          name, description, settings, shared collections, caps,
//	                    agent / pipeline, and the sections (an html section
//	                    names its file: "html_file": "page.html")
//	  page.html         the page
//	  data/<name>.py    each data source
//	  actions/<name>.py each action
//	  lib/<name>.py     code the scripts share: `from engine import price`
//	  assets/           images, sounds, models
//	  NOTES.md          the app's notes: the plan, decisions, what was left out

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// appFolderScript is one data source or action in app.json; its body is in
// File.
type appFolderScript struct {
	Name         string       `json:"name"`
	File         string       `json:"file"`
	Language     string       `json:"language,omitempty"`
	Capabilities []string     `json:"capabilities,omitempty"`
	Label        string       `json:"label,omitempty"`
	Desc         string       `json:"desc,omitempty"`
	Confirm      string       `json:"confirm,omitempty"`
	Schedule     *AppSchedule `json:"schedule,omitempty"`
}

// appFolderManifest is app.json.
type appFolderManifest struct {
	Name              string            `json:"name"`
	Slug              string            `json:"slug"`
	Description       string            `json:"description,omitempty"`
	RecordKey         string            `json:"record_key,omitempty"`
	AgentID           string            `json:"agent_id,omitempty"`
	PipelineID        string            `json:"pipeline_id,omitempty"`
	FullWidth         bool              `json:"full_width,omitempty"`
	AskDailyUSD       float64           `json:"ask_daily_usd,omitempty"`
	AskUserDailyUSD   float64           `json:"ask_user_daily_usd,omitempty"`
	SharedCollections []string          `json:"shared_collections,omitempty"`
	Settings          []AppSetting      `json:"settings,omitempty"`
	Sections          []map[string]any  `json:"sections"`
	DataSources       []appFolderScript `json:"data_sources,omitempty"`
	Actions           []appFolderScript `json:"actions,omitempty"`
}

// appFolderDir resolves the folder an action names (dir, else "<id>.app")
// inside the turn's workspace.
func (t *chatTurn) appFolderDir(args map[string]any) (abs, rel string, err error) {
	rel = strings.TrimSpace(stringArg(args, "dir"))
	if rel == "" {
		id := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), stringArg(args, "name")))
		if id == "" {
			return "", "", errors.New("name the folder (dir, e.g. \"weather.app\") or the app (id)")
		}
		rel = id + ".app"
	}
	ws, _, _ := t.turnWorkspace()
	abs, err = ResolveWorkspacePath(ws, rel)
	return abs, rel, err
}

func scriptExt(language string) string {
	if l := strings.ToLower(strings.TrimSpace(language)); l == "bash" || l == "sh" {
		return ".sh"
	}
	return ".py"
}

// appDefCheckout writes app id out as a project folder, or a starter folder
// for an app that does not exist yet.
func (t *chatTurn) appDefCheckout(args map[string]any) (string, error) {
	abs, rel, err := t.appFolderDir(args)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(abs, "app.json")); err == nil && !boolArg(args, "overwrite") {
		return "", fmt.Errorf("%s already holds an app folder: edit it, publish it, or pass overwrite=true to replace it with the live app", rel)
	}
	id := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), stringArg(args, "name")))
	spec, exists := LoadAppSpec(t.user, id)
	if !exists {
		return t.appFolderScaffold(abs, rel, args)
	}
	assets := map[string][]byte{}
	if names, err := ListAppAssets(spec.Owner, spec.Slug); err == nil {
		for _, n := range names {
			if data, _, err := ReadAppAsset(spec.Owner, spec.Slug, n); err == nil {
				assets[n] = data
			}
		}
	}
	w, err := writeAppFolder(abs, spec, assets, appLiveFingerprint(spec))
	if err != nil {
		return "", err
	}
	pages, m, nAssets := w.pages, w.manifest, w.assets
	return fmt.Sprintf("Checked out app %q into %s/: app.json, %d page file(s), %d data source(s) in data/, %d action(s) in actions/, %d shared module(s) in lib/, %d asset(s) in assets/, NOTES.md. Edit the files (workspace write, or workspace edit for a few lines), run one backend file with app_def(action=\"run\", dir=%q, file=\"data/<name>.py\", sample=[...]), and publish with app_def(action=\"publish\", dir=%q).",
		spec.Name, rel, pages, len(m.DataSources), len(m.Actions), len(spec.Libraries), nAssets, rel, rel), nil
}

// appFolderWrite is what writeAppFolder wrote.
type appFolderWrite struct {
	pages, assets int
	manifest      appFolderManifest
}

// writeAppFolder writes spec into a folder as its files: the pages, each
// script, the shared modules, the assets given, app.json and NOTES.md. base is
// the live state the folder holds (appLiveFingerprint), or "" when it holds
// none. Shared by checkout (the live app) and unpack (a bundle).
func writeAppFolder(abs string, spec AppSpec, assets map[string][]byte, base string) (appFolderWrite, error) {
	var out appFolderWrite
	var sections []map[string]any
	if len(spec.Sections) == 0 || json.Unmarshal(spec.Sections, &sections) != nil {
		return out, fmt.Errorf("app %q has no editable sections stored (it predates them): use app_def get / update for it", spec.Slug)
	}
	write := func(name string, data []byte) error {
		p := filepath.Join(abs, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		return os.WriteFile(p, data, 0o644)
	}
	pages := 0
	for i, sec := range sections {
		html, ok := sec["html"].(string)
		if !ok {
			continue
		}
		name := "page.html"
		if pages > 0 {
			name = fmt.Sprintf("page-%d.html", i+1)
		}
		pages++
		if err := write(name, []byte(html)); err != nil {
			return out, err
		}
		delete(sec, "html")
		sec["html_file"] = name
	}
	m := appFolderManifest{Name: spec.Name, Slug: spec.Slug, Description: spec.Desc, RecordKey: spec.RecordKey,
		AgentID: spec.AgentID, PipelineID: spec.PipelineID, FullWidth: spec.FullWidth,
		AskDailyUSD: spec.AskDailyUSD, AskUserDailyUSD: spec.AskUserDailyUSD,
		SharedCollections: spec.SharedCollections, Settings: spec.Settings, Sections: sections}
	for _, ds := range spec.DataSources {
		f := "data/" + ds.Name + scriptExt(ds.Language)
		if err := write(f, []byte(ds.Script)); err != nil {
			return out, err
		}
		m.DataSources = append(m.DataSources, appFolderScript{Name: ds.Name, File: f, Language: ds.Language, Capabilities: ds.Capabilities})
	}
	for _, a := range spec.Actions {
		f := "actions/" + a.Name + scriptExt(a.Language)
		if err := write(f, []byte(a.Script)); err != nil {
			return out, err
		}
		m.Actions = append(m.Actions, appFolderScript{Name: a.Name, File: f, Language: a.Language, Capabilities: a.Capabilities,
			Label: a.Label, Desc: a.Desc, Confirm: a.Confirm, Schedule: a.Schedule})
	}
	for name, src := range spec.Libraries {
		if err := write("lib/"+name+".py", []byte(src)); err != nil {
			return out, err
		}
	}
	manifest, _ := json.MarshalIndent(m, "", "  ")
	if err := write("app.json", manifest); err != nil {
		return out, err
	}
	if err := write("NOTES.md", []byte(spec.Notes)); err != nil {
		return out, err
	}
	// The live state the folder holds; none for a folder that holds no live
	// app (an unpacked bundle), which a publish over a same-named app sees.
	if base != "" {
		if err := write(appFolderBaseFile, []byte(base)); err != nil {
			return out, err
		}
	} else {
		os.Remove(filepath.Join(abs, appFolderBaseFile))
	}
	for n, data := range assets {
		if !ValidAppAssetName(n) {
			continue
		}
		if write("assets/"+n, data) == nil {
			out.assets++
		}
	}
	out.pages, out.manifest = pages, m
	return out, nil
}

// appFolderStarterPage is a new app's page: a complete document already wired
// to its backend through window.app, for the author to design from.
const appFolderStarterPage = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>APP_NAME</title>
<style>
  :root { color-scheme: light dark; --bg: #0f1420; --card: #172033; --text: #e8edf5; --muted: #8ea0bd; --accent: #5b8cff; }
  * { box-sizing: border-box; }
  body { margin: 0; font: 16px/1.5 system-ui, sans-serif; background: var(--bg); color: var(--text); }
  main { max-width: 760px; margin: 0 auto; padding: 24px 16px; }
  h1 { margin: 0 0 16px; font-size: 1.6rem; }
  .card { background: var(--card); border-radius: 14px; padding: 18px; margin-bottom: 14px; }
  .muted { color: var(--muted); }
</style>
</head>
<body>
<main>
  <h1>APP_NAME</h1>
  <div class="card" id="content"><span class="muted">Loading…</span></div>
</main>
<script>
// The backend: app.data(name, params) loads a data source (data/<name>.py),
// app.action(name, body) runs an action, app.records.list/save/remove keep
// this person's entries, and app.onChange(fn) fires when they change.
async function load() {
  const el = document.getElementById('content');
  try {
    // const d = await app.data('main');
    el.innerHTML = '<span class="muted">Design the page here.</span>';
  } catch (e) {
    el.textContent = 'Could not load: ' + e.message;
  }
}
load();
app.onChange(load);
</script>
</body>
</html>
`

// appFolderScaffold starts a folder for an app that does not exist yet.
func (t *chatTurn) appFolderScaffold(abs, rel string, args map[string]any) (string, error) {
	name := strings.TrimSpace(firstNonEmptyStr(stringArg(args, "name"), stringArg(args, "id")))
	if name == "" {
		return "", errors.New("no app by that id; to start a new one, pass name")
	}
	slug := slugify(firstNonEmptyStr(stringArg(args, "slug"), name))
	m := appFolderManifest{Name: name, Slug: slug, Description: stringArg(args, "description"),
		Sections: []map[string]any{{"id": "page", "kind": "html", "html_file": "page.html", "height": "100vh"}}}
	manifest, _ := json.MarshalIndent(m, "", "  ")
	files := map[string][]byte{
		"app.json":  manifest,
		"page.html": []byte(strings.ReplaceAll(appFolderStarterPage, "APP_NAME", name)),
		"NOTES.md":  []byte("# " + name + "\n\nPlan: what a complete version of this app includes beyond the request, and anything left out and why.\n"),
	}
	for f, data := range files {
		p := filepath.Join(abs, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("Started a new app folder %s/ for %q: app.json (one html section, page.html), a starter page.html already wired to window.app, and NOTES.md. Add a data source by writing data/<name>.py and listing it in app.json's data_sources ({\"name\", \"file\", \"capabilities\"}); an action likewise under actions/. Code more than one script needs goes in lib/<name>.py once (no listing needed), imported as `from <name> import ...`. Run a backend file with app_def(action=\"run\", dir=%q, file=...), then app_def(action=\"publish\", dir=%q) creates the app.", rel, name, rel, rel), nil
}

// appFolderRead loads a folder into app_def's create/update arguments.
func (t *chatTurn) appFolderRead(abs, rel string) (map[string]any, appFolderManifest, error) {
	var m appFolderManifest
	raw, err := os.ReadFile(filepath.Join(abs, "app.json"))
	if err != nil {
		return nil, m, fmt.Errorf("no app.json in %s: start one with app_def(action=\"checkout\", name=...)", rel)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, m, fmt.Errorf("%s/app.json is not valid JSON: %v", rel, err)
	}
	if strings.TrimSpace(m.Name) == "" {
		return nil, m, fmt.Errorf("%s/app.json needs a name", rel)
	}
	if m.Slug == "" {
		m.Slug = slugify(m.Name)
	}
	readFile := func(f string) (string, error) {
		p, err := ResolveWorkspacePath(abs, f)
		if err != nil {
			return "", err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("%s/app.json names %s, which is not in the folder", rel, f)
		}
		return string(b), nil
	}
	sections := make([]any, 0, len(m.Sections))
	for _, sec := range m.Sections {
		out := map[string]any{}
		for k, v := range sec {
			out[k] = v
		}
		if f, ok := out["html_file"].(string); ok && f != "" {
			html, err := readFile(f)
			if err != nil {
				return nil, m, err
			}
			out["html"] = html
			delete(out, "html_file")
		}
		sections = append(sections, out)
	}
	scripts := func(list []appFolderScript, isAction bool) ([]any, error) {
		out := []any{}
		for _, s := range list {
			body, err := readFile(s.File)
			if err != nil {
				return nil, err
			}
			e := map[string]any{"name": s.Name, "script": body}
			if s.Language != "" {
				e["language"] = s.Language
			}
			if s.Capabilities != nil {
				caps := make([]any, len(s.Capabilities))
				for i, c := range s.Capabilities {
					caps[i] = c
				}
				e["capabilities"] = caps
			}
			if isAction {
				for k, v := range map[string]string{"label": s.Label, "desc": s.Desc, "confirm": s.Confirm} {
					if v != "" {
						e[k] = v
					}
				}
				if s.Schedule != nil {
					var sch map[string]any
					b, _ := json.Marshal(s.Schedule)
					_ = json.Unmarshal(b, &sch)
					e["schedule"] = sch
				}
			}
			out = append(out, e)
		}
		return out, nil
	}
	ds, err := scripts(m.DataSources, false)
	if err != nil {
		return nil, m, err
	}
	acts, err := scripts(m.Actions, true)
	if err != nil {
		return nil, m, err
	}
	// Every lib/*.py is a library: the folder is the whole app, so one
	// deleted here is gone from the app too.
	libs := map[string]any{}
	files, _ := filepath.Glob(filepath.Join(abs, "lib", "*.py"))
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, m, err
		}
		libs[strings.TrimSuffix(filepath.Base(p), ".py")] = string(b)
	}
	args := map[string]any{
		"name": m.Name, "slug": m.Slug, "sections": sections,
		"data_sources": ds, "actions": acts, "libraries": libs, "confirm_rewrite": true,
	}
	for k, v := range map[string]string{"description": m.Description, "record_key": m.RecordKey, "agent_id": m.AgentID, "pipeline_id": m.PipelineID} {
		if v != "" {
			args[k] = v
		}
	}
	if m.FullWidth {
		args["full_width"] = true
	}
	if m.AskDailyUSD > 0 {
		args["ask_daily_usd"] = m.AskDailyUSD
	}
	if m.AskUserDailyUSD > 0 {
		args["ask_user_daily_usd"] = m.AskUserDailyUSD
	}
	if m.SharedCollections != nil {
		list := make([]any, len(m.SharedCollections))
		for i, c := range m.SharedCollections {
			list[i] = c
		}
		args["shared_collections"] = list
	}
	if m.Settings != nil {
		var list []any
		b, _ := json.Marshal(m.Settings)
		_ = json.Unmarshal(b, &list)
		args["settings"] = list
	}
	if notes, err := os.ReadFile(filepath.Join(abs, "NOTES.md")); err == nil {
		args["notes"] = strings.TrimSpace(string(notes))
	}
	return args, m, nil
}

// appDefPublish loads a folder into the live app: created if new, updated if
// not, through the same create / update path and checks, then its assets.
func (t *chatTurn) appDefPublish(args map[string]any) (string, error) {
	abs, rel, err := t.appFolderDir(args)
	if err != nil {
		return "", err
	}
	in, m, err := t.appFolderRead(abs, rel)
	if err != nil {
		return "", err
	}
	live, exists := LoadAppSpec(t.user, m.Slug)
	if exists {
		in["id"] = m.Slug
		if err := appFolderStale(abs, rel, live); err != nil && !boolArg(args, "overwrite_live") {
			return "", err
		}
	}
	if n := strings.TrimSpace(stringArg(args, "note")); n != "" {
		in["note"] = n
	}
	out, err := t.appDefCreateOrUpdate(in, exists)
	if err != nil {
		return "", fmt.Errorf("%s not published: %w", rel, err)
	}
	if saved, ok := LoadAppSpec(t.user, m.Slug); ok {
		os.WriteFile(filepath.Join(abs, appFolderBaseFile), []byte(appLiveFingerprint(saved)), 0o644)
	}
	assets, _ := filepath.Glob(filepath.Join(abs, "assets", "*"))
	sort.Strings(assets)
	var saved, refused []string
	for _, p := range assets {
		name := filepath.Base(p)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if have, _, err := ReadAppAsset(t.user, m.Slug, name); err == nil && string(have) == string(data) {
			continue
		}
		if _, err := SaveAppAsset(t.user, m.Slug, name, data); err != nil {
			refused = append(refused, fmt.Sprintf("%s (%v)", name, err))
			continue
		}
		saved = append(saved, name)
	}
	head := fmt.Sprintf("Published %s/ to app %q.", rel, m.Name)
	if len(saved) > 0 {
		head += " Assets saved: " + strings.Join(saved, ", ") + "."
	}
	if len(refused) > 0 {
		head += " Assets REFUSED: " + strings.Join(refused, "; ") + "."
	}
	return head + "\n\n" + out, nil
}

// appDefRun runs one backend file from a folder against sample records (or
// the live app's), with the same checks a save runs on it, and publishes
// nothing.
func (t *chatTurn) appDefRun(args map[string]any) (string, error) {
	abs, rel, err := t.appFolderDir(args)
	if err != nil {
		return "", err
	}
	in, m, err := t.appFolderRead(abs, rel)
	if err != nil {
		return "", err
	}
	file := strings.TrimSpace(stringArg(args, "file"))
	if file == "" {
		return "", errors.New("file is required: the backend file to run, e.g. \"data/forecast.py\"")
	}
	ds, _ := appDataSources(in["data_sources"])
	acts, _ := appActionDefs(in["actions"])
	spec := AppSpec{Owner: t.user, Slug: m.Slug, Name: m.Name, SharedCollections: m.SharedCollections, Settings: m.Settings,
		AgentID: m.AgentID, PipelineID: m.PipelineID, AskDailyUSD: m.AskDailyUSD, AskUserDailyUSD: m.AskUserDailyUSD}
	if libs, err := appLibraries(in["libraries"]); err != nil {
		return "", fmt.Errorf("%s/lib: %w", rel, err)
	} else if len(libs) > 0 {
		spec.Libraries = libs
	}
	if b, err := json.Marshal(in["sections"]); err == nil {
		spec.Sections = b
	}
	match := func(s appFolderScript) bool { return filepath.Clean(s.File) == filepath.Clean(file) }
	for i, s := range m.DataSources {
		if match(s) && i < len(ds) {
			spec.DataSources = []AppDataSource{ds[i]}
		}
	}
	for i, s := range m.Actions {
		if match(s) && i < len(acts) {
			spec.Actions = []AppAction{acts[i]}
		}
	}
	if len(spec.DataSources)+len(spec.Actions) == 0 {
		return "", fmt.Errorf("%s is not listed in %s/app.json's data_sources or actions: list it there ({\"name\", \"file\", \"capabilities\"}) so the run knows its name and grants", file, rel)
	}
	var sample []map[string]any
	if raw, ok := args["sample"].([]any); ok {
		for _, r := range raw {
			if mm, ok := r.(map[string]any); ok {
				sample = append(sample, mm)
			}
		}
	}
	report, _, _, fail := t.runScriptChecks(spec, appScriptRun{includeActions: true, sample: sample, params: mapArg(args["params"]), preview: testOutputPreview, scriptsOnly: true})
	verdict := "It ran clean."
	if fail > 0 {
		verdict = "Fix the file (workspace edit) and run it again."
	}
	return fmt.Sprintf("Ran %s from %s/ (not published).\n\n%s\n%s", file, rel, strings.TrimSpace(report), verdict), nil
}

// saveAppScreenshot writes a page check's screenshot where the author can
// look at it: into the app's folder when there is one, else the workspace.
// "" when there is no screenshot or nowhere to put it.
func (t *chatTurn) saveAppScreenshot(slug string, shot []byte) string {
	if len(shot) == 0 {
		return ""
	}
	ws, _, _ := t.turnWorkspace()
	if ws == "" {
		return ""
	}
	rel := slug + "-preview.jpg"
	if st, err := os.Stat(filepath.Join(ws, slug+".app")); err == nil && st.IsDir() {
		rel = slug + ".app/preview.jpg"
	}
	p, err := ResolveWorkspacePath(ws, rel)
	if err != nil || os.MkdirAll(filepath.Dir(p), 0o755) != nil || os.WriteFile(p, shot, 0o644) != nil {
		return ""
	}
	return rel
}

// appFolderBaseFile records, in a folder, which state of the live app the
// folder holds: written on checkout and on each publish.
//
// A folder and the live app are two copies of one app. A build published
// its folder, then made four fixes to the live page with patch_html; the
// folder never had them, and its next publish would have undone all four
// without a word. With this, a publish over live changes is refused, and a
// live edit of an app whose folder is current is sent to the folder.
const appFolderBaseFile = ".live"

// appLiveFingerprint is a digest of what an app is, as stored: what a folder
// holds and a publish writes. What changes on its own (verify state, samples)
// is left out, so a verify does not read as an edit.
func appLiveFingerprint(spec AppSpec) string {
	b, _ := json.Marshal([]any{spec.Name, spec.Desc, spec.Notes, spec.RecordKey, spec.AgentID, spec.PipelineID,
		spec.FullWidth, spec.AskDailyUSD, spec.AskUserDailyUSD, spec.SharedCollections, spec.Settings,
		spec.Sections, spec.DataSources, spec.Actions, spec.Libraries})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// appFolderStale says why publishing this folder would undo changes made to
// the live app outside it, or nil when the folder holds the live app as it
// is (it was checked out from it, or last published to it).
func appFolderStale(abs, rel string, live AppSpec) error {
	base, err := os.ReadFile(filepath.Join(abs, appFolderBaseFile))
	if err == nil && strings.TrimSpace(string(base)) == appLiveFingerprint(live) {
		return nil
	}
	what := "the live app was changed since this folder was checked out or last published"
	if err != nil {
		what = "this folder has no record of being checked out from the live app (it predates that record, or it was started new while an app of this name exists)"
	}
	if note := strings.TrimSpace(live.ChangeNote); note != "" {
		what += fmt.Sprintf("; the latest live change: %q", note)
	}
	return fmt.Errorf("NOT PUBLISHED: %s, and publishing %s/ would undo what is live and not in the folder. Either make those changes in the folder's files too and publish with overwrite_live=true, or take the live app back into the folder with app_def(action=\"checkout\", id=%q, overwrite=true) (that replaces the folder's files, so redo any change in them that is not live) and publish from there", what, rel, live.Slug)
}

// appFolderOwnsEdit refuses a live edit of an app whose folder holds it as it
// is: the change belongs in the folder, or the next publish undoes it.
func (t *chatTurn) appFolderOwnsEdit(args map[string]any) error {
	slug := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug")))
	if slug == "" {
		return nil
	}
	live, ok := LoadAppSpec(t.user, slug)
	if !ok {
		return nil
	}
	ws, _, _ := t.turnWorkspace()
	if ws == "" {
		return nil
	}
	rel := slug + ".app"
	abs, err := ResolveWorkspacePath(ws, rel)
	if err != nil || appFolderStale(abs, rel, live) != nil {
		return nil
	}
	return fmt.Errorf("NOT CHANGED: app %q is built in %s/, which holds it as it is live: make this change in the folder's files (workspace edit on %s/page.html, data/, actions/, lib/, app.json) and app_def(action=\"publish\", dir=%q). An edit made here would be undone by the next publish of the folder", slug, rel, rel, rel)
}
