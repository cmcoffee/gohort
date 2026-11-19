package orchestrate

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/apptools"
	"github.com/cmcoffee/snugforge/kvlite"
)

// What an app owns lives inside the app: its editor's address opens a page
// saying whose it is, with the way to the app and to Builder, and no editor.
// What no app owns opens its editor as always.
func TestAnAppsOwnItemsOpenNoEditor(t *testing.T) {
	pinRootDB(t)
	app, udb, user := newTestOrchestrate(t)
	SaveAppSpec(AppSpec{Owner: user, Slug: "desk", Name: "Help Desk", RecordKey: "id"})
	agent, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Clerk", OrchestratorPrompt: "Answer.", OwningApp: "desk", appEdit: "Builder"})
	if err != nil {
		t.Fatal(err)
	}
	own, _ := saveAgent(udb, AgentRecord{Owner: user, Name: "Mine", OrchestratorPrompt: "Mine."})
	pipe, _ := SavePipelineDefBy(udb, PipelineDef{Owner: user, Name: "Triage", OwningApp: "desk",
		Stages: []PipelineStage{{Name: "a", Kind: StageWorker, Prompt: "Sort it."}}}, "create", "Builder")
	mach, _ := SaveMachineDefBy(udb, MachineDef{Owner: user, Name: "Intake", OwningApp: "desk"}, "create", "Builder")

	page := func(serve func(w *httptest.ResponseRecorder)) string {
		w := httptest.NewRecorder()
		serve(w)
		return w.Body.String()
	}
	for what, body := range map[string]string{
		"agent": page(func(w *httptest.ResponseRecorder) {
			app.renderAgentEditor(w, asUser(httptest.NewRequest("GET", "/orchestrate/agents/edit?id="+agent.ID, nil), user), user, udb, agent.ID)
		}),
		"pipeline": page(func(w *httptest.ResponseRecorder) {
			app.handlePipelinePage(w, asUser(httptest.NewRequest("GET", "/orchestrate/pipeline?id="+pipe.ID, nil), user))
		}),
		"machine": page(func(w *httptest.ResponseRecorder) {
			app.handleMachinePage(w, asUser(httptest.NewRequest("GET", "/orchestrate/machine?id="+mach.ID, nil), user))
		}),
	} {
		if !strings.Contains(body, "Part of the app Help Desk") || !strings.Contains(body, "changes only with Builder or Anvil") {
			t.Errorf("the %s's page does not say it is the app's", what)
		}
		if !strings.Contains(body, "/apps/desk/") || !strings.Contains(body, "intake.target=desk") {
			t.Errorf("the %s's page offers no way to the app or to Builder", what)
		}
		for _, ed := range []string{"api/agents/", "api/pipelines/", "api/machines/", "Sort it."} {
			if strings.Contains(body, ed) {
				t.Errorf("the %s's page still holds its editor (%q)", what, ed)
			}
		}
	}
	mine := page(func(w *httptest.ResponseRecorder) {
		app.renderAgentEditor(w, asUser(httptest.NewRequest("GET", "/orchestrate/agents/edit?id="+own.ID, nil), user), user, udb, own.ID)
	})
	if strings.Contains(mine, "Part of the app") || !strings.Contains(mine, "api/agents/") {
		t.Error("an agent that is no app's does not open its editor")
	}
}

