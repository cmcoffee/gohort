package orchestrate

// The browser-side surfaces for phase machines: the toolbar entry and
// its handler, and the picker on the agent editor. Both are wiring that
// spans two files which know nothing about each other, which is exactly
// the kind of thing that half-lands and looks fine.

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A client action is wired in two files that know nothing about each
// other: the toolbar entry names it in page_chat.go, and the handler
// registers it in web_assets.html. Half of that pair is a button that
// does nothing, which is invisible to every other test here.
func TestMachinesModal_ToolbarEntryAndHandlerAgree(t *testing.T) {
	page, err := os.ReadFile("page_chat.go")
	if err != nil {
		t.Fatalf("read page: %v", err)
	}
	assets, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatalf("read assets: %v", err)
	}
	const action = "orchestrate_machines_modal"
	if !strings.Contains(string(page), `URL: "`+action+`"`) {
		t.Errorf("no toolbar entry points at %s — the modal is unreachable", action)
	}
	if !strings.Contains(string(assets), `uiRegisterClientAction('`+action+`'`) {
		t.Errorf("%s is named by the toolbar but never registered — the button would do nothing", action)
	}
	// It saves through the PARTIAL agent update, not the whole-record
	// form. Posting the whole record from a modal that only shows one
	// field is how a save clobbers everything the modal didn't load.
	if !strings.Contains(string(assets), `JSON.stringify({machine: picked})`) {
		t.Error("the modal should post only the machine field")
	}
	// Editing is reachable from the same row, and PUTs the recipe.
	if !strings.Contains(string(assets), "function openMachineEditor(") {
		t.Error("no machine editor — the modal could select a machine but not fix a typo in one")
	}
	// And it does NOT delete. Every other control on the row is scoped to
	// this agent — the checkbox attaches, the buttons open — so a × that
	// destroyed the machine for every agent running it sat one row-width
	// from a checkbox meaning "use this one". Deleting is managing, and
	// managing lives in Extensions → Machines.
	if strings.Contains(string(assets), "Delete this machine everywhere") {
		t.Error("a per-agent picker should not destroy a machine for every agent")
	}
	if !strings.Contains(string(assets), "Extensions → Machines") {
		t.Error("the modal should say where deleting went, or it reads as a capability that vanished")
	}
	// The verb is now conditional (create POSTs, edit PUTs); the create
	// path is pinned in TestMachinesModalCanCreate.
	if !strings.Contains(string(assets), `'PUT'`) {
		t.Error("the editor should still PUT when editing an existing machine")
	}
}

func TestMachineSelectField_HidesItselfUntilThereIsSomethingToPick(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")

	f := machineSelectField(udb, "u")
	if f.Type != "hidden" {
		t.Errorf("with no machines the picker should render nothing, got type %q", f.Type)
	}

	SaveMachineDef(udb, MachineDef{
		Name: "Triage", Owner: "u", Description: "decompose then answer",
		Phases: []MachinePhase{{Name: "answer", Resident: true, Prompt: "x"}},
	})
	f = machineSelectField(udb, "u")
	if f.Type != "select" || len(f.Options) != 2 {
		t.Fatalf("expected None plus one machine, got type=%q options=%d", f.Type, len(f.Options))
	}
	if f.Options[0].Value != "" {
		t.Error("the first option must be the detach one, or there is no way back to no machine")
	}
	if !strings.Contains(f.Options[1].Label, "Triage") || !strings.Contains(f.Options[1].Label, "decompose then answer") {
		t.Errorf("the option should be identifiable: %q", f.Options[1].Label)
	}
}

// Creating a machine goes to the PAGE from everywhere, including the
// chat modal. The modal used to open the JSON editor on a starter,
// which is the one door that asks a newcomer to read a schema before
// they can begin — while the page offers a starter that already runs, a
// description to draft from, and a recipe to import.
func TestCreatingAMachineGoesToThePage(t *testing.T) {
	assets, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatalf("read assets: %v", err)
	}
	js := string(assets)

	if !strings.Contains(js, "New machine") {
		t.Error("no way to start a new machine from the modal")
	}
	if !strings.Contains(js, "window.open('/orchestrate/machine?new=1'") {
		t.Error("the modal's New machine should open the page that knows three ways to make one")
	}
	// The JSON door is edit-only now, and says so in one verb: a create
	// branch nothing could reach was a second way in that had quietly
	// stopped being one.
	if strings.Contains(js, "creating ?") {
		t.Error("the JSON editor should not carry a create path nothing reaches")
	}
	if !strings.Contains(js, `method: 'PUT',`) {
		t.Error("editing an existing machine PUTs to the item")
	}
	// And the starter still comes from the server wherever it is used, so
	// there is one copy of it and a Go test can prove it validates.
	if !strings.Contains(js, "api/machines?starter=1") && !strings.Contains(js, "?new=1") {
		t.Error("the starter should come from the server, not from JavaScript")
	}
}

