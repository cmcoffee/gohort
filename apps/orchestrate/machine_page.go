// Machines on the Extensions page, and the editor as a PAGE rather than
// a modal.
//
// A machine is a reusable thing a person owns, like a tool or a
// credential — not a property of the conversation they happen to have
// open. Extensions is where those already live, so it registers a
// section there (core/sections) rather than the Extensions page learning
// what a machine is.
//
// The editor moved out of the modal because the modal was the
// constraint. A phase has a prompt, a routing rule, a list of declared
// fields and a guard; four phases is a page. Squeezed into a dialog it
// reads as a form to get through, which is the opposite of building an
// idea out.
//
// Configure → Machines stays in chat, shrunk to what it should always
// have been: pick one for THIS agent, with a link here to edit it.
// Attaching is a decision about an agent; authoring is not.

package orchestrate

import (
	"net/http"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

func init() {
	RegisterExtensionSection(ExtensionSectionEntry{
		Build: machinesExtensionSection,
		// After the page's own tools and credentials, which are what
		// somebody reaches for constantly.
		Order: 10,
	})
}

// machinesExtensionSection is the index: what you have, what state it is
// in, and a way into the editor.
func machinesExtensionSection(r *http.Request, user string) (ui.Section, bool) {
	if !UserHasAppAccess(r, "/orchestrate") {
		return ui.Section{}, false
	}
	return ui.Section{
		Title: "Machines",
		Wide:  true,
		Subtitle: "Workflows an agent moves through and stays in, instead of re-deciding its approach every turn. " +
			"A machine is yours and reusable: point several agents at the same one. " +
			"Attach one to an agent from that agent's chat toolbar (Configure → Machines); this is where you build them.",
		Body: ui.Stack{Children: []ui.Component{
			// A create affordance where machines are AUTHORED. Without it
			// the only "new" lived in the chat modal and opened the JSON
			// editor — so the page built for authoring could not start one.
			ui.Toolbar{Actions: []ui.ToolbarAction{{
				Label:   "New machine",
				Title:   "Start from a small working machine you can replace entirely",
				URL:     "/orchestrate/machine?new=1",
				Method:  "GET",
				Variant: "primary",
			}}},
			ui.Table{
				Source: "/orchestrate/api/machines",
				RowKey: "id",
				// The whole row opens the editor — a machine's name is a
				// link to the thing, not a label beside a button.
				RowLink: "edit_url",
				Columns: []ui.Col{
					{Field: "name", Flex: 1},
					{Field: "phases", Label: "Steps", Mute: true},
					{Field: "status", Label: "State", Mute: true, Flex: 2},
					{Field: "used_by_text", Label: "Used by", Mute: true, Flex: 2},
				},
				RowActions: []ui.RowAction{
					{Type: "button", Label: "Delete", Method: "DELETE",
						PostTo:     "/orchestrate/api/machines/{id}",
						Variant:    "danger",
						Confirm:    "Delete this machine? Agents pointing at it are detached; conversations already running it finish as ordinary agent turns.",
						Optimistic: true},
				},
				EmptyText: "No machines yet. A machine is a set of steps a conversation moves through — decide what kind of question this is, go and look, then answer — where the conversation SETTLES in one of them rather than starting over each turn.",
			},
		}},
	}, true
}

// handleMachinePage is the editor, full width.
//
//	GET /orchestrate/machine?id=<machine>
func (T *OrchestrateApp) handleMachinePage(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	// ?new=1 mints one and lands you IN the editor rather than on a form
	// asking for a name. The starter comes from the server (StarterMachine)
	// and a test proves it validates, so the first thing somebody sees is a
	// machine that would run — not an empty box and a save that refuses.
	//
	// It has to be minted here because POST /api/machines validates: an
	// EMPTY machine cannot be created at all, so "make one and fill it in"
	// was not a path that existed.
	if r.URL.Query().Get("new") == "1" {
		fresh := StarterMachine()
		fresh.Owner = user
		fresh.ID = ""
		saved := SaveMachineDef(udb, fresh)
		Log("[orchestrate.machines] user=%q started a new machine (id=%s)", user, saved.ID)
		http.Redirect(w, r, "/orchestrate/machine?id="+saved.ID, http.StatusSeeOther)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	def, found := LoadMachineDef(udb, user, id)
	if !found {
		http.NotFound(w, r)
		return
	}
	// The pieces by NAME, not by position in a flat list. Indexing into
	// that list is how every phase's form ended up under the first phase
	// and the second phase's section showed the "add" button: the menu
	// and the diagram were right, and the bodies under them were not.
	spec := machineEditorSpec(def, editorCatalog{agents: agentOptions(udb, user), tools: availableWorkerToolOptions(user)})
	meta, _ := spec["meta"].(ui.FormPanel)
	panels, _ := spec["phases"].([]ui.Component)
	add, _ := spec["add"].(ui.ModalButton)

	page := ui.Page{
		Title:     def.Name,
		ShowTitle: true,
		BackURL:   "/gateways",
		Nav:       HubNav("/gateways"),
		// The tables and the prompt boxes are the content. A machine with
		// four phases does not fit a dialog, which is why this is a page.
		MaxWidth: "100%",
		// One section at a time, with the steps as the rail. This is the
		// navigation: a machine IS a list of steps, so the page's index
		// should be that list rather than a scroll position.
		SectionNav: true,
		Head: ui.NewHead().
			ClientAction("machine_remove_step", machineRemoveStepJS).
			ClientAction("machine_try", machineTryJS).
			JS(machineTryEnterJS),
		Sections: []ui.Section{
			{
				Title:    "The machine",
				Wide:     true,
				Subtitle: "Its name, and where a new conversation begins.",
				Body:     meta,
			},
		},
	}
	// One section PER STEP, so the left rail is the list of steps and you
	// work on one at a time. A machine is read step by step; a page that
	// stacks four of them asks you to scroll to find where you were.
	// One section per step, each with ITS OWN panel. Aligned by index
	// against the same slice the spec built from def.Phases, so the
	// section titled "verify" holds verify's form and nothing else.
	for i, p := range def.Phases {
		if i >= len(panels) {
			break
		}
		page.Sections = append(page.Sections, ui.Section{
			Title:    p.Name,
			Wide:     true,
			Subtitle: phaseSubtitle(p),
			Body:     panels[i],
		})
	}
	page.Sections = append(page.Sections, []ui.Section{
		{
			Title:    "Add a step",
			Wide:     true,
			Subtitle: "Name it and say what it does; wire it up in its own section afterwards, where every option is real.",
			Body:     add,
		},
		// Behaviour, next to shape. Everything a machine is about
		// happens ACROSS turns, and until this the editor could only
		// ever show its configuration — you authored twelve fields per
		// step and then opened a chat and hoped.
		{
			Title:    "Try it",
			Wide:     true,
			Subtitle: "Send a message through it and watch where it goes. It runs the real driver, with no tools and without running the step it lands in, so it shows the PATH rather than the answer.",
			Body:     machineTryPanel(def),
		},
		{
			Title:    "Picture",
			Wide:     true,
			Subtitle: "Rendered from the steps above. Reload after a change to see it move.",
			Body: ui.Card{HTML: `<div style="overflow-x:auto">` +
				`<img src="/orchestrate/api/machines/` + HTMLEscape(def.ID) + `/graph" alt="` +
				HTMLEscape(def.Name) + ` diagram" style="max-width:100%"></div>`},
		},
		// The criticism goes AFTER the work. Landing on two lists of
		// what is wrong before seeing the thing you are building reads
		// as a rebuke; the list is still one scroll away, and the
		// Extensions row already said how much was outstanding before
		// you opened it.
		{
			Title: "Worth a look",
			// Separate from the checklist above, and phrased as a
			// suggestion: this reads prompt wording, which is a guess
			// about intent. A guess that looked like a defect would
			// send somebody rewriting something that works.
			Subtitle: adviceText(def),
			Wide:     true,
		},
		{
			Title: "What is still missing",
			// Validate's own findings, phrased as work remaining. A
			// machine mid-build has problems by definition; reporting
			// them as failure argues with somebody who is not finished.
			Subtitle: checklistText(def),
			Wide:     true,
		},
	}...)
	page.ServeHTTP(w, r)
}

// machineRemoveStepJS deletes one step and reloads.
//
// A client action rather than a plain DELETE toolbar button because the
// toolbar fires the request and does not refresh — the removed step
// would sit on screen looking removed-but-not, which is the same class of
// lie as a menu that disagrees with its content.
const machineRemoveStepJS = `function(ctx) {
  var step = ctx && ctx.action && ctx.action.data;
  var id = new URLSearchParams(window.location.search).get('id');
  if (!step || !id) return;
  fetch('/orchestrate/api/machines/' + encodeURIComponent(id) +
        '/phases?name=' + encodeURIComponent(step), {method: 'DELETE'})
    .then(function(r) {
      if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
      // Reload rather than removing the section in place: deleting a step
      // changes what every OTHER step can route to, so their forms are
      // stale the moment this succeeds.
      window.location.reload();
    })
    .catch(function(err) {
      window.uiAlert && window.uiAlert('Could not remove it: ' + (err && err.message || err));
    });
}`

// phaseSubtitle says what a step is and where it goes, in the rail and
// above its form — so the shape of the machine is legible without
// opening every section.
func phaseSubtitle(p MachinePhase) string {
	var b strings.Builder
	if d := strings.TrimSpace(p.Desc); d != "" {
		b.WriteString(d + " · ")
	}
	if p.Resident {
		b.WriteString("the conversation waits here")
		if p.Next != "" {
			b.WriteString(", then goes to " + p.Next)
		}
	} else {
		switch {
		case len(p.RoutingChoices()) > 0:
			b.WriteString("then chooses between " + strings.Join(p.RoutingChoices(), ", "))
		case p.NextFrom != "":
			b.WriteString("then goes to whichever step it names in " + p.NextFrom)
		case p.Next != "":
			b.WriteString("then goes to " + p.Next)
		default:
			b.WriteString("goes nowhere yet")
		}
	}
	if p.Agent != "" {
		b.WriteString(" · run by another agent")
	}
	return b.String()
}

// checklistText renders Problems() as one readable line, or says the
// machine is complete.
// adviceText renders Advice(), or says there is none.
func adviceText(def MachineDef) string {
	adv := def.Advice()
	if len(adv) == 0 {
		return "Nothing — the steps read as instructions rather than specifications."
	}
	return "• " + strings.Join(adv, " • ")
}

func checklistText(def MachineDef) string {
	probs := def.Problems()
	if len(probs) == 0 {
		return "✓ Nothing outstanding — this machine will run as written."
	}
	return strconv.Itoa(len(probs)) + " to fix: • " + strings.Join(probs, " • ")
}