// An app switched off stops what it owns on every way a run starts: one
// agent dispatching it, the app's own Ask, the agents(run) list, and a run
// of its pipeline. Switched back on, they run again; an agent that is no
// app's is never stopped.
func TestAnAppSwitchedOffStopsWhatItOwns(t *testing.T) {
	pinRootDB(t)
	app, udb, user := newTestOrchestrate(t)
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "Answered.", Repeat: true}}}
	app.LLM, app.LeadLLM = fake, fake
	SaveAppSpec(AppSpec{Owner: user, Slug: "desk", Name: "Help Desk", RecordKey: "id", Disabled: true})
	clerk, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Clerk", OrchestratorPrompt: "Answer.", OwningApp: "desk", appEdit: "Builder"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.RunAgentSync(context.Background(), user, user, clerk.ID, "hello"); err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Errorf("a dispatch ran an agent whose app is switched off: %v", err)
	}
	if _, _, err := app.AppAgentAsk(context.Background(), user, "", clerk.ID, "hello", false); err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Errorf("an app's Ask ran an agent whose app is switched off: %v", err)
	}
	if agentRunStopped(AgentRecord{Owner: user, Name: "Mine"}) != nil {
		t.Error("an agent that is no app's was stopped")
	}
	pipe := PipelineDef{Owner: user, Name: "Triage", OwningApp: "desk", Stages: []PipelineStage{{Name: "a", Kind: StageWorker, Prompt: "Sort it."}}}
	if _, err := new(AppCore).RunPipelineDefSync(context.Background(), pipe, "x", nil, nil); err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Errorf("a pipeline whose app is switched off ran: %v", err)
	}

	spec, _ := LoadAppSpec(user, "desk")
	spec.Disabled = false
	SaveAppSpec(spec)
	if _, err := app.RunAgentSync(context.Background(), user, user, clerk.ID, "hello"); err != nil {
		t.Errorf("switched back on, its agent does not run: %v", err)
	}
}

// An app's agent loads its app's own tool space beside the pool (the pool is
// cut for an app when it copies what it uses, piece D): the app's tool wins
// over the pool's of its name, a tool reaching a credential loads once an
// administrator approved it, and an agent of no app sees none of it.
func TestAnAppsAgentLoadsItsAppsTools(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	prevRoot := RootDB
	RootDB = root
	t.Cleanup(func() { RootDB = prevRoot })
	for _, tool := range []TempTool{
		{Name: "get_weather", Description: "the pool's", CommandTemplate: "echo pool"},
		{Name: "pool_only", Description: "d", CommandTemplate: "echo p"},
	} {
		if err := AdminPersistTempTool(udb, "u", tool); err != nil {
			t.Fatal(err)
		}
	}
	apptools.SaveBy("u", "wx", TempTool{Name: "get_weather", Description: "the app's", CommandTemplate: "echo app"}, "Builder")
	crm := TempTool{Name: "crm", Description: "d", Mode: TempToolModeAPI, Credential: "crm", CommandTemplate: "https://crm.invalid/{q}"}
	apptools.SaveBy("u", "wx", crm, "Builder")

	load := func(owningApp string) map[string]string {
		turn := &chatTurn{user: "u", udb: udb, agent: AgentRecord{ID: "a-1", Owner: "u", OwningApp: owningApp}}
		sess := &ToolSession{}
		turn.loadAgentTempTools(sess, "u", root)
		got := map[string]string{}
		for _, tt := range sess.TempTools {
			got[tt.Name] = tt.Description
		}
		return got
	}
	got := load("wx")
	if got["get_weather"] != "the app's" || got["pool_only"] == "" {
		t.Errorf("an app's agent loaded %v", got)
	}
	if _, ok := got["crm"]; ok {
		t.Error("a tool reaching a credential loaded before an administrator approved it")
	}
	apptools.Approve("u", "wx", "admin", map[string]string{"crm": apptools.Fingerprint(crm)})
	if _, ok := load("wx")["crm"]; !ok {
		t.Error("an approved tool did not load")
	}
	got = load("")
	if got["get_weather"] != "the pool's" {
		t.Errorf("an agent of no app loaded %v", got)
	}
	if _, ok := got["crm"]; ok {
		t.Error("an agent of no app loaded an app's tool")
	}
}

