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
		Head:  runsOnPillsHead,
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
			// The three ways to get a machine, on one line: they are
			// alternatives to each other, and stacked they read as three
			// steps somebody is meant to take in order.
			ui.Stack{Row: true, Children: []ui.Component{
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
				// instead of building from a blank. A PAGE, like New
				// machine — not a dialog. ModalButton opens a native
				// <dialog> with showModal(), which sits in the browser's
				// top layer above every z-index, so anything the
				// framework raised from inside it (a failure toast, a
				// second dialog) was invisible underneath. Drafting runs
				// a model for up to a minute and can fail; a door that
				// cannot show you why it failed is the wrong door for it.
				ui.Toolbar{Actions: []ui.ToolbarAction{{
					Label:  "Describe one…",
					Title:  "Draft a machine from a description of what it should do",
					URL:    "/orchestrate/machine?describe=1",
					Method: "GET",
				}}},
				// The third way in. The endpoint has existed since machines
				// did, with nothing on any page calling it — so a recipe
				// somebody was handed could only be brought in through the
				// tool or a bundle. A file field reads the file as TEXT in
				// the browser and submits its contents, so this is a form
				// rather than an upload path.
				ui.ModalButton{
					Label:    "Import…",
					Title:    "Bring in a machine somebody exported",
					Subtitle: "Pick a .machine.json recipe. It lands as a machine of your own — a copy, with its own id — and opens in the editor.",
					Width:    "480px",
					Body: ui.FormPanel{
						PostURL:        "/orchestrate/api/machines/import",
						SubmitLabel:    "Import",
						RedirectURL:    "/orchestrate/machine?id={id}",
						RedirectTarget: "_self",
						Fields: []ui.FormField{{
							Field: "recipe", Type: "file", Accept: ".json",
							Label: "Recipe file",
							Help:  "The file an Export produced. Steps, prompts and wiring travel; nothing about the conversations that ran it does.",
						}},
					},
				},
			}},
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
					// Assignment from the LIST, the way My tools does it.
					// Somebody deciding which agents run what is looking
					// at all of them at once; opening each machine to
					// answer that is the navigation the tools table
					// already avoids.
					{Type: "button", Label: "Runs on", Method: "client",
						PostTo: "orchestrate_runs_on"},
					// Downloading a recipe should not require opening the
					// machine first — the reason to take a copy is usually
					// that you are about to change it.
					{Type: "button", Label: "Export", Method: "GET",
						PostTo: "/orchestrate/api/machines/{id}/export"},
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

