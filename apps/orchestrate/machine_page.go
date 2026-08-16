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
			// The other door: say what you want and review a draft,
			// instead of building from a blank. The editor's checklist
			// carries anything the draft got wrong, so an imperfect
			// draft is still a better starting point than an empty one.
			ui.ModalButton{
				Label:    "Describe one…",
				Title:    "Draft a machine from a description",
				Subtitle: "Say what the conversation should do — what it works out first, what it decides between, where it settles. A draft machine opens in the editor for you to adjust.",
				Width:    "560px",
				Body: ui.FormPanel{
					PostURL:     "/orchestrate/api/machines/draft",
					SubmitLabel: "Draft it",
					RedirectURL: "/orchestrate/machine?id={id}",
					Fields: []ui.FormField{{
						Field: "description", Type: "textarea", Rows: 6,
						Label:       "What should it do?",
						Placeholder: "Triage support questions: work out whether there is a log bundle to dig into or just a question, investigate bundles with the log tools, and answer questions from the knowledge base. Stay in the investigation until the person moves to a new problem.",
						Help:        "Plain words. Say what kinds of turns arrive and what should happen to each; the draft picks the steps.",
					}},
				},
			},
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
					{Type: "button", Label: "Duplicate", Method: "POST",
						PostTo:         "/orchestrate/api/machines/{id}/duplicate",
						RedirectURL:    "/orchestrate/machine?id={id}",
						RedirectTarget: "_self"},
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
		// The map, above every step rather than parked in a section of
		// its own. SectionNav shows ONE section at a time, so the picture
		// and the step you are editing could never be on screen together
		// — and a split is exactly what a step's own form cannot show
		// you: it lists the names it may choose between, while the SHAPE
		// of that choice lives in the arrows.
		Sticky: machineMapCard(def),
		Head: ui.NewHead().
			ClientAction("machine_remove_step", machineRemoveStepJS).
			ClientAction("machine_move_step", machineMoveStepJS).
			ClientAction("machine_try", machineTryJS).
			ClientAction("machine_try_reset", machineTryResetJS).
			ClientAction("machine_duplicate", machineDuplicateJS).
			CSS(machineMapCSS).
			JS(machineMapHereJS).
			JS(machineTryEnterJS).
			JS(machinePreviewRefreshJS),
		Sections: []ui.Section{
			{
				Title:    "The machine",
				Wide:     true,
				Subtitle: "Its name, and where a new conversation begins.",
				Body: ui.Stack{Children: []ui.Component{
					meta,
					// The portable recipe and the safe-experiment copy,
					// both of which already existed as endpoints and
					// neither of which was reachable from the page where
					// you author. Export downloads the same JSON the
					// bundle carries; Duplicate lands in the copy, so
					// trying something drastic never costs the original.
					ui.Toolbar{Actions: []ui.ToolbarAction{{
						Label:  "Export",
						Title:  "Download this machine's portable recipe",
						Method: "GET",
						URL:    "/orchestrate/api/machines/" + url_(def.ID) + "/export",
					}, {
						Label:  "Duplicate",
						Title:  "Make a copy to experiment on, and open it",
						Method: "client",
						URL:    "machine_duplicate",
						Data:   def.ID,
					}}},
				}},
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
			Subtitle: "Hold a rehearsal conversation with it: send a message, watch where it goes, then keep sending — later turns resume the parked step, so you can watch a guard fire or a handoff happen. Real driver, no tools, and the step it lands in is not run: it shows the PATH, not the answer.",
			Body:     machineTryPanel(def),
		},
		{
			Title:    "Picture",
			Wide:     true,
			Subtitle: "The same map as the one pinned above, full size — click a step to open its section. Reload after a change to see it move.",
			// Inline rather than an <img>: links inside an imaged SVG are
			// inert, and the whole point of drawing the steps is that
			// they are the same steps the rail navigates. Every dynamic
			// string in the document is escaped at the renderer
			// (xmlEscape), which is what makes inlining safe.
			Body: ui.Card{HTML: `<div style="overflow-x:auto">` + machineGraphSVG(def) + `</div>`},
		},
		{
			// Derived, not written: the definition knows exactly which
			// steps cost a model call and which turns pay a guard check,
			// and the person choosing between one step and three should
			// see the price where they are choosing.
			Title:    "What a turn costs",
			Wide:     true,
			Subtitle: costText(def),
		},
		{
			Title: "Who runs it",
			Wide:  true,
			Subtitle: "An unattached machine does nothing — an agent has to carry it into its conversations. " +
				"Checking an agent points it at this machine; an agent can only run one at a time, so checking one that runs another moves it.",
			Body: ui.FormPanel{
				Source:  machineAPIBase(def) + "/agents",
				PostURL: machineAPIBase(def) + "/agents",
				Fields: []ui.FormField{{
					Field: "agents", Type: "checklist",
					Placeholder: "(no agents yet — create one in the chat sidebar first)",
					Options:     attachAgentOptions(udb, user, def),
				}},
			},
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

// machinePreviewRefreshJS keeps "What this step actually receives"
// honest without reloading the page.
//
// The framework already broadcasts ui-data-changed with the endpoint a
// form just wrote to (uiInvalidateSaved), and a phase form writes to
// .../phases?name=<step> — so the step whose preview went stale is
// named in the event. A page reload would be the other way to do this,
// and the wrong one: the prompt textarea saves on a typing debounce, so
// reloading would yank the page out from under somebody mid-sentence.
const machinePreviewRefreshJS = `window.addEventListener('ui-data-changed', function(ev) {
  var sources = (ev.detail && ev.detail.sources) || [];
  sources.forEach(function(src) {
    var at = String(src).indexOf('/phases?name=');
    if (at < 0) return;
    var step = decodeURIComponent(String(src).slice(at + '/phases?name='.length));
    var box = document.querySelector('[data-preview-step="' + (window.CSS && CSS.escape ? CSS.escape(step) : step) + '"]');
    if (!box) return;
    fetch(src + '&preview=1')
      .then(function(r) { return r.ok ? r.json() : null; })
      .then(function(d) {
        if (!d) return;
        var body = box.querySelector('[data-preview-body]');
        var note = box.querySelector('[data-preview-note]');
        if (body) body.textContent = d.block || '';
        if (note && d.note) note.textContent = d.note;
      })
      .catch(function() {});
  });
});`

// machineDuplicateJS copies the machine and opens the copy — the point
// of duplicating is to work on the copy, so staying on the original
// would be the wrong half of the action.
const machineDuplicateJS = `function(ctx) {
  var id = ctx && ctx.action && ctx.action.data;
  if (!id) return;
  fetch('/orchestrate/api/machines/' + encodeURIComponent(id) + '/duplicate', {method: 'POST'})
    .then(function(r) {
      if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
      return r.json();
    })
    .then(function(d) { window.location.href = '/orchestrate/machine?id=' + encodeURIComponent(d.id); })
    .catch(function(err) {
      window.uiAlert && window.uiAlert('Could not duplicate it: ' + (err && err.message || err));
    });
}`

// machineMoveStepJS reorders one step and reloads — the rail, the
// sections and every "then go to" summary are ordered by the list this
// changes, so a stale page would disagree with the machine.
const machineMoveStepJS = `function(ctx) {
  var data = ctx && ctx.action && ctx.action.data;
  var id = new URLSearchParams(window.location.search).get('id');
  if (!data || !id) return;
  var cut = data.lastIndexOf('|');
  var step = data.slice(0, cut), dir = data.slice(cut + 1);
  fetch('/orchestrate/api/machines/' + encodeURIComponent(id) + '/move', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({name: step, dir: dir})
  }).then(function(r) {
    if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
    window.location.reload();
  }).catch(function(err) {
    window.uiAlert && window.uiAlert('Could not move it: ' + (err && err.message || err));
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

// machineMapCard is the sticky map: the whole machine, the step you are
// on lit up, every box a door into that step's form.
//
// Collapsible, and that is not decoration — a four-step machine is
// nearly 300px of permanent screen, which is a real price to pay while
// writing a long prompt. Open by default because the reason it exists
// is to be seen; <details> so the browser remembers nothing and the
// page has no state to get wrong.
func machineMapCard(def MachineDef) ui.Component {
	return ui.Card{HTML: `<details class="machine-map" open>` +
		`<summary>Map — click a step to open it</summary>` +
		`<div class="machine-map-body">` + machineGraphSVG(def) + `</div>` +
		`</details>`}
}

// machineMapCSS keeps the map from eating the page, and lights the step
// the reader is on.
const machineMapCSS = `
.machine-map { padding: 0.3rem 0.5rem; }
.machine-map > summary {
  cursor: pointer; font-size: 0.72rem; letter-spacing: 0.04em;
  text-transform: uppercase; color: var(--text-mute);
}
.machine-map-body { overflow: auto; max-height: 42vh; padding-top: 0.35rem; }
.machine-map svg a { text-decoration: none; }
/* You are here. The fill is what carries it — a border alone is lost
   among the boxes that are already drawn heavier for holding a turn. */
.machine-map [data-node].here rect {
  fill: var(--accent-soft, rgba(99,102,241,0.16));
  stroke: var(--accent, #6366f1);
  stroke-width: 2.5;
}
`

// machineMapHereJS lights the step whose section is open.
//
// The section rail already addresses sections by hash (#step-slug) and
// the map's boxes already link to those anchors, so "which step am I
// on" is a question the URL answers — on arrival, on every click, and
// on the back button.
const machineMapHereJS = `(function() {
  function mark() {
    var want = (window.location.hash || '').replace(/^#/, '');
    var nodes = document.querySelectorAll('.machine-map [data-node]');
    for (var i = 0; i < nodes.length; i++) {
      var slug = String(nodes[i].getAttribute('data-node') || '')
        .toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
      nodes[i].classList.toggle('here', !!want && slug === want);
    }
  }
  window.addEventListener('hashchange', mark);
  mark();
})();`

// machineGraphSVG renders the picture with each step linking to its own
// section — the graph is the rail, drawn. The anchors use the SAME slug
// the section nav computes from its titles (SectionSlug ↔ secnavSlug),
// which is the contract that makes a drawn box a working door.
func machineGraphSVG(def MachineDef) string {
	g := def.Graph()
	for i := range g.Nodes {
		g.Nodes[i].Href = "#" + ui.SectionSlug(g.Nodes[i].ID)
	}
	return g.SVG(nil)
}

// costText derives the machine's price in model calls. Per PIECE rather
// than per turn, because a deciding step makes the turn-1 path dynamic
// and a guessed total would be a lie some of the time — each piece's
// price is exact.
func costText(def MachineDef) string {
	var passing, pinned, guarded, searching, delegated []string
	for _, p := range def.Phases {
		switch {
		case p.Resident:
			if strings.TrimSpace(p.Guard) != "" {
				guarded = append(guarded, p.Name)
			}
		case strings.TrimSpace(p.Agent) != "":
			delegated = append(delegated, p.Name)
		case len(p.ModelOutput()) == 0 && strings.TrimSpace(p.Prompt) == "" && len(p.StaticFields()) > 0:
			pinned = append(pinned, p.Name)
		case len(p.Tools) > 0:
			searching = append(searching, p.Name)
		default:
			passing = append(passing, p.Name)
		}
	}
	var parts []string
	if len(passing) > 0 {
		parts = append(parts, strings.Join(passing, ", ")+" each cost one model call when they run (a reply that fails to decode costs one repair retry)")
	}
	// A step with tools is a small agent loop, not one call — the number
	// comes from the cap itself so it cannot go stale.
	if len(searching) > 0 {
		parts = append(parts, strings.Join(searching, ", ")+" may use tools, so each is a short loop: one call per round, up to "+
			strconv.Itoa(StageToolRounds))
	}
	if len(delegated) > 0 {
		parts = append(parts, strings.Join(delegated, ", ")+" hand their work to another agent, which spends its own turn (and its own tools) before this one records what came back")
	}
	if len(pinned) > 0 {
		parts = append(parts, strings.Join(pinned, ", ")+" only pin values and are free")
	}
	if len(guarded) > 0 {
		parts = append(parts, "every new turn arriving in "+strings.Join(guarded, " or ")+" pays one extra check (its guard)")
	}
	if len(parts) == 0 {
		return "Nothing beyond the reply itself — no step runs before it and no guard checks it."
	}
	return "Beyond the reply itself: " + strings.Join(parts, "; ") + ". The reply step is the turn you were paying for anyway."
}