// The try panel spans four places that know nothing about each other:
// the panel's markup, the client action the button names, the JS that
// registers under that name, and the route that answers it. Any one of
// them missing leaves a button that looks fine and does nothing.
func TestMachineTryPanel_ButtonMarkupAndRouteAgree(t *testing.T) {
	body, err := json.Marshal(machineTryPanel(MachineDef{ID: "m1", Name: "Investigation",
		Description: "Explain something you are seeing. Then test it."}))
	if err != nil {
		t.Fatal(err)
	}
	panel := string(body)

	if !strings.Contains(panel, `"url":"machine_try"`) || !strings.Contains(panel, `"method":"client"`) {
		t.Errorf("the button does not name the client action: %s", panel)
	}
	// A rehearsal that can only be cleared by reloading the page is half
	// a rehearsal — and the handler for this was registered with nothing
	// naming it for several versions, which is why the pairing is pinned
	// from BOTH sides now (see TestTheWholeEditorPageRenders).
	if !strings.Contains(panel, `"url":"machine_try_reset"`) {
		t.Error("no way to start the rehearsal over")
	}
	if !strings.Contains(panel, `"label":"Send"`) {
		t.Error("the button should read as sending another message, not running once")
	}
	// The JS looks these up by id. A renamed element is a panel that
	// renders and a button that quietly finds nothing.
	for _, id := range []string{"machine-try-msg", "machine-try-out"} {
		if !strings.Contains(panel, `id=\"`+id+`\"`) {
			t.Errorf("the panel has no #%s, which the handler reads", id)
		}
		if !strings.Contains(machineTryJS, "'"+id+"'") {
			t.Errorf("the handler never looks up #%s", id)
		}
	}
	// The placeholder speaks in the machine's own terms rather than
	// asking somebody to guess what the box wants.
	if !strings.Contains(panel, "Explain something you are seeing.") {
		t.Errorf("the box should suggest something from the machine's description: %s", panel)
	}

	page, err := os.ReadFile("machine_page.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), `ClientAction("machine_try", machineTryJS)`) {
		t.Error("machine_try is named by the button but never registered on the page")
	}
	if !strings.Contains(string(page), "machineTryPanel(def)") {
		t.Error("the panel is built but never placed on the page")
	}

	// And the endpoint it posts to is one the router actually serves.
	if !strings.Contains(machineTryJS, "/try'") {
		t.Error("the handler does not post to the try endpoint")
	}
	routes, err := os.ReadFile("machines_http.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(routes), `case "try":`) {
		t.Error("nothing serves /try")
	}
}

// The picture's boxes are doors. Three parts have to agree: the SVG is
// INLINE (links inside an <img> SVG are inert), each node links to a
// section anchor, and the anchor is the same slug the section rail
// computes from its titles — SectionSlug and secnavSlug are one
// transform in two languages, and this is where they are held together.
func TestThePictureNavigatesTheRail(t *testing.T) {
	def := MachineDef{Name: "m", Start: "triage", Phases: []MachinePhase{
		{Name: "triage", Prompt: "p", Next: "log check"},
		{Name: "log check", Prompt: "p", Resident: true},
	}}
	svg := machineGraphSVG(def)
	for _, want := range []string{`href="#triage"`, `href="#log-check"`} {
		if !strings.Contains(svg, want) {
			t.Errorf("each step should link to its section, missing %s:\n%s", want, svg)
		}
	}

	page, err := os.ReadFile("machine_page.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(page), `<img src="/orchestrate/api/machines/`) {
		t.Error("the picture is an <img> again — its links are inert there")
	}

	// The two slug transforms must be the same function or a link lands
	// nowhere. The JS side is pinned by string; the Go side by value.
	runtime, err := os.ReadFile("../../core/ui/assets/runtime/99_epilogue.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(runtime), "replace(/[^a-z0-9]+/g, '-')") {
		t.Error("secnavSlug changed shape — keep it matching ui.SectionSlug")
	}
	for in, want := range map[string]string{
		"Try it": "try-it", "log check": "log-check", "The machine": "the-machine", "  odd--name  ": "odd-name",
	} {
		if got := ui.SectionSlug(in); got != want {
			t.Errorf("SectionSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

// The panel test above pins strings; this one RUNS the handler. The
// review found machineTryJS referencing cursor/turnNo that a botched
// edit had left undeclared — a synchronous ReferenceError on the first
// click, invisible to every string check. Executes under node with a
// stub DOM up to the fetch; skips where node is not installed.
func TestMachineTryJSActuallyExecutes(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	harness := `
var calledFetch = false;
var stubEl = function() { return {
  textContent: '', value: 'why is it slow?', dataset: {}, style: {},
  setAttribute: function() {}, appendChild: function() {}, focus: function() {},
  querySelectorAll: function() { return []; },
}; };
var document = {
  getElementById: function() { return stubEl(); },
  createElement: function() { return stubEl(); },
  createTextNode: function(t) { return {text: t}; },
};
var window = {location: {search: '?id=m1'}};
var URLSearchParams = function() { return {get: function() { return 'm1'; }}; };
var fetch = function() { calledFetch = true; return {then: function() { return this; }, catch: function() { return this; }}; };
var fn = ` + machineTryJS + `;
fn({button: null});
if (!calledFetch) { throw new Error('never reached the fetch — something threw or returned early'); }
console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "try.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("machineTryJS threw before the fetch:\n%s", out)
	}
}

