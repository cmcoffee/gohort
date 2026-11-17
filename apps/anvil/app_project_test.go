package anvil

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/appagents"
	"github.com/cmcoffee/snugforge/kvlite"
)

// appProjectWorld is alice's app wx, an orchestrate host to install it, a
// workspaces folder, and Anvil behind its routes.
func appProjectWorld(t *testing.T) (T *Anvil, serve func(user, method, path, body string) *httptest.ResponseRecorder) {
	t.Helper()
	if !gitAvailable() {
		t.Skip("git is not installed")
	}
	prevRoot, prevAssets := RootDB, AppAssetsDir()
	RootDB = &DBase{Store: kvlite.MemStore()}
	SetAppAssetsDir(t.TempDir())
	prevHost := agenthost.Get()
	o := &orchestrate.OrchestrateApp{}
	o.DB = &DBase{Store: kvlite.MemStore()}
	agenthost.Register(o)
	t.Cleanup(func() { RootDB = prevRoot; SetAppAssetsDir(prevAssets); agenthost.Register(prevHost) })
	SaveAppSpec(AppSpec{Owner: "alice", Slug: "wx", Name: "Weather", RecordKey: "id",
		Sections:    json.RawMessage(`[{"id":"page","kind":"html","html":"<!DOCTYPE html><html><body><h1>Wx</h1></body></html>"}]`),
		Page:        json.RawMessage(`{"title":"Weather","sections":[]}`),
		DataSources: []AppDataSource{{Name: "now", Script: "print('{}')"}}})
	T, raw := bundleRoutes(t)
	return T, func(user, method, path, body string) *httptest.ResponseRecorder {
		ct := ""
		if body != "" {
			ct = "application/json"
		}
		return raw(user, method, path, []byte(body), ct)
	}
}

func openWx(t *testing.T, serve func(user, method, path, body string) *httptest.ResponseRecorder) string {
	t.Helper()
	w := serve("alice", "POST", "/api/apps/open", `{"slug":"wx"}`)
	var out struct{ ID string }
	if json.Unmarshal(w.Body.Bytes(), &out); w.Code != 200 || out.ID == "" {
		t.Fatalf("open: %d %s", w.Code, w.Body.String())
	}
	return out.ID
}

// One of your apps opens as its folder, in git, with the app as installed as
// its first commit and the folder's live marker kept out of it; opening it
// again is the same project, and somebody else's app does not open.
func TestOneOfYourAppsOpensAsItsFiles(t *testing.T) {
	T, serve := appProjectWorld(t)
	id := openWx(t, serve)
	var p Project
	if !UserDB(T.DB, "alice").Get(projectTable, id, &p) || p.Source != sourceApp || p.AppSlug != "wx" || p.Name != "Weather" {
		t.Fatalf("project %+v", p)
	}
	base, _ := p.baseDir()
	for _, f := range []string{"app.json", "page.html", "data/now.py", ".live"} {
		if _, err := os.Stat(filepath.Join(base, f)); err != nil {
			t.Errorf("the checkout has no %s", f)
		}
	}
	if log, _ := runGit(base, "log", "--format=%s"); strings.TrimSpace(log) != "As installed" {
		t.Errorf("history: %q", log)
	}
	if st, _ := runGit(base, "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Errorf("a fresh checkout has changes: %q", st)
	}
	if tracked, _ := runGit(base, "ls-files", "--", ".live"); strings.TrimSpace(tracked) != "" {
		t.Error("the live marker is in the app's history")
	}
	if again := openWx(t, serve); again != id {
		t.Errorf("opened twice as two projects: %s, %s", id, again)
	}
	if w := serve("root", "POST", "/api/apps/open", `{"slug":"wx"}`); w.Code != http.StatusNotFound {
		t.Errorf("someone else's app opened: %d", w.Code)
	}
}

