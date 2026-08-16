package orchestrate

// The browser-side surfaces for phase machines: the toolbar entry and
// its handler, and the picker on the agent editor. Both are wiring that
// spans two files which know nothing about each other, which is exactly
// the kind of thing that half-lands and looks fine.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
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