// The app's folder holds its tools, each as tools/<name>.json with its script
// beside it, and an install applies them as the folder says; deleting the app
// takes its space, and its confirmation counts the tools.
func TestAnInstallAppliesTheAppsTools(t *testing.T) {
	turn, _ := folderTestTurn(t)
	T, dir := installTestApp(t, turn)
	prevHost := agenthost.Get()
	agenthost.Register(T)
	t.Cleanup(func() { agenthost.Register(prevHost) })
	script := "import os\nprint(os.environ.get('city', ''))\n"
	if err := apptools.SaveBy("u", "wx", TempTool{Name: "get_weather", Description: "Weather.", CommandTemplate: "python3 run.py",
		ScriptName: "run.py", ScriptBody: script, CanonicalScriptName: "get_weather_abc.py"}, "Builder"); err != nil {
		t.Fatal(err)
	}
	if err := WriteAppFolder("u", "wx", dir); err != nil {
		t.Fatal(err)
	}
	def, err := os.ReadFile(filepath.Join(dir, "tools", "get_weather.json"))
	if err != nil || strings.Contains(string(def), "script_body") || strings.Contains(string(def), "canonical") {
		t.Fatalf("the tool's file: %v %s", err, def)
	}
	side := filepath.Join(dir, "tools", "get_weather.py")
	if b, _ := os.ReadFile(side); string(b) != script {
		t.Fatalf("the script beside it: %q", b)
	}
	os.WriteFile(side, []byte(script+"print('done')\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "tools", "uptime.json"), []byte(`{"name": "uptime", "description": "Uptime.", "command_template": "uptime"}`), 0o644)
	out, err := T.InstallAppFolder(context.Background(), "u", dir, "Weather says done; uptime")
	if err != nil || !strings.Contains(out, "tool get_weather") || !strings.Contains(out, "tool uptime") {
		t.Fatalf("install: %v %s", err, out)
	}
	got, _ := apptools.ByName("u", "wx", "get_weather")
	if !strings.HasSuffix(got.Tool.ScriptBody, "print('done')\n") || got.Tool.CanonicalScriptName != "" || got.SavedBy != "Anvil" {
		t.Errorf("the tool after: %+v", got)
	}
	if _, ok := apptools.ByName("u", "wx", "uptime"); !ok {
		t.Error("a new tool file made no tool")
	}
	if pool := LoadPersistentTempTools(nil, "u"); len(pool) != 0 {
		t.Errorf("an install put the app's tools in the pool: %+v", pool)
	}

	os.WriteFile(filepath.Join(dir, "tools", "twice.json"), []byte(`{"name": "uptime", "command_template": "uptime -p"}`), 0o644)
	if _, err := T.InstallAppFolder(context.Background(), "u", dir, "Two files, one name"); err == nil || !strings.Contains(err.Error(), "both name the tool") {
		t.Fatalf("two files naming one tool: %v", err)
	}
	os.Remove(filepath.Join(dir, "tools", "twice.json"))

	if n := AppOwnedCount("u", "wx"); n != "2 tools" {
		t.Errorf("the delete confirmation counts %q", n)
	}
	gone, _ := DeleteAppOwned("u", "wx")
	if strings.Join(gone, ",") != `tool "get_weather",tool "uptime"` || len(apptools.Load("u", "wx")) != 0 {
		t.Errorf("deleting the app took %v", gone)
	}
}

// An app's collection is in its folder as its blueprint (knowledge/<name>.json:
// what it is and its plan, never its documents), and an install applies the
// folder's: a changed file is the collection changed, a new one a new
// collection of the app's. Deleting the app takes them, counted in its
// confirmation.
func TestAnAppsCollectionIsItsBlueprint(t *testing.T) {
	turn, _ := folderTestTurn(t)
	T, dir := installTestApp(t, turn)
	prevHost := agenthost.Get()
	agenthost.Register(T)
	t.Cleanup(func() { agenthost.Register(prevHost) })
	udb := UserDB(T.DB, "u")
	SaveCollectionBy(udb, Collection{ID: "c-1", Owner: "u", Name: "Manual", Description: "The product manual.",
		SearchTerms: []string{"widget manual"}, OwningApp: "wx", IngestedURLs: []string{"https://example.invalid/m"}}, "Builder")
	if err := WriteAppFolder("u", "wx", dir); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "knowledge", "manual.json")
	raw, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(raw), "widget manual") || strings.Contains(string(raw), "example.invalid") || strings.Contains(string(raw), "c-1") {
		t.Fatalf("the collection's file: %v %s", err, raw)
	}
	os.WriteFile(file, []byte(strings.Replace(string(raw), `"name": "Manual",`, `"name": "Manual",
  "filter_rules": "Skip forums.",`, 1)), 0o644)
	os.WriteFile(filepath.Join(dir, "knowledge", "faq.json"), []byte(`{"name": "FAQ", "description": "Questions people ask."}`), 0o644)
	out, err := T.InstallAppFolder(context.Background(), "u", dir, "Rules for the manual; a FAQ")
	if err != nil || !strings.Contains(out, "collection Manual") || !strings.Contains(out, "collection FAQ") {
		t.Fatalf("install: %v %s", err, out)
	}
	c, _ := LoadCollection(udb, "u", "c-1")
	if c.FilterRules != "Skip forums." || len(c.IngestedURLs) != 1 {
		t.Errorf("the collection after: %+v", c)
	}
	var faq Collection
	for _, x := range ListCollections(udb, "u") {
		if x.Name == "FAQ" {
			faq = x
		}
	}
	if faq.OwningApp != "wx" {
		t.Errorf("the new file made no collection of the app's: %+v", faq)
	}
	if n := AppOwnedCount("u", "wx"); n != "2 collections" {
		t.Errorf("the delete confirmation counts %q", n)
	}
	gone, kept := DeleteAppOwned("u", "wx")
	if len(gone) != 2 || len(kept) != 0 {
		t.Errorf("deleting the app: gone %v, kept %v", gone, kept)
	}
	if _, ok := LoadCollection(udb, "u", "c-1"); ok {
		t.Error("the app's collection outlived it")
	}
}