// Install puts the working copy live and commits it, so the history is what
// was installed.
func TestInstallPutsTheWorkingCopyLive(t *testing.T) {
	T, serve := appProjectWorld(t)
	id := openWx(t, serve)
	var p Project
	UserDB(T.DB, "alice").Get(projectTable, id, &p)
	base, _ := p.baseDir()
	os.WriteFile(filepath.Join(base, "data", "now.py"), []byte("print('{\"t\": 1}')"), 0o644)
	if w := serve("alice", "POST", "/api/project/"+id+"/install", "{}"); w.Code != 200 {
		t.Fatalf("install: %d %s", w.Code, w.Body.String())
	}
	if live, _ := LoadAppSpec("alice", "wx"); live.DataSources[0].Script != "print('{\"t\": 1}')" {
		t.Errorf("not live: %+v", live.DataSources)
	}
	if log, _ := runGit(base, "log", "--format=%s"); strings.TrimSpace(log) != "Installed\nAs installed" {
		t.Errorf("history: %q", log)
	}
	if w := serve("root", "POST", "/api/project/"+id+"/install", "{}"); w.Code == 200 {
		t.Error("somebody else installed alice's working copy")
	}
}

// With Install on every save, a turn ending with changes installs them, for
// an app nobody else uses; a refused install sends the assistant back with
// it; a shared app is left as it is.
func TestInstallOnSaveInstallsAsTheTurnEnds(t *testing.T) {
	T, serve := appProjectWorld(t)
	id := openWx(t, serve)
	var p Project
	UserDB(T.DB, "alice").Get(projectTable, id, &p)
	p.InstallOnSave = true
	base, _ := p.baseDir()
	c := &toolCtx{user: "alice", project: p, base: base}
	c.noteCtx(context.Background())

	os.WriteFile(filepath.Join(base, "data", "now.py"), []byte("print('{\"t\": 2}')"), 0o644)
	if note := c.appFinish(false); note != "" {
		t.Errorf("a turn with no changes: %q", note)
	}
	if live, _ := LoadAppSpec("alice", "wx"); live.DataSources[0].Script != "print('{}')" {
		t.Errorf("a turn with no changes installed: %+v", live.DataSources)
	}
	if note := c.appFinish(true); note != "" {
		t.Fatalf("the turn's install: %q", note)
	}
	if live, _ := LoadAppSpec("alice", "wx"); live.DataSources[0].Script != "print('{\"t\": 2}')" {
		t.Errorf("not installed as the turn ended: %+v", live.DataSources)
	}

	// The live app changed elsewhere: the working copy would undo it.
	live, _ := LoadAppSpec("alice", "wx")
	live.DataSources[0].Script = "print('{\"elsewhere\": 1}')"
	SaveAppSpec(live)
	os.WriteFile(filepath.Join(base, "data", "now.py"), []byte("print('{\"t\": 3}')"), 0o644)
	if note := c.appFinish(true); !strings.Contains(note, "not installed") {
		t.Errorf("a refused install did not send the assistant back: %q", note)
	}

	// Shared: never installed as the turn ends.
	live, _ = LoadAppSpec("alice", "wx")
	live.SharedWith = []string{"bob"}
	SaveAppSpec(live)
	if note := c.appFinish(true); note != "" {
		t.Errorf("a shared app: %q", note)
	}
	if after, _ := LoadAppSpec("alice", "wx"); after.DataSources[0].Script != "print('{\"elsewhere\": 1}')" {
		t.Errorf("a shared app was installed as the turn ended: %+v", after.DataSources)
	}
}

