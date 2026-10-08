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
//	  assets/           images, sounds, models
//	  NOTES.md          the app's notes: the plan, decisions, what was left out

import (
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
	var sections []map[string]any
	if len(spec.Sections) == 0 || json.Unmarshal(spec.Sections, &sections) != nil {
		return "", fmt.Errorf("app %q has no editable sections stored (it predates them): use app_def get / update for it", spec.Slug)
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
			return "", err
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
			return "", err
		}
		m.DataSources = append(m.DataSources, appFolderScript{Name: ds.Name, File: f, Language: ds.Language, Capabilities: ds.Capabilities})
	}
	for _, a := range spec.Actions {
		f := "actions/" + a.Name + scriptExt(a.Language)
		if err := write(f, []byte(a.Script)); err != nil {
			return "", err
		}
		m.Actions = append(m.Actions, appFolderScript{Name: a.Name, File: f, Language: a.Language, Capabilities: a.Capabilities,
			Label: a.Label, Desc: a.Desc, Confirm: a.Confirm, Schedule: a.Schedule})
	}
	manifest, _ := json.MarshalIndent(m, "", "  ")
	if err := write("app.json", manifest); err != nil {
		return "", err
	}
	if err := write("NOTES.md", []byte(spec.Notes)); err != nil {
		return "", err
	}
	assets := 0
	if names, err := ListAppAssets(spec.Owner, spec.Slug); err == nil {
		for _, n := range names {
			if data, _, err := ReadAppAsset(spec.Owner, spec.Slug, n); err == nil && write("assets/"+n, data) == nil {
				assets++
			}
		}
	}
	return fmt.Sprintf("Checked out app %q into %s/: app.json, %d page file(s), %d data source(s) in data/, %d action(s) in actions/, %d asset(s) in assets/, NOTES.md. Edit the files (workspace write, or workspace edit for a few lines), run one backend file with app_def(action=\"run\", dir=%q, file=\"data/<name>.py\", sample=[...]), and publish with app_def(action=\"publish\", dir=%q).",
		spec.Name, rel, pages, len(m.DataSources), len(m.Actions), assets, rel, rel), nil
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
	return fmt.Sprintf("Started a new app folder %s/ for %q: app.json (one html section, page.html), a starter page.html already wired to window.app, and NOTES.md. Add a data source by writing data/<name>.py and listing it in app.json's data_sources ({\"name\", \"file\", \"capabilities\"}); an action likewise under actions/. Run a backend file with app_def(action=\"run\", dir=%q, file=...), then app_def(action=\"publish\", dir=%q) creates the app.", rel, name, rel, rel), nil
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
	args := map[string]any{
		"name": m.Name, "slug": m.Slug, "sections": sections,
		"data_sources": ds, "actions": acts, "confirm_rewrite": true,
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
	_, exists := LoadAppSpec(t.user, m.Slug)
	if exists {
		in["id"] = m.Slug
	}
	if n := strings.TrimSpace(stringArg(args, "note")); n != "" {
		in["note"] = n
	}
	out, err := t.appDefCreateOrUpdate(in, exists)
	if err != nil {
		return "", fmt.Errorf("%s not published: %w", rel, err)
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
	spec := AppSpec{Owner: t.user, Slug: m.Slug, Name: m.Name, SharedCollections: m.SharedCollections, Settings: m.Settings, AgentID: m.AgentID}
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