// runsOnPillsHead registers the assignment pills for BOTH extension
// sections — machines and pipelines contribute it, and registering a
// client action twice is harmless because the second registration
// replaces an identical first.
//
// The generic renderer is the framework's (uiRenderScopePills); this
// only knows which endpoint to talk to, which is the app-specific half
// per the extension-registry rule. The endpoint comes from the ROW
// (edit_url carries the id), so one action serves both kinds rather
// than each growing its own copy.
const runsOnPillsHead = `<script>
(function(){
  function register(){
    if (!window.uiRegisterClientAction) { setTimeout(register, 50); return; }
    window.uiRegisterClientAction('orchestrate_runs_on', function(ctx){
      var r = (ctx && ctx.record) || {};
      var url = String(r.edit_url || '');
      // /orchestrate/machine?id=X or /orchestrate/pipeline?id=X — the
      // row already carries where it lives, so the action does not have
      // to be told which kind it is.
      var kind = url.indexOf('/pipeline') >= 0 ? 'pipelines' : 'machines';
      var id = (url.split('id=')[1] || '').split('&')[0];
      if (!id) { window.uiAlert && window.uiAlert('No row selected.'); return; }
      var base = '/orchestrate/api/' + kind + '/' + encodeURIComponent(id) + '/agents';
      var reload = ctx && ctx.reload;
      window.uiOpenSimpleModal({
        title: 'Runs on: ' + (r.name || ''),
        width: '560px',
        mount: function(body){
          var host = document.createElement('div');
          body.appendChild(host);
          window.uiRenderScopePills(host, {
            load: function(){
              return fetch(base + '?pills=1', {cache:'no-store'}).then(function(res){
                if(!res.ok) return res.text().then(function(t){ throw new Error(t || ('HTTP ' + res.status)); });
                return res.json();
              });
            },
            toggle: function(key, on){
              return fetch(base, {
                method: 'POST',
                headers: {'Content-Type':'application/json'},
                body: JSON.stringify({ target: key, on: on })
              }).then(function(res){
                if(!res.ok) return res.text().then(function(t){ throw new Error(t || ('HTTP ' + res.status)); });
                if (reload) reload();
              });
            }
          });
        }
      });
    });
  }
  register();
})();
</script>`

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
	// ?describe=1 is the drafting form, on its own page.
	//
	// It was a dialog until somebody could not tell whether it had
	// failed. The framework's own failure surfaces — the toast, an
	// alert — are z-indexed elements, and a native <dialog> opened with
	// showModal() renders in the TOP LAYER above all of them, so they
	// landed underneath it. Drafting runs a model for up to a minute and
	// can come back with nothing usable; that is exactly the door that
	// has to be able to say so.
	if r.URL.Query().Get("describe") == "1" {
		T.serveMachineDescribePage(w, r, user)
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
			ClientAction("machine_repair", machineRepairJS).
			ClientAction("machine_undo", machineUndoJS).
			CSS(machineMapCSS).
			JS(machineMapHereJS).
			JS(machineRewriteJS).
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
			// Say what should change, against the machine already on
			// screen. A SECTION rather than a dialog, and that is the
			// whole point: ModalButton opens a native <dialog> with
			// showModal(), which renders in the browser's top layer —
			// above every z-index there is. Anything the framework
			// raises from inside one (a toast, another modal) lands
			// underneath it and cannot be seen, so a submit that failed
			// in there reset its button and told you nothing. On the
			// page the form is just a form: its errors render inline,
			// next to the box you typed in.
			{
				Title: "Describe a change",
				Wide:  true,
				Subtitle: "Say what should be different and the machine is redrafted with that change made, keeping every step and prompt it does not touch. " +
					"The version you have now is kept, so you can put it back.",
				Body: ui.Stack{Children: []ui.Component{
					ui.FormPanel{
						PostURL:     machineAPIBase(def) + "/revise",
						SubmitLabel: "Revise it",
						RedirectURL: "/orchestrate/machine?id=" + url_(def.ID),
						Fields: []ui.FormField{{
							Field: "description", Type: "textarea", Rows: 4,
							Label:       "What should change?",
							Placeholder: "Let triage choose between three lanes — logs, config, and a plain question — and give the config lane its own step before it answers.",
							Help:        "One change, in plain words. It runs a model, so it takes a moment; what it changed is reported when it lands.",
						}},
					},
					undoRevisionToolbar(def),
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
	// Which steps are ALTERNATIVES, and to what. A flat rail draws two
	// steps that a decision picks between exactly like two steps that
	// run one after the other — the one distinction this list most needs
	// to make, and the reason a branch was hard to see anywhere but the
	// map.
	forks := branchAlternatives(def)
	for i, p := range def.Phases {
		if i >= len(panels) {
			break
		}
		page.Sections = append(page.Sections, ui.Section{
			Title:    p.Name,
			Wide:     true,
			Indent:   len(forks[p.Name]),
			Subtitle: phaseSubtitle(p) + forkNote(forks[p.Name]),
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
			// Separate from the checklist below, and phrased as a
			// suggestion: this reads prompt wording, which is a guess
			// about intent. A guess that looked like a defect would
			// send somebody rewriting something that works.
			Subtitle: "Nothing here refuses a save. It is what the machine looks like it might not have meant.",
			Wide:     true,
			Body: withRepairButton(def, RepairAdvice,
				ui.Card{HTML: `<div data-machine-advice>` + adviceHTML(def) + `</div>`}),
		},
		{
			Title: "What is still missing",
			// Validate's own findings, phrased as work remaining. A
			// machine mid-build has problems by definition; reporting
			// them as failure argues with somebody who is not finished.
			//
			// In the BODY rather than the heading so it can be kept
			// true: this is the list somebody works against, fixing one
			// thing at a time, and a list that still says "3 to fix"
			// after the third fix is the one place staleness actually
			// costs something.
			Subtitle: "The same findings a save is checked against, as work remaining.",
			Wide:     true,
			Body: withRepairButton(def, RepairProblems,
				ui.Card{HTML: `<div data-machine-checklist>` + checklistHTML(def) + `</div>`}),
		},
	}...)
	page.ServeHTTP(w, r)
}

// withRepairButton puts a fix-it button under a findings list, and only
// when there is something it can honestly fix.
//
// The button is scoped to the panel it sits under: each list settles
// its own findings. It is labelled with the COUNT and titled with the
// changes themselves, because a button that edits the thing you are
// building has to say what it will do before it does it — and because
// the ones it cannot fix stay in the list, so "fix 2" next to five
// findings is the honest shape rather than a bug.
func withRepairButton(def MachineDef, kind string, body ui.Component) ui.Component {
	fixable := def.Repairs(kind)
	if len(fixable) == 0 {
		return body
	}
	label := "Fix it"
	if len(fixable) > 1 {
		label = "Fix " + strconv.Itoa(len(fixable)) + " of these"
	}
	return ui.Stack{Children: []ui.Component{
		body,
		ui.Toolbar{Actions: []ui.ToolbarAction{{
			Label:  label,
			Title:  "Applies exactly these:\n• " + strings.Join(RepairLines(fixable), "\n• "),
			Method: "client",
			URL:    "machine_repair",
			Data:   kind,
		}}},
	}}
}

// serveMachineDescribePage is the drafting form: one box, one button,
// and room for what came back.
func (T *OrchestrateApp) serveMachineDescribePage(w http.ResponseWriter, r *http.Request, user string) {
	page := ui.Page{
		Title:     "Describe a machine",
		ShowTitle: true,
		BackURL:   "/gateways",
		Nav:       HubNav("/gateways"),
		Sections: []ui.Section{{
			Title: "What should it do?",
			Wide:  true,
			Subtitle: "Say what kinds of turns arrive and what should happen to each — what the conversation works out first, what it decides between, where it settles. " +
				"A draft machine opens in the editor for you to adjust; anything the draft got wrong is waiting in its checklist, which beats an empty editor.",
			Body: ui.FormPanel{
				PostURL:     "/orchestrate/api/machines/draft",
				SubmitLabel: "Draft it",
				RedirectURL: "/orchestrate/machine?id={id}",
				Fields: []ui.FormField{{
					Field: "description", Type: "textarea", Rows: 8,
					Label:       "In plain words",
					Placeholder: "Triage support questions: work out whether there is a log bundle to dig into or just a question, investigate bundles with the log tools, and answer questions from the knowledge base. Stay in the investigation until the person moves to a new problem.",
					Help:        "It runs a model, so it takes a moment. If it cannot produce something usable it says so here rather than failing quietly.",
				}},
			},
		}, {
			Title:    "The other two ways in",
			Wide:     true,
			Subtitle: "A starter machine that already runs, or a recipe somebody exported. Neither needs a model.",
			Body: ui.Toolbar{Actions: []ui.ToolbarAction{{
				Label:   "New machine",
				Title:   "Start from a small working machine you can replace entirely",
				URL:     "/orchestrate/machine?new=1",
				Method:  "GET",
				Variant: "primary",
			}, {
				Label:  "Back to the list",
				Title:  "Extensions, where your machines are kept",
				URL:    "/gateways",
				Method: "GET",
			}}},
		}},
	}
	Log("[orchestrate.machines] user=%q opened the describe form", user)
	page.ServeHTTP(w, r)
}

// machineRepairJS applies one panel's mechanical fixes and reloads.
//
// A reload rather than a patch of the two lists: a repair rewrites the
// wiring of steps whose own forms are on this page, so their pickers
// and previews are stale the moment it succeeds.
const machineRepairJS = `function(ctx) {
  var kind = (ctx && ctx.action && ctx.action.data) || '';
  var id = new URLSearchParams(window.location.search).get('id');
  if (!id) return;
  fetch('/orchestrate/api/machines/' + encodeURIComponent(id) +
        '/repair?kind=' + encodeURIComponent(kind), {method: 'POST'})
    .then(function(r) {
      if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
      return r.json();
    })
    .then(function(d) {
      // Nothing to do is worth saying: the alternative is a button that
      // looks like it did something and a page that looks unchanged.
      if (!d || !d.fixed || !d.fixed.length) {
        window.uiAlert && window.uiAlert('Nothing left to fix here.');
        return;
      }
      window.location.reload();
    })
    .catch(function(err) {
      window.uiAlert && window.uiAlert('Could not fix it: ' + (err && err.message || err));
    });
}`

// undoRevisionToolbar offers to put back what a revision replaced, and
// only while there is something to put back.
//
// A revision is the one edit on this page that can rewrite work nobody
// asked it to touch — every other control changes the field it names.
// Without a way back it is a control people are right not to press.
func undoRevisionToolbar(def MachineDef) ui.Component {
	if def.Previous == nil {
		return ui.Stack{}
	}
	return ui.Toolbar{Actions: []ui.ToolbarAction{{
		Label:   "Undo the revision",
		Title:   "Put back the version this machine had before the last Describe a change",
		Method:  "client",
		URL:     "machine_undo",
		Data:    def.ID,
		Variant: "danger",
	}}}
}

// machineUndoJS restores the previous version and reloads.
const machineUndoJS = `function(ctx) {
  var id = ctx && ctx.action && ctx.action.data;
  if (!id) return;
  Promise.resolve(window.uiConfirm
    ? window.uiConfirm('Put back the version from before the last revision? What the revision produced is discarded.')
    : confirm('Put back the version from before the last revision?')).then(function(ok) {
    if (!ok) return;
    fetch('/orchestrate/api/machines/' + encodeURIComponent(id) + '/undo', {method: 'POST'})
      .then(function(r) {
        if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
        // Reload: every step's form, the map and both findings lists are
        // showing the version being replaced.
        window.location.reload();
      })
      .catch(function(err) {
        window.uiAlert && window.uiAlert('Could not undo it: ' + (err && err.message || err));
      });
  });
}`

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

// branchAlternatives maps each step to the steps that CHOOSE it — but
// only where the choosing is a real fork (two or more destinations).
// A step that hands off to exactly one place is a sequence, not a
// branch, and drawing it as one would make every machine look forked.
func branchAlternatives(def MachineDef) map[string][]string {
	out := map[string][]string{}
	for _, p := range def.Phases {
		choices := p.RoutingChoices()
		if len(choices) < 2 {
			continue
		}
		for _, c := range choices {
			c = strings.TrimSpace(c)
			if _, real := def.Phase(c); real {
				out[c] = append(out[c], p.Name)
			}
		}
	}
	return out
}

// forkNote says what a step is an alternative TO, in the step's own
// heading — the rail shows the shape, this says what the shape means.
func forkNote(deciders []string) string {
	if len(deciders) == 0 {
		return ""
	}
	return " · one of the ways " + strings.Join(deciders, " and ") + " can go"
}

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

// A findings list is a LIST. Joined into one paragraph with inline
// bullets, three findings that each run to four lines of prose read as
// a wall — and the wall is what somebody works down one item at a time,
// so it is the last place to save vertical space.
// And the step a finding names is a LINK to that step. Several of them
// end in an instruction to go somewhere — tick its tools, set next,
// turn on resident — and the rail is one section at a time, so a
// finding that names a step you then have to go and find is asking you
// to do the navigation twice.
func findingsHTML(def MachineDef, items []string, empty string) string {
	if len(items) == 0 {
		return `<div class="machine-findings-none">` + HTMLEscape(empty) + `</div>`
	}
	// The findings whose fix is prose. Matched by VALUE against the same
	// sentence core writes, because a step can carry two findings at once
	// and only one of them is a rewrite — matching by step would put the
	// button on whichever came first.
	rewritable := map[string]string{}
	for _, rw := range def.PromptRewrites() {
		rewritable[rw.Why] = rw.Step
	}
	var b strings.Builder
	b.WriteString(`<ul class="machine-findings">`)
	for _, it := range items {
		b.WriteString(`<li>`)
		if step := findingStep(def, it); step != "" {
			lead := "step " + step
			b.WriteString(`<a class="machine-finding-step" href="#` + ui.SectionSlug(step) + `">` +
				HTMLEscape(lead) + `</a>` + HTMLEscape(strings.TrimPrefix(it, lead)))
		} else {
			b.WriteString(HTMLEscape(it))
		}
		if step, ok := rewritable[it]; ok {
			b.WriteString(` <button type="button" class="machine-finding-rewrite" data-rewrite-step="` +
				HTMLEscape(step) + `">Rewrite the instructions…</button>`)
		}
		b.WriteString(`</li>`)
	}
	b.WriteString(`</ul>`)
	return b.String()
}

// findingStep is the step a finding opens with, or "" for a finding
// about the machine itself.
//
// The boundary is a colon OR a space, because the findings are written
// as sentences and not all of them punctuate the same way ("step x:
// next names…" but "step x passes on but goes nowhere"). Longest match
// wins: a step may be named with another step's name as its prefix, and
// then both match and only the longer one is right.
func findingStep(def MachineDef, line string) string {
	best := ""
	for _, p := range def.Phases {
		n := strings.TrimSpace(p.Name)
		if n == "" || len(n) <= len(best) || !strings.HasPrefix(line, "step "+n) {
			continue
		}
		switch rest := line[len("step "+n):]; {
		case rest == "", rest[0] == ':', rest[0] == ' ':
			best = n
		}
	}
	return best
}

func adviceHTML(def MachineDef) string {
	return findingsHTML(def, def.Advice(), "Nothing — the steps read as instructions rather than specifications.")
}

func checklistHTML(def MachineDef) string {
	probs := def.Problems()
	if len(probs) == 0 {
		return findingsHTML(def, nil, "✓ Nothing outstanding — this machine will run as written.")
	}
	return `<div class="machine-findings-count">` + strconv.Itoa(len(probs)) + ` to fix</div>` +
		findingsHTML(def, probs, "")
}

// machineMapCard is the sticky map: the whole machine, the step you are
// on lit up, every box a door into that step's form.
//
// Not collapsible. The map is how this page is read — the steps below
// it are a form per box, and hiding the shape leaves a list of forms
// whose relationship to each other is invisible. A collapse control
// offers to turn the page back into the thing the map was built to
// replace, and the height it would reclaim is capped already.
func machineMapCard(def MachineDef) ui.Component {
	return ui.Card{HTML: `<div class="machine-map">` +
		`<div class="machine-map-cap">Map — click a step to open it. Its shape follows the arrows, not the step order.</div>` +
		`<div class="machine-map-body">` + machineGraphSVG(def) + `</div>` +
		`</div>`}
}

// machineMapCSS keeps the map from eating the page, and lights the step
// the reader is on.
const machineMapCSS = `
.machine-map { padding: 0.3rem 0.5rem; }
.machine-map-cap {
  font-size: 0.72rem; letter-spacing: 0.04em;
  text-transform: uppercase; color: var(--text-mute);
  text-align: center;
}
/* Centred, and only while it FITS: a machine wider than the card still
   scrolls from its left edge, where the first step is. Centring a
   scrolled diagram would hide both ends at once. */
.machine-map-body {
  overflow: auto; max-height: 42vh; padding-top: 0.35rem;
  display: flex; justify-content: center;
}
.machine-map-body > svg { flex: 0 0 auto; margin: 0 auto; }
/* One finding per line, and each one indented under its own marker so a
   four-line finding does not read as four findings. */
.machine-findings { margin: 0; padding-left: 1.1rem; }
.machine-findings > li { margin: 0 0 0.5rem 0; line-height: 1.5; }
.machine-findings > li:last-child { margin-bottom: 0; }
.machine-findings-count {
  font-size: 0.72rem; letter-spacing: 0.04em; text-transform: uppercase;
  color: var(--text-mute); margin-bottom: 0.4rem;
}
.machine-findings-none { color: var(--text-mute); }
.machine-finding-step {
  color: var(--accent, #6366f1); text-decoration: none; font-weight: 600;
}
.machine-finding-step:hover { text-decoration: underline; }
/* Offered where the finding is, not in a toolbar: this one settles ONE
   finding, and a button away from the sentence it answers has to
   restate it. */
.machine-finding-rewrite {
  display: inline-block; margin-left: 0.25rem; padding: 0.05rem 0.45rem;
  font: inherit; font-size: 0.74rem; cursor: pointer;
  color: var(--accent, #6366f1); background: transparent;
  border: 1px solid var(--accent, #6366f1); border-radius: 999px;
  white-space: nowrap;
}
.machine-finding-rewrite:hover { background: var(--accent-soft, rgba(99,102,241,0.16)); }
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
  // The two derived lists, worded HERE as well as on the server —
  // deliberately, and pinned by a test, because a refresh that phrased
  // them differently would read as the page changing its mind rather
  // than as the same list one item shorter.
  // Rebuilt as a LIST, matching what the page rendered — the server
  // draws <ul><li>, so replacing it with one joined line would make the
  // panel change shape the first time anything was saved.
  function paintFindings(box, items, empty, count, rewrites) {
    if (!box) return;
    var rewritable = {};
    (rewrites || []).forEach(function(rw) { if (rw && rw.why) rewritable[rw.why] = rw.step; });
    box.textContent = '';
    if (!items.length) {
      var none = document.createElement('div');
      none.className = 'machine-findings-none';
      none.textContent = empty;
      box.appendChild(none);
      return;
    }
    if (count) {
      var head = document.createElement('div');
      head.className = 'machine-findings-count';
      head.textContent = items.length + ' to fix';
      box.appendChild(head);
    }
    var names = stepNames();
    var ul = document.createElement('ul');
    ul.className = 'machine-findings';
    items.forEach(function(it) {
      var li = document.createElement('li');
      var step = findingStep(names, it);
      if (step) {
        var a = document.createElement('a');
        a.className = 'machine-finding-step';
        a.href = '#' + slugOf(step);
        a.textContent = 'step ' + step;
        li.appendChild(a);
        li.appendChild(document.createTextNode(it.slice(('step ' + step).length)));
      } else {
        li.textContent = it;   // textContent, so a finding quoting a step name is text
      }
      if (rewritable[it]) {
        var btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'machine-finding-rewrite';
        btn.setAttribute('data-rewrite-step', rewritable[it]);
        btn.textContent = 'Rewrite the instructions…';
        li.appendChild(document.createTextNode(' '));
        li.appendChild(btn);
      }
      ul.appendChild(li);
    });
    box.appendChild(ul);
  }

  // The step names come from the map this refresh just redrew, so there
  // is one list of steps on the page rather than a second copy shipped
  // alongside the findings.
  function stepNames() {
    var out = [];
    var nodes = document.querySelectorAll('.machine-map [data-node]');
    for (var i = 0; i < nodes.length; i++) {
      var n = nodes[i].getAttribute('data-node');
      if (n) out.push(n);
    }
    return out;
  }
  function slugOf(name) {
    return String(name || '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
  }
  // Longest match wins, same as the server: one step may be named with
  // another's name as its prefix.
  function findingStep(names, line) {
    var best = '';
    names.forEach(function(n) {
      var lead = 'step ' + n;
      if (n.length <= best.length || line.indexOf(lead) !== 0) return;
      var next = line.charAt(lead.length);
      if (next === '' || next === ':' || next === ' ') best = n;
    });
    return best;
  }

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

  // Redraw the map when a step is saved. Ticking a choice ADDS AN ARROW
  // — the shape changed — and the map is rendered server-side, so
  // without this the picture kept describing the machine as it was
  // before the edit that was made while looking at it. Structural edits
  // that reload the page (add, remove, rename, reorder, kind) do not
  // need it; the ones that quietly change routing do.
  var pending = null;
  window.addEventListener('ui-data-changed', function(ev) {
    var sources = (ev.detail && ev.detail.sources) || [];
    // A step save, or the machine's own — "Starts at" is the entry the
    // whole layout is ranked FROM, so changing it can rearrange every
    // box on the map.
    var touched = sources.some(function(s) {
      s = String(s);
      return s.indexOf('/phases?name=') >= 0 || s.lastIndexOf('/meta') === s.length - 5;
    });
    if (!touched) return;
    var body = document.querySelector('.machine-map-body');
    var id = new URLSearchParams(window.location.search).get('id');
    if (!body || !id) return;
    // One redraw per burst: a checklist fires a save per box, and three
    // ticks should not be three fetches of the same picture.
    clearTimeout(pending);
    pending = setTimeout(function() {
      fetch('/orchestrate/api/machines/' + encodeURIComponent(id) + '/graph?links=1')
        .then(function(r) { return r.ok ? r.text() : null; })
        .then(function(svg) {
          if (!svg) return;
          body.innerHTML = svg;
          mark();
        })
        .catch(function() {});
      // The two derived lists, from the endpoint that already computes
      // both. This is the one place staleness costs something: it is
      // the list you work against, fixing one thing at a time, and it
      // said "3 to fix" until a reload however many you had fixed.
      fetch('/orchestrate/api/machines/' + encodeURIComponent(id) + '/editor')
        .then(function(r) { return r.ok ? r.json() : null; })
        .then(function(spec) {
          if (!spec) return;
          var c = document.querySelector('[data-machine-checklist]');
          var a = document.querySelector('[data-machine-advice]');
          paintFindings(c, spec.checklist || [], '\u2713 Nothing outstanding — this machine will run as written.', true, spec.rewrites);
          paintFindings(a, spec.advice || [], 'Nothing — the steps read as instructions rather than specifications.', false, spec.rewrites);
        })
        .catch(function() {});
    }, 250);
  });
})();`

// machineRewriteJS is the one finding whose fix is prose.
//
// Draft-and-review rather than a silent edit: which sentences are
// formatting instructions and which are the subject is a judgement, so
// the model proposes and the person keeps or discards. The workbench
// (uiOpenAssist) is the framework's, and it already holds every version
// including the original — so walking back from a bad suggestion to
// what was there is one click, which is what makes accepting one safe.
//
// It drives the SAME assist endpoint the step's own ✨ button does, with
// the finding as its opening request. A second endpoint here would be a
// second set of rules about what a step's instructions may say, and the
// two would drift.
const machineRewriteJS = `(function() {
  function machineID() { return new URLSearchParams(window.location.search).get('id'); }

  // Delegated: the findings lists are redrawn on every save, so a
  // handler bound to the buttons would survive exactly one edit.
  document.addEventListener('click', function(ev) {
    var btn = ev.target && ev.target.closest && ev.target.closest('[data-rewrite-step]');
    if (!btn) return;
    ev.preventDefault();
    var step = btn.getAttribute('data-rewrite-step');
    var id = machineID();
    if (!step || !id || !window.uiOpenAssist) return;
    var base = '/orchestrate/api/machines/' + encodeURIComponent(id);

    // Open on what is actually STORED rather than on the textarea in the
    // section, which may be mid-edit or not rendered yet — the rail
    // mounts one section at a time.
    btn.disabled = true;
    fetch(base)
      .then(function(r) { return r.ok ? r.json() : null; })
      .then(function(def) {
        btn.disabled = false;
        if (!def) throw new Error('could not read the machine');
        var phase = (def.phases || []).filter(function(p) { return p.name === step; })[0];
        if (!phase) throw new Error('no step called ' + step);
        window.uiOpenAssist({
          title: 'Rewrite the instructions — ' + step,
          subtitle: 'The declared fields already say what to produce. Keep the method; drop the format.',
          initial: phase.prompt || '',
          placeholder: 'Ask for another pass — shorter, keep the second paragraph…',
          ask: 'These instructions hand-roll the JSON the declared fields already produce. ' +
               'Rewrite them: delete the format instructions and any example object, keep everything ' +
               'that says HOW to do the work, and add nothing new. Reply with the instructions only.',
          send: function(req, done) {
            fetch(base + '/suggest', {
              method: 'POST',
              headers: {'Content-Type': 'application/json'},
              body: JSON.stringify({
                field: 'prompt',
                record: {name: step},
                message: req.message,
                draft: req.draft,
                history: req.history,
              }),
            })
              .then(function(r) {
                if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
                return r.json();
              })
              .then(function(d) { done({reply: d && d.reply, value: d && d.value}); })
              .catch(function(err) { done(null, (err && err.message) || String(err)); });
          },
          onAccept: function(text) {
            fetch(base + '/phases?name=' + encodeURIComponent(step), {
              method: 'POST',
              headers: {'Content-Type': 'application/json'},
              body: JSON.stringify({prompt: text}),
            })
              .then(function(r) {
                if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
                // Reload: the prompt is on screen in its own section and
                // the finding that sent you here is in two lists, all of
                // which just became wrong.
                window.location.reload();
              })
              .catch(function(err) {
                window.uiAlert && window.uiAlert('Could not save it: ' + (err && err.message || err));
              });
          },
        });
      })
      .catch(function(err) {
        btn.disabled = false;
        window.uiAlert && window.uiAlert('Could not open it: ' + (err && err.message || err));
      });
  });
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