// An app project's assistant can show the person a preview, in the pane the
// main chat opens pages in; no other project has the tool, nor one the caller
// may only read.
func TestOnlyAnAppProjectOffersAPreview(t *testing.T) {
	has := func(c *toolCtx) bool {
		for _, d := range c.codeTools() {
			if d.Tool.Name == "preview" {
				return true
			}
		}
		return false
	}
	if !has(&toolCtx{project: Project{Kind: KindClone, Source: sourceApp, AppSlug: "wx"}}) {
		t.Error("an app project has no preview tool")
	}
	if has(&toolCtx{project: Project{Kind: KindClone}}) || has(&toolCtx{project: Project{Kind: KindClone, Source: sourceApp}, readOnly: true}) {
		t.Error("a preview tool where there is no app to preview, or for a reader")
	}
	var T Anvil
	if p := T.chatPanel(nil, "", "", nil); p.ListPosition != "top" {
		t.Errorf("the sessions are not beside the conversation: %q", p.ListPosition)
	}
}

// An app's NOTES.md is held to the notes cap at the edit, not only at the
// install: an edit or a new file taking it over is refused with the reason,
// and nothing is written; an edit shortening notes already over goes
// through, and other files and other projects are not held to it.
func TestAnAppsNotesAreHeldToTheCapAtTheEdit(t *testing.T) {
	c, _ := checkCtx(t, Project{Kind: KindClone, Source: sourceApp, AppSlug: "wx"})
	limit := AppSpec{}.NotesCap()
	create, _ := toolNamed(t, c, "create_file")
	edit, _ := toolNamed(t, c, "edit_file")
	call := func(td AgentToolDef, args map[string]any) error {
		_, err := td.Handler(context.Background(), args)
		return err
	}
	if err := call(create, map[string]any{"path": "NOTES.md", "content": strings.Repeat("n", limit+1)}); err == nil || !strings.Contains(err.Error(), "not a log") {
		t.Fatalf("over-long new notes: %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.base, "NOTES.md")); !os.IsNotExist(err) {
		t.Fatal("refused notes were written")
	}
	full := strings.Repeat("n", limit-10) + "\nEND"
	if err := call(create, map[string]any{"path": "NOTES.md", "content": full}); err != nil {
		t.Fatal(err)
	}
	if err := call(edit, map[string]any{"path": "NOTES.md", "old_text": "END", "new_text": "END and a long new entry"}); err == nil {
		t.Error("an edit took the notes over the cap")
	}
	if got := readFile(t, filepath.Join(c.base, "NOTES.md")); got != full {
		t.Error("the refused edit changed the file")
	}
	// Notes already over (saved before the cap) may be trimmed in steps.
	os.WriteFile(filepath.Join(c.base, "NOTES.md"), []byte(strings.Repeat("o", limit+50)+"\nTAIL"), 0o644)
	if err := call(edit, map[string]any{"path": "NOTES.md", "old_text": "\nTAIL", "new_text": ""}); err != nil {
		t.Errorf("trimming over-long notes was refused: %v", err)
	}
	if err := call(create, map[string]any{"path": "docs/NOTES.md", "content": strings.Repeat("n", limit+1)}); err != nil {
		t.Errorf("another NOTES.md was held to the cap: %v", err)
	}
	plain, _ := checkCtx(t, Project{Kind: KindClone})
	pc, _ := toolNamed(t, plain, "create_file")
	if err := call(pc, map[string]any{"path": "NOTES.md", "content": strings.Repeat("n", limit+1)}); err != nil {
		t.Errorf("a project that is not an app was held to the cap: %v", err)
	}
}

// An app project's assistant runs the app's checks on its working copy with
// app_checks, installing nothing, and is told what a check is and that
// installing is the owner's: without them one wrote its own runner for
// checks.json and another spent its turn looking for an install command.
func TestAnAppProjectRunsItsChecksOnTheWorkingCopy(t *testing.T) {
	T, serve := appProjectWorld(t)
	id := openWx(t, serve)
	var p Project
	UserDB(T.DB, "alice").Get(projectTable, id, &p)
	base, _ := p.baseDir()

	ctx := T.projectContext(p, base)
	for _, want := range []string{"app_checks", "Install button", "page check:", "shared", "Reset view"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("the app project's notes do not say %q:\n%s", want, ctx)
		}
	}
	tool := func(c *toolCtx) *AgentToolDef {
		for _, d := range c.codeTools() {
			if d.Tool.Name == "app_checks" {
				return &d
			}
		}
		return nil
	}
	if tool(&toolCtx{project: Project{Kind: KindClone}}) != nil {
		t.Error("a project that is not an app offers app_checks")
	}
	c := &toolCtx{user: "alice", project: p, base: base}
	run := tool(c)
	if run == nil {
		t.Fatal("an app project has no app_checks tool")
	}

	// The scripts run in the owner's workspace folder, outside the
	// sandbox's fresh /tmp, as the orchestrate checks tests' do.
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no cache folder")
	}
	ws, err := os.MkdirTemp(cache, "gohort-anvil-checks-")
	if err != nil {
		t.Skip("no cache folder to run scripts from")
	}
	prevWS := WorkspacesDir()
	SetWorkspacesDir(ws)
	t.Cleanup(func() { SetWorkspacesDir(prevWS); os.RemoveAll(ws) })
	os.MkdirAll(filepath.Join(ws, "alice"), 0o755)

	os.WriteFile(filepath.Join(base, "data", "now.py"), []byte("print('{\"t\": 5}')"), 0o644)
	os.WriteFile(filepath.Join(base, "checks.json"), []byte(`[
  {"name": "now says five", "script": "now", "expect": [{"path": "t", "equals": 5}]},
  {"name": "now says six", "script": "now", "expect": [{"path": "t", "equals": 6}]}
]`), 0o644)
	out, err := run.Handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("app_checks: %v", err)
	}
	if strings.Contains(out, "could not run") {
		t.Skipf("no script can run here: %s", out)
	}
	if !strings.Contains(out, `PASS "now says five"`) || !strings.Contains(out, `FAIL "now says six"`) {
		t.Errorf("the checks did not run on the working copy:\n%s", out)
	}
	if live, _ := LoadAppSpec("alice", "wx"); live.DataSources[0].Script != "print('{}')" || len(live.Checks) != 0 {
		t.Errorf("running the checks installed the working copy: %+v", live)
	}
}