// Export and Duplicate existed as endpoints but were unreachable from
// the page where machines are authored — the portable recipe and the
// safe-experiment copy both lived somewhere else. Wiring like this
// spans two files, so pin the pair.
func TestTheEditorOffersExportAndDuplicate(t *testing.T) {
	page, err := os.ReadFile("machine_page.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(page)
	if !strings.Contains(src, `/export"`) {
		t.Error("the editor has no way to download the recipe")
	}
	if !strings.Contains(src, `URL:    "machine_duplicate"`) {
		t.Error("the editor has no way to copy the machine")
	}
	if !strings.Contains(src, `ClientAction("machine_duplicate", machineDuplicateJS)`) {
		t.Error("machine_duplicate is named by a button but never registered — the button would do nothing")
	}
	// Duplicating opens the COPY: staying on the original is the wrong
	// half of the action.
	if !strings.Contains(machineDuplicateJS, "window.location.href = '/orchestrate/machine?id='") {
		t.Error("duplicate should land in the copy")
	}
	// And the endpoint it posts to is one the router serves.
	routes, err := os.ReadFile("machines_http.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`case "duplicate":`, `case "export":`} {
		if !strings.Contains(string(routes), want) {
			t.Errorf("nothing serves %s", want)
		}
	}
}

// The rehearsal is multi-turn, and the only way to see that without a
// browser is to drive the handler. This runs it twice against a stub
// DOM: the second message must CONTINUE the first (carrying the cursor
// back, appending rather than replacing), and Start over must forget
// it. Both halves shipped broken once — the cursor variable undeclared,
// then the reset button missing — and neither showed up in a test that
// only read the source.
func TestMachineTryJSHoldsAConversation(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	harness := `
function makeEl(tag) {
  return {
    tag: tag, children: [], attrs: {}, dataset: {}, style: {}, textContent: '', value: '',
    setAttribute: function(k, v) { this.attrs[k] = v; },
    appendChild: function(c) { this.children.push(c); return c; },
    focus: function() {},
    querySelector: function() { return null; },
    querySelectorAll: function() {
      return this.children.filter(function(c) { return c.attrs && c.attrs['data-try-turn'] !== undefined; });
    },
  };
}
var box = makeEl('input'), out = makeEl('div');
var document = {
  getElementById: function(id) { return id === 'machine-try-msg' ? box : out; },
  createElement: makeEl,
  createTextNode: function(t) { return {text: t}; },
};
var window = {location: {search: '?id=m1'}};
var URLSearchParams = function() { return {get: function() { return 'm1'; }}; };

var sent = [];
var fetch = function(url, opts) {
  sent.push(JSON.parse(opts.body));
  return Promise.resolve({
    ok: true,
    json: function() {
      return Promise.resolve({landed: 'answer', path: [{from: 'triage', to: 'answer'}],
                              cursor: {phase: 'answer', log: [{from: 'triage', to: 'answer'}]}});
    },
  });
};

var send = ` + machineTryJS + `;
var reset = ` + machineTryResetJS + `;

box.value = 'why is the export failing?';
send({button: null});
setTimeout(function() {
  box.value = 'any update?';
  send({button: null});
  setTimeout(function() {
    if (sent.length !== 2) throw new Error('expected two requests, got ' + sent.length);
    if (sent[0].cursor) throw new Error('the first message must start fresh');
    if (!sent[1].cursor || sent[1].cursor.phase !== 'answer') {
      throw new Error('the second message must carry the rehearsal position back');
    }
    var turns = out.children.filter(function(c) { return c.attrs['data-try-turn'] !== undefined; });
    if (turns.length !== 2) throw new Error('each message needs its own block, got ' + turns.length);
    if (turns[1].attrs['data-try-turn'] !== '2') throw new Error('the second block should be turn 2');
    reset({});
    if (out.dataset.cursor) throw new Error('Start over must forget the position');
    console.log('OK');
  }, 20);
}, 20);
`
	tmp := filepath.Join(t.TempDir(), "rehearsal.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("the rehearsal does not hold a conversation:\n%s", out)
	}
}

// Every piece of JS this feature ships lives in a Go string, so nothing
// parses it until a browser does. A syntax error there is a button that
// silently does nothing — the same failure as a handler nobody
// registers, arriving by a different road.
func TestEveryMachineScriptParses(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	// Read the scripts out of the SOURCE rather than listing them here.
	// A hand-kept list is a second thing to forget, and it had already
	// been forgotten three times over — machine_repair, machine_undo and
	// the map refresh all shipped uncovered while this test passed.
	// Every machine_*.go, not just the page: the try panel keeps its
	// scripts next to the handler they drive, and scoping this to one
	// file is how three of them shipped uncovered.
	files, err := filepath.Glob("machine_*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no machine sources: %v", err)
	}
	decl := regexp.MustCompile("(?s)const (machine[A-Za-z]*JS) = `(.*?)`\n")
	var found [][]string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, rerr := os.ReadFile(f)
		if rerr != nil {
			t.Fatal(rerr)
		}
		found = append(found, decl.FindAllStringSubmatch(string(src), -1)...)
	}
	if len(found) < 11 {
		t.Fatalf("expected every machine script, found %d — the declaration shape changed", len(found))
	}
	// Enough of a browser for a top-level statement to attach itself to.
	// Function expressions are wrapped instead; nothing here RUNS, so a
	// stub only has to satisfy the parser and the outermost call.
	const stubs = "var document = {addEventListener: function(){}, querySelector: function(){}, " +
		"querySelectorAll: function(){ return []; }};\n" +
		"var window = {addEventListener: function(){}, location: {search: ''}};\n"
	scripts := map[string]string{}
	for _, m := range found {
		name, body := m[1], m[2]
		if strings.HasPrefix(strings.TrimSpace(body), "function(") {
			scripts[name] = "var f = " + body + ";"
		} else {
			scripts[name] = stubs + body
		}
	}
	dir := t.TempDir()
	for name, src := range scripts {
		path := filepath.Join(dir, name+".js")
		if err := os.WriteFile(path, []byte(src), 0644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("node", "--check", path).CombinedOutput(); err != nil {
			t.Errorf("%s does not parse:\n%s", name, out)
		}
	}
}

