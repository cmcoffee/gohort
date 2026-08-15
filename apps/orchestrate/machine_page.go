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
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	def, found := LoadMachineDef(udb, user, id)
	if !found {
		http.NotFound(w, r)
		return
	}
	spec := machineEditorSpec(def)
	comps, _ := spec["components"].([]any)
	children := make([]ui.Component, 0, len(comps))
	for _, c := range comps {
		if cc, ok := c.(ui.Component); ok {
			children = append(children, cc)
		}
	}

	ui.Page{
		Title:     def.Name,
		ShowTitle: true,
		BackURL:   "/gateways",
		Nav:       HubNav("/gateways"),
		// The tables and the prompt boxes are the content. A machine with
		// four phases does not fit a dialog, which is why this is a page.
		MaxWidth: "100%",
		Sections: []ui.Section{
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
			{
				Title:    "The machine",
				Wide:     true,
				Subtitle: "Its name, and where a new conversation begins.",
				Body:     children[0],
			},
			{
				Title: "Steps",
				Wide:  true,
				Subtitle: "Each step is one part of the conversation: what the agent is doing, whether the turn ENDS there, and where it goes next. " +
					"A conversation lives in the steps that wait; the ones that hand on run and pass through in the same turn.",
				Body: ui.Stack{Children: children[1:]},
			},
			{
				Title:    "Picture",
				Wide:     true,
				Subtitle: "Rendered from the steps above. Reload after a change to see it move.",
				// The SVG endpoint, injected as-is. It is server-rendered
				// and themed by the page's own variables (see the graph
				// route), so nothing here has to know how it is drawn.
				Body: ui.Card{HTML: `<div style="overflow-x:auto">` +
					`<img src="/orchestrate/api/machines/` + HTMLEscape(def.ID) + `/graph" alt="` +
					HTMLEscape(def.Name) + ` diagram" style="max-width:100%"></div>`},
			},
		},
	}.ServeHTTP(w, r)
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