// Anvil's turns are held to its own tools and a short list of the
// framework's: the whole catalog (44 tools, image and video among them, about
// 15K tokens a call) went to a local worker that spent minutes on each round.
// Nothing in the list is a tool for anything but working on code.
func TestAnvilIsHeldToItsOwnToolsAndAFewOfTheFrameworks(t *testing.T) {
	spec, ok := appagents.AppAgentByID(anvilAgentID)
	if !ok || !spec.ExactTools || strings.Join(spec.AllowedTools, ",") != strings.Join(anvilCoreTools, ",") {
		t.Fatalf("the Anvil agent is not held to its tools: %+v", spec)
	}
	for _, n := range anvilCoreTools {
		switch n {
		case "image", "video", "request_build", "workspace", "find_tools", "load_tool":
			t.Errorf("%s is on Anvil's list", n)
		}
	}
	// The browser stays: an app that reads a live site is built by looking
	// at the site, and a turn without it could only report a timeout.
	has := map[string]bool{}
	for _, n := range anvilCoreTools {
		has[n] = true
	}
	if !has["browse_page"] || !has["fetch_url"] || !has["web_search"] {
		t.Errorf("Anvil cannot reach the web it builds against: %v", anvilCoreTools)
	}
	if anvilRounds < 30 {
		t.Errorf("a turn has %d rounds: an ordinary change to an app ran out at 15", anvilRounds)
	}
}