// Parsing proves a script is JavaScript; it proves nothing about the
// names it calls. An app's script reaches the framework through
// window.<something>, and a misspelling there (window.uiClientActions
// for window.UIClientActions — a real one, caught by hand this time) is
// a button that parses, registers, runs, and does nothing.
//
// So: every window.* an app script names must be either a browser
// global or something the runtime actually defines.
func TestMachineScriptsOnlyCallFrameworkNamesThatExist(t *testing.T) {
	browserGlobals := map[string]bool{
		"location": true, "addEventListener": true, "removeEventListener": true,
		"CSS": true, "open": true, "document": true, "console": true,
		"setTimeout": true, "clearTimeout": true, "fetch": true, "alert": true,
		"URLSearchParams": true, "getComputedStyle": true, "scrollTo": true,
	}
	runtime := assembledRuntimeForTest(t)
	scripts := map[string]string{
		"machineTryJS": machineTryJS, "machineTryResetJS": machineTryResetJS,
		"machineRemoveStepJS": machineRemoveStepJS, "machineMoveStepJS": machineMoveStepJS,
		"machineDuplicateJS": machineDuplicateJS, "machineTryEnterJS": machineTryEnterJS,
		"machinePreviewRefreshJS": machinePreviewRefreshJS,
	}
	ref := regexp.MustCompile(`window\.([A-Za-z_][A-Za-z0-9_]*)`)
	for name, src := range scripts {
		for _, m := range ref.FindAllStringSubmatch(src, -1) {
			called := m[1]
			if browserGlobals[called] {
				continue
			}
			// The runtime defines its exports as window.X = …
			if !strings.Contains(runtime, "window."+called+" =") &&
				!strings.Contains(runtime, "window."+called+"=") {
				t.Errorf("%s calls window.%s, which the runtime never defines — a button that runs and does nothing", name, called)
			}
		}
	}
}

// assembledRuntimeForTest concatenates the runtime the browser gets.
// Read from disk rather than imported: the assembled string is
// unexported in core/ui, and a test that reads what actually ships is
// the more honest of the two anyway.
func assembledRuntimeForTest(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "core", "ui", "assets", "runtime")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".js") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
	}
	return b.String()
}