// An app's collection's seed files (the documents added to it by hand) are
// in its folder as their text, rebuilt from what was indexed, its title on
// its first line; a new or changed one is indexed on install, and a document
// whose file is left out is kept and said.
func TestAnAppsCollectionCarriesItsSeedFiles(t *testing.T) {
	turn, _ := folderTestTurn(t)
	T, dir := installTestApp(t, turn)
	prevHost, savedVec := agenthost.Get(), VectorDB
	agenthost.Register(T)
	VectorDB = &DBase{Store: kvlite.MemStore()}
	InvalidateChunkCache()
	t.Cleanup(func() { agenthost.Register(prevHost); VectorDB = savedVec; InvalidateChunkCache() })
	udb := UserDB(T.DB, "u")
	c := Collection{ID: "c-1", Owner: "u", Name: "Manual", OwningApp: "wx"}
	SaveCollectionBy(udb, c, "Builder")
	manual := "The widget turns on with the red switch. Hold it for two seconds until the light shows green, then let go."
	if _, err := pasteIntoCollection(context.Background(), VectorDB, c, "Getting started", manual); err != nil {
		t.Fatal(err)
	}
	if err := WriteAppFolder("u", "wx", dir); err != nil {
		t.Fatal(err)
	}
	seed := filepath.Join(dir, "knowledge", "manual", "getting-started.txt")
	raw, err := os.ReadFile(seed)
	if err != nil || !strings.HasPrefix(string(raw), "# Getting started") || !strings.Contains(string(raw), "red switch") {
		t.Fatalf("the seed file: %v %q", err, raw)
	}

	os.WriteFile(filepath.Join(dir, "knowledge", "manual", "care.txt"), []byte("# Care\n\nWipe the widget with a dry cloth; never put it in water, which ruins the switch."), 0o644)
	out, err := T.InstallAppFolder(context.Background(), "u", dir, "A care page")
	if err != nil || !strings.Contains(out, `Manual's document "Care"`) || strings.Contains(out, `"Getting started"`) {
		t.Fatalf("install: %v %s", err, out)
	}
	if got := collectionSeeds(c); len(got) != 2 || !strings.Contains(got["care.txt"], "dry cloth") {
		t.Errorf("the collection's documents after: %v", got)
	}

	os.Remove(seed)
	out, err = T.InstallAppFolder(context.Background(), "u", dir, "Drop the getting started file")
	if err != nil || !strings.Contains(out, `Not in the folder, and kept: Manual's document "Getting started"`) {
		t.Fatalf("an install with a seed file gone: %v %s", err, out)
	}
	if got := collectionSeeds(c); len(got) != 2 {
		t.Errorf("a document went because its file was left out: %v", got)
	}
}