// A project brought another way (here, one started in Anvil) that holds an
// app's folder installs as a new app of its owner's and becomes that app's
// project: the app is live, the project is an app project, the live marker
// stays out of git, and a later install is a commit. A folder naming an app its
// owner already has is refused and the app is left as it was; the button is
// only on a project of the caller's that holds app.json and is not an app.
func TestAProjectHoldingAnAppInstallsAsANewApp(t *testing.T) {
	T, serve := appProjectWorld(t)
	udb := UserDB(T.DB, "alice")
	started := func(name, slug string) (Project, string) {
		t.Helper()
		p := Project{ID: UUIDv4(), Name: name, Kind: KindClone, Owner: "alice", Source: sourceNew}
		base, err := p.baseDir()
		if err != nil {
			t.Fatal(err)
		}
		if err := orchestrate.WriteAppFolder("alice", "wx", base); err != nil {
			t.Fatal(err)
		}
		os.Remove(filepath.Join(base, ".live"))
		raw, _ := os.ReadFile(filepath.Join(base, "app.json"))
		var m map[string]any
		json.Unmarshal(raw, &m)
		m["name"], m["slug"] = name, slug
		raw, _ = json.Marshal(m)
		os.WriteFile(filepath.Join(base, "app.json"), raw, 0o644)
		runGit(base, "init", "-q")
		if err := commitCheckout(base, "alice", "Started"); err != nil {
			t.Fatal(err)
		}
		T.saveProject(udb, p)
		return p, base
	}
	offered := func(p Project, user string) bool {
		for _, a := range T.toolbarActions(p, user, false) {
			if a.URL == "anvil_app_install_new" {
				return true
			}
		}
		return false
	}

	p, base := started("Notes Pad", "pad")
	if !offered(p, "alice") || offered(p, "bob") {
		t.Errorf("the button: owner %v, someone else %v", offered(p, "alice"), offered(p, "bob"))
	}
	if ctx := T.projectContext(p, base); !strings.Contains(ctx, "Install as a new app") || !strings.Contains(ctx, "window.app") {
		t.Errorf("the assistant is not told the project holds an app:\n%s", ctx)
	}
	if w := serve("bob", "POST", "/api/project/"+p.ID+"/install_new", "{}"); w.Code == 200 {
		t.Error("somebody else installed alice's project as an app")
	}
	if w := serve("alice", "POST", "/api/project/"+p.ID+"/install_new", "{}"); w.Code != 200 {
		t.Fatalf("install: %d %s", w.Code, w.Body.String())
	}
	if live, ok := LoadAppSpec("alice", "pad"); !ok || live.Name != "Notes Pad" {
		t.Fatalf("no new app: %v %+v", ok, live)
	}
	var now Project
	if !udb.Get(projectTable, p.ID, &now) || now.Source != sourceApp || now.AppSlug != "pad" {
		t.Fatalf("the project is not the app's: %+v", now)
	}
	if offered(now, "alice") {
		t.Error("an app project still offers to become one")
	}
	if strings.Contains(T.projectContext(now, base), "Install as a new app") {
		t.Error("an app project's assistant is still told to install it as a new one")
	}
	if st, _ := runGit(base, "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Errorf("changes left after the install: %q", st)
	}
	// The install changed no file of the folder's, so there is nothing to
	// commit; the live marker it left is not one.
	if log, _ := runGit(base, "log", "--format=%s"); strings.TrimSpace(log) != "Started" {
		t.Errorf("history: %q", log)
	}
	// Now it installs as any app project does.
	os.WriteFile(filepath.Join(base, "data", "now.py"), []byte("print('{\"t\": 2}')"), 0o644)
	if w := serve("alice", "POST", "/api/project/"+p.ID+"/install", "{}"); w.Code != 200 {
		t.Fatalf("a later install: %d %s", w.Code, w.Body.String())
	}
	if log, _ := runGit(base, "log", "--format=%s"); strings.TrimSpace(log) != "Installed\nStarted" {
		t.Errorf("history after a later install: %q", log)
	}

	clash, _ := started("Weather Copy", "wx")
	before, _ := LoadAppSpec("alice", "wx")
	if w := serve("alice", "POST", "/api/project/"+clash.ID+"/install_new", "{}"); w.Code != 409 || !strings.Contains(w.Body.String(), "already have an app") {
		t.Errorf("a folder naming an app alice has: %d %s", w.Code, w.Body.String())
	}
	if after, _ := LoadAppSpec("alice", "wx"); after.Name != before.Name || after.ChangeNote != before.ChangeNote {
		t.Errorf("the app it named was changed: %+v", after)
	}
	var still Project
	if udb.Get(projectTable, clash.ID, &still); still.Source != sourceNew {
		t.Errorf("a refused project changed kind: %q", still.Source)
	}

	empty := Project{ID: UUIDv4(), Name: "plain", Kind: KindClone, Owner: "alice", Source: sourceNew}
	eb, _ := empty.baseDir()
	os.MkdirAll(eb, 0o700)
	if offered(empty, "alice") {
		t.Error("offered on a project with no app.json")
	}
}