// The map is pinned above the steps, not parked in a section of its
// own: SectionNav shows ONE section at a time, so a picture in a
// section can never be on screen with the step being edited — and a
// split is exactly what a step's own form cannot show, since it lists
// the names it may choose between while the shape lives in the arrows.
func TestTheMapIsPinnedAboveTheSteps(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{Owner: user, Name: "M", Start: "triage",
		Phases: []MachinePhase{
			{Name: "triage", Prompt: "decide", Choices: []string{"dig", "answer"}, Next: "answer"},
			{Name: "dig", Prompt: "look", Next: "answer"},
			{Name: "answer", Prompt: "reply", Resident: true},
		}})

	r := httptest.NewRequest("GET", "/orchestrate/machine?id="+def.ID, nil)
	w := httptest.NewRecorder()
	app.handleMachinePage(w, asUser(r, user))
	body := w.Body.String()

	if !strings.Contains(body, `"sticky"`) {
		t.Fatal("the map should be pinned, not parked in a section")
	}
	if !strings.Contains(body, "machine-map") {
		t.Error("no map on the page")
	}
	// Every box is a door, and every box can be lit.
	for _, want := range []string{`data-node=\"triage\"`, `href=\"#dig\"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the map is missing %s", want)
		}
	}
	// The split itself: a deciding step leaves by more than one arrow.
	if n := strings.Count(body, "stroke-dasharray"); n < 2 {
		t.Errorf("a step choosing between two should show two run-time arrows, found %d dashed", n)
	}
	// You-are-here is driven by the URL, which is the same thing the
	// section rail navigates by — one source of truth for "where am I".
	if !strings.Contains(body, "hashchange") || !strings.Contains(body, "classList.toggle('here'") {
		t.Error("the map should light the step whose section is open")
	}
	// And it is not collapsible. The steps below it are a form per box;
	// hiding the shape leaves a list of forms whose relationship to each
	// other is invisible, which is what the map was built to replace.
	if strings.Contains(body, "details class=\\\"machine-map") {
		t.Error("the map should not offer to hide itself")
	}
	// Centred, so the diagram sits under the page rather than hugging an
	// edge of a card it does not fill.
	if !strings.Contains(body, "justify-content: center") {
		t.Error("the map should be centred in its card")
	}
}

// A branch has to be visible in the STEPS, not only in the map. A flat
// rail draws two steps a decision picks between exactly like two steps
// that run one after the other, which is the one distinction that list
// most needs to make.
func TestTheStepListShowsABranch(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{Owner: user, Name: "M", Start: "triage",
		Phases: []MachinePhase{
			{Name: "triage", Prompt: "decide", Choices: []string{"dig", "answer"}, Next: "answer"},
			{Name: "dig", Prompt: "look", Next: "answer"},
			{Name: "answer", Prompt: "reply", Resident: true},
		}})

	// The two destinations are alternatives; the decider is not.
	forks := branchAlternatives(def)
	if len(forks["dig"]) != 1 || forks["dig"][0] != "triage" {
		t.Errorf("dig is one of triage's ways: %v", forks)
	}
	if len(forks["triage"]) != 0 {
		t.Errorf("the step doing the choosing is not an alternative to itself: %v", forks)
	}

	r := httptest.NewRequest("GET", "/orchestrate/machine?id="+def.ID, nil)
	w := httptest.NewRecorder()
	app.handleMachinePage(w, asUser(r, user))
	body := w.Body.String()
	if !strings.Contains(body, `"indent":1`) {
		t.Error("the alternatives should be nested under the step that chooses them")
	}
	if !strings.Contains(body, "one of the ways triage can go") {
		t.Error("and the step itself should say what it is an alternative to")
	}

	// A chain must NOT look forked: one destination is a sequence.
	chain := SaveMachineDef(udb, MachineDef{Owner: user, Name: "C", Start: "one",
		Phases: []MachinePhase{
			{Name: "one", Prompt: "p", Next: "two"},
			{Name: "two", Prompt: "p", Resident: true},
		}})
	if got := branchAlternatives(chain); len(got) != 0 {
		t.Errorf("a step that hands off to one place is a sequence, not a branch: %v", got)
	}
}

// The map is rendered server-side, so an edit that changes the SHAPE
// without reloading the page leaves it describing the machine as it was
// a moment ago. Ticking a choice is exactly that edit: it adds an
// arrow, and it is made while looking at the picture that should show
// it.
func TestTheMapRedrawsWhenAStepChangesShape(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{Owner: user, Name: "M", Start: "triage",
		Phases: []MachinePhase{
			{Name: "triage", Prompt: "decide", Next: "answer"},
			{Name: "dig", Prompt: "look", Next: "answer"},
			{Name: "answer", Prompt: "reply", Resident: true},
		}})

	// The endpoint the refresh calls draws the MAP — anchors and all —
	// not the plain picture, or a redraw would quietly cost every box
	// its link.
	r := httptest.NewRequest("GET", "/api/machines/"+def.ID+"/graph?links=1", nil)
	w := httptest.NewRecorder()
	app.handleMachineOne(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("graph fetch failed: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `href="#dig"`) {
		t.Error("the refreshed map should keep its links")
	}
	// And the plain endpoint stays plain — it is what a standalone
	// image or a saved copy gets.
	r = httptest.NewRequest("GET", "/api/machines/"+def.ID+"/graph", nil)
	w = httptest.NewRecorder()
	app.handleMachineOne(w, asUser(r, user))
	if strings.Contains(w.Body.String(), `href="#dig"`) {
		t.Error("anchors only mean something inside the page that has those sections")
	}

	// Adding a choice adds an arrow: what the redraw exists to show.
	before := strings.Count(machineGraphSVG(def), "stroke-dasharray")
	def.Phases[0].Choices = []string{"dig", "answer"}
	if after := strings.Count(machineGraphSVG(def), "stroke-dasharray"); after <= before {
		t.Errorf("ticking a choice should change the picture: %d then %d", before, after)
	}

	// The listener keys off the same broadcast the preview uses, and
	// coalesces: a checklist fires one save per box.
	page, err := os.ReadFile("machine_page.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(page)
	if !strings.Contains(src, "graph?links=1") {
		t.Error("the map never refetches itself")
	}
	if !strings.Contains(src, "clearTimeout(pending)") {
		t.Error("three ticks should not be three fetches of the same picture")
	}
	if !strings.Contains(src, "body.innerHTML = svg;\n          mark();") {
		t.Error("a redrawn map should light the step you are on again")
	}
}

// The machine's own fields move the map too: "Starts at" is the entry
// the layout is ranked FROM, so changing it can rearrange every box.
// And the page is titled with the machine's name, as is the browser
// tab, so renaming it without a rebuild leaves both showing the old one.
func TestTheMachinesOwnFieldsKeepThePageTrue(t *testing.T) {
	_, _, _, def := editorFixture(t)
	raw, _ := json.Marshal(metaPanel(def, "api/machines/"+def.ID))
	meta := string(raw)
	if !strings.Contains(meta, `"reload_on_change":true`) {
		t.Error("renaming the machine should rebuild the page it titles")
	}
	// Only the name: the description and the start do not retitle
	// anything, and a reload on each would be gratuitous.
	if strings.Count(meta, `"reload_on_change":true`) != 1 {
		t.Errorf("only the name needs a rebuild: %s", meta)
	}

	page, err := os.ReadFile("machine_page.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), "'/meta'") {
		t.Error("a meta save should redraw the map — the start is what the layout ranks from")
	}

	// And that claim is true of the graph, not just of the comment: the
	// entry is drawn differently, and the ranking begins there.
	from := MachineDef{Name: "m", Start: "triage", Phases: []MachinePhase{
		{Name: "triage", Prompt: "p", Next: "answer"},
		{Name: "answer", Prompt: "p", Resident: true},
	}}
	other := from
	other.Start = "answer"
	if machineGraphSVG(from) == machineGraphSVG(other) {
		t.Error("changing where a machine starts should change its map")
	}
}

// The checklist is the list somebody works against, fixing one thing at
// a time — the one place staleness actually costs something, because it
// said "3 to fix" until a reload however many had been fixed. It lives
// in the section BODY now so it can be kept true, and the browser words
// it exactly as the server does.
func TestTheChecklistStaysTrueWhileYouFixThings(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{Owner: user, Name: "M", Start: "triage",
		Phases: []MachinePhase{
			{Name: "triage", Prompt: "decide"}, // goes nowhere: one problem
			{Name: "answer", Prompt: "reply", Resident: true},
		}})

	r := httptest.NewRequest("GET", "/orchestrate/machine?id="+def.ID, nil)
	w := httptest.NewRecorder()
	app.handleMachinePage(w, asUser(r, user))
	body := w.Body.String()
	for _, want := range []string{"data-machine-checklist", "data-machine-advice"} {
		if !strings.Contains(body, want) {
			t.Errorf("the %s should be addressable, or it can never be refreshed", want)
		}
	}
	if !strings.Contains(body, "1 to fix") {
		t.Error("the page should show the outstanding work it found")
	}

	// The refresh reads the endpoint that already computes both lists.
	page, err := os.ReadFile("machine_page.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(page)
	if !strings.Contains(src, "/editor'") {
		t.Error("nothing refetches the lists")
	}

	// Both wordings exist twice — Go and JS — so they are pinned
	// together: a refresh phrased differently reads as the page changing
	// its mind rather than as the same list one item shorter.
	clean := MachineDef{Name: "ok", Start: "s", Phases: []MachinePhase{{Name: "s", Prompt: "p", Resident: true}}}
	goneClean := checklistHTML(clean, clean.Problems())
	if !strings.Contains(src, "Nothing outstanding — this machine will run as written.") ||
		!strings.Contains(goneClean, "Nothing outstanding — this machine will run as written.") {
		t.Error("the empty-checklist sentence should be the same on both sides")
	}
	if !strings.Contains(src, "' to fix'") || !strings.Contains(checklistHTML(def, def.Problems()), " to fix") {
		t.Error("the counted form should be the same on both sides")
	}
	if !strings.Contains(src, "the steps read as instructions rather than specifications.") ||
		!strings.Contains(adviceHTML(nil, "u", clean), "the steps read as instructions rather than specifications.") {
		t.Error("the empty-advice sentence should be the same on both sides")
	}

	// And the SHAPE is pinned with the wording. Each finding runs to
	// several lines of prose, so joining them into one paragraph with
	// inline bullets made three findings read as one wall — the page
	// draws <li> per finding, and so must the refresh.
	if html := checklistHTML(def, def.Problems()); !strings.Contains(html, "<li>") || strings.Contains(html, " • ") {
		t.Error("the page should draw one finding per line")
	}
	if !strings.Contains(src, "ul.className = 'machine-findings'") ||
		!strings.Contains(src, "createElement('li')") {
		t.Error("the refresh should rebuild the same list, not a joined line")
	}
	// Built with textContent: a finding quotes step names, and a step
	// named by somebody else is not markup.
	if !strings.Contains(src, "li.textContent = it") {
		t.Error("findings are text, not HTML")
	}
}

// Both doors, on the page where machines are kept. Export was reachable
// only from inside a machine — though the reason to take a copy is
// usually that you are about to change it — and import was reachable
// from nowhere at all.
func TestTheMachinesListCarriesInAndOut(t *testing.T) {
	_, _, user := newTestOrchestrate(t)
	// The access gate is not what this test is about.
	adminAuth(t, user)
	sec, ok := machinesExtensionSection(asUser(httptest.NewRequest("GET", "/gateways", nil), user), user)
	if !ok {
		t.Fatal("the section did not build")
	}
	raw, _ := json.Marshal(sec)
	body := string(raw)

	// In: a file field, landing in the imported machine's editor.
	if !strings.Contains(body, "/api/machines/import") {
		t.Error("no way to bring a machine in")
	}
	if !strings.Contains(body, `"type":"file"`) || !strings.Contains(body, `"accept":".json"`) {
		t.Error("importing should be picking the file somebody was handed")
	}
	if !strings.Contains(body, `"redirect_url":"/orchestrate/machine?id={id}"`) {
		t.Error("an import should land in the machine it made, like Draft and Duplicate do")
	}

	// Out: on the row, as a navigation — the endpoint answers with a
	// Content-Disposition, so the browser downloads and the page stays.
	if !strings.Contains(body, `"post_to":"/orchestrate/api/machines/{id}/export"`) {
		t.Error("no way to take a machine out from the list")
	}
	if !strings.Contains(body, `"label":"Export","post_to":"/orchestrate/api/machines/{id}/export","method":"GET"`) &&
		!strings.Contains(body, `"method":"GET"`) {
		t.Error("export should navigate rather than fetch-and-discard")
	}
}

// A finding you cannot act on. When a step is deleted, RemoveStep drops
// every reference to it — but a machine that arrived any other way (an
// import, an older save, the tool) can name a step that is not there,
// and then the picker offering targets no longer offers that name. The
// checklist says "next names unknown step" and there is nothing on the
// page to clear it.
//
// So the panel that reports it fixes it, scoped to itself and only for
// findings with exactly one right answer.
func TestAFindingYouCannotActOnHasAButton(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{Owner: user, Name: "M", Start: "triage",
		Phases: []MachinePhase{
			{Name: "triage", Prompt: "decide", Next: "testing"},
			{Name: "answer", Prompt: "reply", Resident: true},
		}})

	r := httptest.NewRequest("GET", "/orchestrate/machine?id="+def.ID, nil)
	w := httptest.NewRecorder()
	app.handleMachinePage(w, asUser(r, user))
	body := w.Body.String()
	if !strings.Contains(body, "machine_repair") || !strings.Contains(body, "Fix it") {
		t.Fatal("the checklist reports a dangling reference with no way to settle it")
	}
	// Labelled with what it will do, before it does it.
	if !strings.Contains(body, "Applies exactly these") || !strings.Contains(body, "testing") {
		t.Error("the button should say what it changes")
	}

	// And the endpoint settles it.
	r = httptest.NewRequest("POST", "/api/machines/"+def.ID+"/repair?kind=problems", nil)
	w = httptest.NewRecorder()
	app.handleMachineOne(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("repair: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "testing") {
		t.Errorf("the response should name what it changed: %s", w.Body.String())
	}
	stored, _ := LoadMachineDef(udb, user, def.ID)
	if strings.TrimSpace(stored.Phases[0].Next) != "" {
		t.Errorf("the reference survived the repair: %q", stored.Phases[0].Next)
	}
	for _, p := range stored.Problems() {
		if strings.Contains(p, "testing") {
			t.Errorf("still reported: %s", p)
		}
	}

	// A clean machine offers no button — a fix-it that fixes nothing is
	// the same lie as a control that does nothing.
	clean := SaveMachineDef(udb, MachineDef{Owner: user, Name: "C", Start: "s",
		Phases: []MachinePhase{{Name: "s", Prompt: "p", Resident: true}}})
	r = httptest.NewRequest("GET", "/orchestrate/machine?id="+clean.ID, nil)
	w = httptest.NewRecorder()
	app.handleMachinePage(w, asUser(r, user))
	if strings.Contains(w.Body.String(), "Fix it") {
		t.Error("a machine with nothing to fix should offer no fix")
	}
}

// A finding names a step, and several of them end in an instruction to
// go to it — tick its tools, set next, turn on resident. The rail shows
// one section at a time, so a finding that names a step without linking
// to it asks somebody to do the navigation twice.
func TestAFindingLinksToTheStepItIsAbout(t *testing.T) {
	def := MachineDef{Name: "M", Start: "log check", Phases: []MachinePhase{
		// A transient step that goes nowhere, and one whose name is a
		// prefix of the other's — longest match or the link lands wrong.
		{Name: "log", Prompt: "look"},
		// Reach "none" plus instructions that send it looking is what
		// the advisory catches, and the link to it is what is under test.
		{Name: "log check", Prompt: "search the logs", Next: "answer", Reach: ReachNone},
		{Name: "answer", Prompt: "reply", Resident: true},
	}}
	html := checklistHTML(def, def.Problems())
	if !strings.Contains(html, `href="#log"`) {
		t.Errorf("the finding about \"log\" should open it:\n%s", html)
	}
	if !strings.Contains(adviceHTML(nil, "u", def), `href="#log-check"`) {
		t.Errorf("and the advice about \"log check\" should open THAT step:\n%s", adviceHTML(nil, "u", def))
	}
	// The label is only the step, not the whole finding.
	if !strings.Contains(html, `>step log</a>`) {
		t.Errorf("the link should be the step name, not the sentence:\n%s", html)
	}
	// A finding about the machine rather than a step stays plain text.
	wholeDef := MachineDef{Name: "M", Phases: []MachinePhase{{Name: "s", Prompt: "p"}}}
	whole := checklistHTML(wholeDef, wholeDef.Problems())
	if strings.Contains(whole, "<a") && !strings.Contains(whole, "step s") {
		t.Errorf("a machine-level finding has no step to open:\n%s", whole)
	}
	// The refresh applies the same rule, from the step names in the map
	// it just redrew — one list of steps on the page, not two.
	src, err := os.ReadFile("machine_page.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"function stepNames()", "data-node", "function findingStep(names, line)",
		"machine-finding-step"} {
		if !strings.Contains(string(src), want) {
			t.Errorf("the live refresh is missing %q", want)
		}
	}
}

// The finding whose fix is prose gets a draft-and-review, not a silent
// edit. The case that decides the design is a step carrying TWO findings
// at once — "the prompt asks for JSON" and "the instructions send it
// looking, but it names no tools" — where only the first is a rewrite
// and the second is answered by ticking a tool.
func TestTheProseFindingOffersARewrite(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{Owner: user, Name: "M", Start: "decide_approach",
		Phases: []MachinePhase{
			{Name: "decide_approach", Next: "answer",
				Prompt: "Go and look at the logs. Respond only with valid JSON: {\"lane\": \"...\"}",
				Output: []PipelineField{{Name: "lane", Type: FieldString, Desc: "which lane"}}},
			{Name: "answer", Prompt: "reply", Resident: true},
		}})

	r := httptest.NewRequest("GET", "/orchestrate/machine?id="+def.ID, nil)
	w := httptest.NewRecorder()
	app.handleMachinePage(w, asUser(r, user))
	body := w.Body.String()

	// Exactly one button, on exactly one of that step's two findings.
	if n := strings.Count(body, "machine-finding-rewrite\\\" data-rewrite-step"); n != 1 {
		t.Fatalf("expected one rewrite button, found %d — the step has two findings and only one is prose", n)
	}
	if !strings.Contains(body, "data-rewrite-step=\\\"decide_approach\\\"") {
		t.Error("the button should name the step it rewrites")
	}
	// It drives the SAME assist endpoint the step's own suggest button
	// does. A second endpoint would be a second set of rules about what a
	// step's instructions may say, and the two would drift.
	src, err := os.ReadFile("machine_page.go")
	if err != nil {
		t.Fatal(err)
	}
	js := string(src)
	for _, want := range []string{"uiOpenAssist", "'/suggest'", "field: 'prompt'", "ask:", "onAccept:"} {
		if !strings.Contains(js, want) {
			t.Errorf("the rewrite flow is missing %q", want)
		}
	}
	// Accepting saves through the phase form's own endpoint, so one place
	// still decides what a save does to a step.
	if !strings.Contains(js, "'/phases?name=' + encodeURIComponent(step)") {
		t.Error("accepting a draft should save the way the form does")
	}
	// Delegated, because both findings lists are rebuilt on every save —
	// a handler bound to the button would survive exactly one edit.
	if !strings.Contains(js, "closest('[data-rewrite-step]')") {
		t.Error("the handler should be delegated, or it dies with the first redraw")
	}
	// And the live refresh redraws the button.
	if !strings.Contains(js, "btn.setAttribute('data-rewrite-step'") {
		t.Error("a save would drop the button until the next reload")
	}

	// A machine with no prose finding offers no rewrite.
	clean := SaveMachineDef(udb, MachineDef{Owner: user, Name: "C", Start: "s",
		Phases: []MachinePhase{{Name: "s", Prompt: "reply plainly", Resident: true}}})
	r = httptest.NewRequest("GET", "/orchestrate/machine?id="+clean.ID, nil)
	w = httptest.NewRecorder()
	app.handleMachinePage(w, asUser(r, user))
	// The handler ships on every page; the BUTTON is what must be absent.
	if strings.Contains(w.Body.String(), "machine-finding-rewrite\\\" data-rewrite-step") {
		t.Error("nothing to rewrite, so nothing should offer to")
	}

	// The refresh endpoint carries the set, or the buttons vanish on save.
	r = httptest.NewRequest("GET", "/api/machines/"+def.ID+"/editor", nil)
	w = httptest.NewRecorder()
	app.handleMachineOne(w, asUser(r, user))
	if !strings.Contains(w.Body.String(), `"rewrites"`) {
		t.Error("the editor spec should say which findings can be rewritten")
	}
}

// This app builds its own dialog (openOrchModal) alongside the
// framework's, and both used to pin themselves at z-index 1000 — so a
// dialog opened from inside a dialog sat on the same layer as the one
// it came from, with the first still visible underneath. The framework
// hands out layers now; sharing that counter is what keeps the two
// implementations on one stack, and keeping a second counter here would
// be the same bug with more code.
func TestTheAppsOwnModalSharesTheFrameworksStack(t *testing.T) {
	assets, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatalf("read assets: %v", err)
	}
	js := string(assets)
	if !strings.Contains(js, "window.uiNextModalZ()") {
		t.Error("openOrchModal pins its own layer instead of taking one from the framework")
	}
	if !strings.Contains(js, "window.uiReleaseModalZ()") {
		t.Error("and never gives the layer back, so the stack only ever climbs")
	}
	// Escape closes the top dialog only — every open modal registers its
	// own listener, so one press used to dismiss the whole stack.
	if !strings.Contains(js, "querySelectorAll('[data-ui-modal]')") {
		t.Error("Escape does not check whether another dialog is above this one")
	}
}

// Neither drafting door is a dialog any more, and the reason is
// structural rather than cosmetic.
//
// ui.ModalButton opens a native <dialog> with showModal(), which renders
// in the browser's TOP LAYER — above every z-index there is. So every
// surface the framework uses to report a failure (the toast; another
// modal) lands underneath it and cannot be seen. A submit that failed in
// there reset its button and told you nothing, which is exactly what a
// button that does nothing looks like. Raising the toast's z-index, as
// v0.6.216 did, could not have fixed it: no z-index beats the top layer.
//
// Drafting runs a model for up to a minute and can come back with
// nothing usable, so it is precisely the door that has to be able to say
// so. On a page the form is just a form: its errors render inline.
func TestNeitherDraftingDoorIsADialog(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{Owner: user, Name: "M", Start: "s",
		Phases: []MachinePhase{{Name: "s", Prompt: "p", Resident: true}}})
	adminAuth(t, user)

	// The editor's own door is a section, posting to revise.
	editor := renderMachinePage(t, app, user, def.ID)
	at := strings.Index(editor, "Describe a change")
	if at < 0 {
		t.Fatal("the editor lost its describe-a-change form")
	}
	if strings.Contains(editor[max0(at-400):at+400], "modal_button") {
		t.Error("describe a change is still a dialog — its failures would be invisible")
	}
	if !strings.Contains(editor, `"post_url":"api/machines/`+def.ID+`/revise"`) {
		t.Error("the form does not post to revise")
	}

	// The list's door is a link to a page.
	r := httptest.NewRequest("GET", "/orchestrate/machine?describe=1", nil)
	w := httptest.NewRecorder()
	app.handleMachinePage(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("the describe page does not render: %d", w.Code)
	}
	page := w.Body.String()
	if !strings.Contains(page, `"post_url":"/orchestrate/api/machines/draft"`) {
		t.Error("the describe page does not post to the draft endpoint")
	}
	if !strings.Contains(page, `"redirect_url":"/orchestrate/machine?id={id}"`) {
		t.Error("a drafted machine should land in the editor")
	}
	if strings.Contains(page, "modal_button") {
		t.Error("the describe page should not open a dialog to do its own job")
	}
	// And the ways in that do NOT need a model are on it, because
	// somebody who lands here and does not want to describe anything
	// should not have to go back to find them.
	for _, want := range []string{"machine?new=1", "New machine"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
}
