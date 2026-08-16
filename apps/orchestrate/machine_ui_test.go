package orchestrate

// The browser-side surfaces for phase machines: the toolbar entry and
// its handler, and the picker on the agent editor. Both are wiring that
// spans two files which know nothing about each other, which is exactly
// the kind of thing that half-lands and looks fine.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
		t.Error("no machine editor — the modal can select and delete but not fix a typo")
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

// The Machines modal could select, edit, diagram and delete — but not
// create. "How do I load a machine" had no answer in the UI at all, and
// an editor that can only edit what already exists is a strange shape
// for the surface people are pointed at first.
func TestMachinesModalCanCreate(t *testing.T) {
	assets, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatalf("read assets: %v", err)
	}
	js := string(assets)

	if !strings.Contains(js, "New machine") {
		t.Error("no way to start a new machine from the modal")
	}
	// Create POSTs to the collection; edit PUTs to the item. One editor,
	// two verbs — a second editor would drift from the first.
	if !strings.Contains(js, "creating ? 'api/machines' : 'api/machines/'") {
		t.Error("the editor should POST when creating and PUT when editing")
	}
	if !strings.Contains(js, "creating ? 'POST' : 'PUT'") {
		t.Error("the method should follow the same switch as the URL")
	}
	// The starter comes from the server, so there is one copy of it and a
	// Go test can prove it validates.
	if !strings.Contains(js, "api/machines?starter=1") {
		t.Error("the starter should be fetched, not written in the JavaScript")
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