// Porting code brought in a bundle into a gohort app: a project with no
// app.json offers start_app, which writes the starter as create_file would
// (an edit each, to undo) and answers with how the folder is built; then the
// project offers app_guide in its place, start_app refuses a second start,
// and Install as a new app makes it one of the owner's apps.
func TestABundleProjectIsPortedIntoANewApp(t *testing.T) {
	T, serve := appProjectWorld(t)
	udb := UserDB(T.DB, "alice")
	p := Project{ID: UUIDv4(), Name: "har-tool", Kind: KindClone, Owner: "alice", Source: sourceBundle, BundleName: "har-tool.bundle"}
	base, err := p.baseDir()
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(base, 0o700)
	os.WriteFile(filepath.Join(base, "analyze.py"), []byte("print('the old tool')\n"), 0o644)
	runGit(base, "init", "-q")
	commitCheckout(base, "alice", "From the bundle")
	T.saveProject(udb, p)

	named := func(c *toolCtx, name string) *AgentToolDef {
		for _, d := range c.codeTools() {
			if d.Tool.Name == name {
				return &d
			}
		}
		return nil
	}
	c := &toolCtx{user: "alice", udb: udb, project: p, base: base, sessionID: "s1"}
	start := named(c, "start_app")
	if start == nil || named(c, "app_guide") != nil {
		t.Fatalf("a project with no app offers start_app %v, app_guide %v", start != nil, named(c, "app_guide") != nil)
	}
	if named(&toolCtx{user: "alice", udb: udb, project: p, base: base, readOnly: true}, "start_app") != nil {
		t.Error("a reader may start an app")
	}
	out, err := start.Handler(context.Background(), map[string]any{"name": "HAR Analyzer"})
	if err != nil {
		t.Fatalf("start_app: %v", err)
	}
	for _, want := range []string{"/apps/har-analyzer/", "Install as a new app", "fetch_url", "window.app", "checks.json", `"file": "data/<name>.py"`} {
		if !strings.Contains(out, want) {
			t.Errorf("start_app's answer does not say %q", want)
		}
	}
	for _, f := range []string{"app.json", "page.html", "NOTES.md", "analyze.py"} {
		if _, err := os.Stat(filepath.Join(base, f)); err != nil {
			t.Errorf("no %s after the start", f)
		}
	}
	if n := len(listEdits(udb, p.ID, "s1")); n != 3 {
		t.Errorf("the start left %d edits, want one per file", n)
	}
	if named(c, "start_app") != nil || named(c, "app_guide") == nil {
		t.Error("a project holding an app still offers start_app, or not app_guide")
	}
	if _, err := start.Handler(context.Background(), map[string]any{"name": "Again"}); err == nil {
		t.Error("a second start was not refused")
	}
	if !strings.Contains(T.projectContext(p, base), "app_guide") {
		t.Error("the assistant is not pointed at app_guide")
	}

	if w := serve("alice", "POST", "/api/project/"+p.ID+"/install_new", "{}"); w.Code != 200 {
		t.Fatalf("install: %d %s", w.Code, w.Body.String())
	}
	if live, ok := LoadAppSpec("alice", "har-analyzer"); !ok || live.Name != "HAR Analyzer" {
		t.Errorf("the ported app is not one of alice's: %v %+v", ok, live)
	}
}
