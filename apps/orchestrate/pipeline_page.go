// Pipelines, on the page where things are kept.
//
// Machines got a list and an editor page; pipelines had neither. They
// were authored from chat through the `pipeline` grouped tool and
// attached from a picker, which means the only way to answer "what
// pipelines do I have, and is any of them live" was to ask an agent.
// Everything the HTTP layer already served — list, export, import,
// delete, run — was reachable from nothing.
//
// This is the first half of that parity: the index, and a page per
// pipeline that shows what it is made of. The per-stage FORM is the
// second half; a stage has more shapes than a phase does (worker,
// agent, fanout, loop with a nested body, branch, tool) and building
// that against the wrong idea of what people edit is the mistake the
// machine editor spent three versions unwinding.

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
		Build: pipelinesExtensionSection,
		Head:  assignPillsHead,
		// Directly after Machines: they are the same kind of thing to
		// somebody looking for one, and a workflow that runs to an end
		// belongs next to a workflow a conversation sits in.
		Order: 11,
	})
}

// pipelinesExtensionSection is the index: what you have, what it is made
// of, and who can call it.
func pipelinesExtensionSection(r *http.Request, user string) (ui.Section, bool) {
	if !UserHasAppAccess(r, "/orchestrate") {
		return ui.Section{}, false
	}
	return ui.Section{
		Title: "Pipelines",
		Wide:  true,
		Subtitle: "Workflows that run start to finish and return a result — decompose, investigate, synthesize. " +
			"A pipeline attaches to an agent as a callable tool (run_<name>), so one that is attached to nothing is inert. " +
			"Author them from chat with the pipeline tool; this is where you see what you have.",
		Body: ui.Stack{Children: []ui.Component{
			// The three ways to get one, on a line: they are
			// alternatives, and stacked they read as three steps
			// somebody is meant to take in order.
			ui.Stack{Row: true, Children: []ui.Component{
				// The list could only IMPORT — there was no way to make
				// a pipeline from the UI at all, which is the same gap
				// machines had until v0.6.201: the page built for
				// keeping them could not start one.
				ui.Toolbar{Actions: []ui.ToolbarAction{{
					Label:   "New pipeline",
					Title:   "Start from a small pipeline that already runs",
					URL:     "/orchestrate/pipeline?new=1",
					Method:  "GET",
					Variant: "primary",
				}}},
				ui.Toolbar{Actions: []ui.ToolbarAction{{
					Label:  "Describe one…",
					Title:  "Draft a pipeline from a description of what it should do",
					URL:    "/orchestrate/pipeline?describe=1",
					Method: "GET",
				}}},
				// Import had nothing calling it until v0.6.221. A file
				// field reads the file as TEXT in the browser and submits
				// its contents, so this is a form rather than an upload.
				ui.ModalButton{
					Label:    "Import…",
					Title:    "Bring in a pipeline somebody exported",
					Subtitle: "Pick a .pipeline.json recipe. It lands as a pipeline of your own, with its own id.",
					Width:    "480px",
					Body: ui.FormPanel{
						PostURL:        "/orchestrate/api/pipelines/import",
						SubmitLabel:    "Import",
						RedirectURL:    "/orchestrate/pipeline?id={id}",
						RedirectTarget: "_self",
						Fields: []ui.FormField{{
							Field: "recipe", Type: "file", Accept: ".json",
							Label: "Recipe file",
							Help:  "The file an Export produced. Stages, prompts and wiring travel; nothing about the runs that ran it does.",
						}},
					},
				},
			}},
			ui.Table{
				Source:       "/orchestrate/api/pipelines",
				RecordsField: "pipelines",
				RowKey:       "id",
				RowLink:      "edit_url",
				Columns: []ui.Col{
					{Field: "name", Flex: 1},
					{Field: "stages", Label: "Stages", Mute: true},
					{Field: "status", Label: "Made of", Mute: true, Flex: 2},
					{Field: "used_by_text", Label: "Callable by", Mute: true, Flex: 2},
				},
				RowActions: []ui.RowAction{
					// Assignment from the list, same control the machines
					// table carries and the same one Extensions › Tools uses.
					{Type: "button", Label: "Assign", Method: "client",
						PostTo: "orchestrate_assign"},
					{Type: "button", Label: "Export", Method: "GET",
						PostTo: "/orchestrate/api/pipelines/{id}/export"},
					{Type: "button", Label: "Delete", Method: "DELETE",
						PostTo:     "/orchestrate/api/pipelines/{id}",
						Variant:    "danger",
						Confirm:    "Delete this pipeline? Agents that call it lose the tool; runs already finished keep their results.",
						Optimistic: true},
				},
				EmptyText: "No pipelines yet. A pipeline is a saved multi-step flow — decompose a question, research each part in parallel, then synthesize — that an agent can call as one tool. Ask Builder for one in chat.",
			},
		}},
	}, true
}

// handlePipelinePage is one pipeline, read end to end.
//
//	GET /orchestrate/pipeline?id=<pipeline>
func (T *OrchestrateApp) handlePipelinePage(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	// ?new=1 mints a pipeline that runs and lands you IN it, rather than
	// on a form asking for a name. The starter comes from the server
	// (StarterPipeline) and a test proves it validates, so the first
	// thing anybody sees is something that would run.
	if r.URL.Query().Get("new") == "1" {
		fresh := StarterPipeline()
		fresh.Owner = user
		fresh.ID = ""
		saved := SavePipelineDef(udb, fresh)
		Log("[orchestrate.pipelines] user=%q started a new pipeline (id=%s)", user, saved.ID)
		http.Redirect(w, r, "/orchestrate/pipeline?id="+saved.ID, http.StatusSeeOther)
		return
	}
	// ?describe=1 is the drafting form, on its own page rather than in a
	// dialog — drafting runs a model for up to a minute and can come
	// back with nothing runnable, and a door that cannot show you why it
	// failed is the wrong door for it (v0.6.220).
	if r.URL.Query().Get("describe") == "1" {
		T.servePipelineDescribePage(w, r, user)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	// Own, or one somebody shared (pipeline_sharing.go). A recipient sees the
	// same page — reading what a pipeline does is most of why it was shared —
	// and the sections that WRITE are simply not built for them, which is a
	// better answer than a form that posts and 403s.
	def, defOwner, mine, found := resolvePipelineFor(user, udb, id)
	if !found {
		http.NotFound(w, r)
		return
	}

	page := ui.Page{
		Title:     def.Name,
		ShowTitle: true,
		BackURL:   "/gateways",
		Nav:       HubNav("/gateways"),
		MaxWidth:  "100%",
		// One section at a time, with the stages as the rail: a pipeline
		// IS a list of stages, so the page's index should be that list.
		SectionNav: true,
		// The picture, pinned above every stage. Same reasoning as the
		// machine editor: SectionNav shows ONE section at a time, so a
		// diagram parked in a section of its own could never be on
		// screen with the stage being read — and a branch or a fanout is
		// exactly what a stage's own section cannot show.
		Sticky: pipelineMapCard(def),
		Head: ui.NewHead().CSS(pipelineStageCSS).
			ClientAction("pipeline_duplicate", pipelineDuplicateJS),
		Sections: []ui.Section{{
			Title:    "The pipeline",
			Wide:     true,
			Subtitle: "Its name and what it is for. Both are what an agent reads when deciding whether to call it, so the description is not decoration.",
			Body: ui.Stack{Children: []ui.Component{
				ui.FormPanel{
					Source:  "/orchestrate/api/pipelines/" + url_(def.ID),
					PostURL: "/orchestrate/api/pipelines/" + url_(def.ID),
					Method:  "PUT",
					Fields: []ui.FormField{
						{Field: "name", Type: "text", Label: "Name"},
						{Field: "description", Type: "textarea", Rows: 2, Label: "What it is for",
							Help: "One line. This is what an agent sees in its tool list, and the difference between a pipeline that gets called and one that does not."},
					},
				},
				ui.Toolbar{Actions: []ui.ToolbarAction{{
					Label:  "Export",
					Title:  "Download this pipeline's portable recipe",
					Method: "GET",
					URL:    "/orchestrate/api/pipelines/" + url_(def.ID) + "/export",
				}, {
					// A client action, not a POST toolbar button: the
					// toolbar fires the request and stays put, and the
					// point of duplicating is to work on the COPY.
					Label:  "Duplicate",
					Title:  "Make a copy to experiment on, and open it",
					Method: "client",
					URL:    "pipeline_duplicate",
					Data:   def.ID,
				}}},
			}},
		}},
	}
	// The order matches the machine editor's, section for section, so
	// somebody moving between the two finds the same things in the same
	// places: identity, then the parts, then running it, then the
	// wholesale change, what it costs, who gets it, and what is worth a
	// look. They had drifted into two different orders within a day of
	// each other, and the pinned test is what keeps them from doing it
	// again (TestBothEditorsPresentTheSameOrder).
	//
	// Build, rehearse, revise — in that order, because that is the loop.
	// "Say what should be different" presumes you know what is wrong, and
	// running it is what tells you; a redraft offered before the rehearsal
	// asks for a verdict nobody has earned yet. It also put the biggest
	// button on the page in front of somebody who came to edit one prompt.
	page.Sections = append(page.Sections, pipelineStageSections(def,
		editorCatalog{agents: agentOptions(udb, user), tools: phaseToolOptions(user),
			machines: childMachineOptions(udb, user, MachineDef{})})...)
	page.Sections = append(page.Sections,
		// Run it, here. The streaming endpoints have existed since a
		// PipelineDef could back an app (core/pipeline_runs.go), and
		// nothing on any page for the pipeline ITSELF called them — the
		// only way to try one you had just built was to attach it to an
		// agent and ask. Same class as every other door this work has
		// opened: the capability was there and unreachable.
		//
		// The framework's own panel, not a bespoke run box: a custom app
		// backed by a pipeline already mounts exactly this, so a
		// pipeline gets the transcript, the run history and the cancel
		// button by pointing at its own endpoints rather than growing a
		// second, worse version of them.
		ui.Section{
			Title:    "Run it",
			Wide:     true,
			NoChrome: true, // the panel manages its own layout
			Body: ui.PipelinePanel{
				SessionsListURL:  "api/pipelines/" + url_(def.ID) + "/sessions",
				SessionLoadURL:   "api/pipelines/" + url_(def.ID) + "/sessions/{id}",
				SessionDeleteURL: "api/pipelines/" + url_(def.ID) + "/sessions/{id}",
				SubmitURL:        "api/pipelines/" + url_(def.ID) + "/stream",
				SubmitLabel:      "Run it",
				// This page's ?id= is the PIPELINE. Without naming the param,
				// the panel's deep-link fallback reads it as a session id and
				// opens a run that cannot exist.
				DeepLinkParam: "session",
				Fields: []ui.PipelineField{{
					// "topic" because the stream endpoint accepts
					// input|topic and the panel titles a run from it.
					Name: "topic", Type: "textarea", Required: true, Rows: 3,
					Label:       "What should this run on?",
					Placeholder: "A real question. This is a REAL run: it spends model calls and its stages reach whatever tools they name.",
				}},
				// A stage transcript is prose, and a run history is
				// something you prune in batches.
				Markdown:   true,
				BulkSelect: true,
				EmptyText:  "No runs yet. This is the fastest way to find out whether a stage is doing what its prompt says.",
			},
		},
		// A form on the page, not a dialog: drafting runs a model for up
		// to a minute and can come back with nothing runnable, and a
		// door that cannot show you why is the wrong door for it
		// (v0.6.220).
		ui.Section{
			Title: "Describe a change",
			Wide:  true,
			Subtitle: "Say what should be different and the pipeline is redrafted with that change made, keeping every stage and prompt it does not touch. " +
				"A revision that would not run is refused rather than saved, and the version you have now is kept either way.",
			Body: ui.Stack{Children: []ui.Component{
				ui.FormPanel{
					PostURL:     "api/pipelines/" + url_(def.ID) + "/revise",
					SubmitLabel: "Revise it",
					RedirectURL: "/orchestrate/pipeline?id=" + url_(def.ID),
					Fields: []ui.FormField{{
						Field: "description", Type: "textarea", Rows: 4,
						Label:       "What should change?",
						Placeholder: "Research each query with the web tools instead of answering from memory, and add a stage that checks the sources before the summary.",
						Help:        "One change, in plain words. It runs a model, so it takes a moment; what it changed is reported when it lands.",
					}},
				},
				undoPipelineRevisionToolbar(def),
			}},
		},
		ui.Section{
			// Derived, not written: the definition knows which stages
			// call a model, and a fanout or a loop turns one line of a
			// stage list into twelve calls.
			Title:    "What a run costs",
			Wide:     true,
			Subtitle: pipelineCostText(def),
		},
		// Assign to agents. The list has said "callable by" since it
		// existed and nothing could change it — a pipeline attached to
		// nothing is a tool no agent has, which is the single most
		// useful fact about one and was read-only.
		ui.Section{
			Title: "Assign to agents",
			Wide:  true,
			Subtitle: "A pipeline reaches an agent as a tool named run_" + strings.ToLower(strings.ReplaceAll(def.Name, " ", "_")) + ". " +
				"Unlike a machine, an agent can hold several — checking one here adds this pipeline to that agent's list rather than replacing what it already has.",
			Body: ui.FormPanel{
				Source:  "api/pipelines/" + url_(def.ID) + "/agents",
				PostURL: "api/pipelines/" + url_(def.ID) + "/agents",
				Fields: []ui.FormField{{
					Field: "agents", Type: "checklist",
					Placeholder: "(no agents yet — create one in the chat sidebar first)",
					Options:     attachPipelineAgentOptions(udb, user),
				}},
			},
		},
		ui.Section{
			Title:    "What it will do",
			Wide:     true,
			Subtitle: "Worked out from the definition, without running anything — the order, what each stage is handed, and what a run costs before you pay for one.",
			Body:     ui.Card{HTML: planHTML(def)},
		},
		ui.Section{
			Title:    "Worth a look",
			Wide:     true,
			Subtitle: "Nothing here refuses a save. It is what the pipeline looks like it might not have meant.",
			Body:     ui.Card{HTML: findingsHTMLPlain(pipelineChecklist(udb, user, def), "Nothing — the stages read as instructions rather than specifications.")},
		},
	)
	// Share with users — the owner's decision, so the section exists only for
	// them. The recipient gets the note below instead: a page that shows a
	// picker you cannot use is a page that has to explain itself twice.
	if mine {
		page.Sections = append(page.Sections, ui.Section{
			Title: "Share with users",
			Subtitle: "Let specific other users read and run this pipeline. They run YOUR recipe against THEIR agents, tools and credentials — nothing of yours travels with the share, and nothing of theirs comes back. " +
				"Editing stays yours: a recipient can run it and take a copy, not change it. Empty = private to you. An admin can audit or revoke shares.",
			Body: ui.ACLPicker(ui.ACLPickerConfig{
				OptionsSource: "api/user-candidates",
				RecordSource:  "api/pipelines/" + url_(def.ID),
				Field:         "allowed_users",
				PostTo:        "api/pipelines/" + url_(def.ID) + "/share",
				Method:        "POST",
				Noun:          "user",
				Intro:         "Users who may run this pipeline.",
				EmptyText:     "No other users to share with yet.",
			}),
		})
	} else {
		page.Sections = append(page.Sections, ui.Section{
			Title:    "Shared with you",
			Subtitle: defOwner + " shared this pipeline with you. You can run it and duplicate it; the definition stays theirs, and it runs against your own agents, tools and credentials rather than " + defOwner + "'s.",
		})
	}
	page.ServeHTTP(w, r)
}

// servePipelineDescribePage is the drafting form: one box, one button,
// and room for what came back.
func (T *OrchestrateApp) servePipelineDescribePage(w http.ResponseWriter, r *http.Request, user string) {
	page := ui.Page{
		Title:     "Describe a pipeline",
		ShowTitle: true,
		BackURL:   "/gateways",
		Nav:       HubNav("/gateways"),
		Sections: []ui.Section{{
			Title: "What should it do?",
			Wide:  true,
			Subtitle: "Say what the work is, start to finish — what it works out first, what it does with each piece, what it produces. " +
				"A draft opens for you to adjust. A pipeline that would not run is refused rather than saved, so an empty result means the draft failed, not that it vanished.",
			Body: ui.FormPanel{
				PostURL:     "/orchestrate/api/pipelines/draft",
				SubmitLabel: "Draft it",
				RedirectURL: "/orchestrate/pipeline?id={id}",
				Fields: []ui.FormField{{
					Field: "description", Type: "textarea", Rows: 8,
					Label:       "In plain words",
					Placeholder: "Break a research question into separate sub-questions, look each one up on the web in parallel, then write one answer that cites what it found and says what it could not settle.",
					Help:        "It runs a model, so it takes a moment. If it cannot produce something that runs it says so here rather than failing quietly.",
				}},
			},
		}, {
			Title:    "The other two ways in",
			Wide:     true,
			Subtitle: "A starter pipeline that already runs, or a recipe somebody exported. Neither needs a model.",
			Body: ui.Toolbar{Actions: []ui.ToolbarAction{{
				Label:   "New pipeline",
				Title:   "Start from a small pipeline that already runs",
				URL:     "/orchestrate/pipeline?new=1",
				Method:  "GET",
				Variant: "primary",
			}, {
				Label:  "Back to the list",
				Title:  "Extensions, where your pipelines are kept",
				URL:    "/gateways",
				Method: "GET",
			}}},
		}},
	}
	Log("[orchestrate.pipelines] user=%q opened the describe form", user)
	page.ServeHTTP(w, r)
}

// pipelineDuplicateJS copies the pipeline and opens the copy — the
// point of duplicating is to work on the copy, so staying on the
// original would be the wrong half of the action.
const pipelineDuplicateJS = `function(ctx) {
  var id = ctx && ctx.action && ctx.action.data;
  if (!id) return;
  fetch('/orchestrate/api/pipelines/' + encodeURIComponent(id) + '/duplicate', {method: 'POST'})
    .then(function(r) {
      if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
      return r.json();
    })
    .then(function(d) { window.location.href = '/orchestrate/pipeline?id=' + encodeURIComponent(d.id); })
    .catch(function(err) {
      window.uiAlert && window.uiAlert('Could not duplicate it: ' + (err && err.message || err));
    });
}`

// undoPipelineRevisionToolbar offers to put back what a revision
// replaced, and only while there is something to put back.
func undoPipelineRevisionToolbar(def PipelineDef) ui.Component {
	if def.Previous == nil {
		return ui.Stack{}
	}
	return ui.Toolbar{Actions: []ui.ToolbarAction{{
		Label:   "Undo the revision",
		Title:   "Put back the version this pipeline had before the last Describe a change",
		Method:  "POST",
		URL:     "/orchestrate/api/pipelines/" + url_(def.ID) + "/undo",
		Variant: "danger",
		Confirm: "Put back the version from before the last revision? What the revision produced is discarded.",
	}}}
}

// pipelineMapCard is the picture: the whole pipeline, every box a door
// into that stage's section.
func pipelineMapCard(def PipelineDef) ui.Component {
	if len(def.Stages) == 0 {
		return ui.Stack{}
	}
	return ui.Card{HTML: `<div class="machine-map">` +
		`<div class="machine-map-cap">Map — click a stage to open it. A fanout is one box and many calls; a loop's body is drawn inside it.</div>` +
		`<div class="machine-map-body">` + pipelineGraphSVG(def) + `</div>` +
		`</div>`}
}

// pipelineGraphSVG draws the pipeline with each stage linking to its own
// section.
//
// WHERE a node links is the surface's knowledge, not the graph's — the
// adapter builds shape and this sets Href — so the anchors use the same
// slug the section nav computes from the section TITLES, which is what
// makes a drawn box a working door.
func pipelineGraphSVG(def PipelineDef) string {
	g := def.Graph()
	titles := map[string]string{}
	for i, s := range def.Stages {
		titles[strings.TrimSpace(s.Name)] = stageSectionTitle(i, s.Name)
	}
	for i := range g.Nodes {
		if title, ok := titles[g.Nodes[i].ID]; ok {
			g.Nodes[i].Href = "#" + ui.SectionSlug(title)
		}
	}
	return g.SVG(nil)
}

// stageSectionTitle is the ONE place a stage's section title is written,
// so the rail, the anchor and the box agree.
func stageSectionTitle(i int, name string) string {
	return strconv.Itoa(i+1) + ". " + name
}

// bodyStageSectionTitle names a step INSIDE a body. Prefixed with its
// parent because the rail lists every section flat, and two bodies may
// both hold a step called "check".
func bodyStageSectionTitle(parent, name string) string {
	return parent + " › " + name
}

// pipelineStageSections is one section per stage, in order, so the rail
// is the stage list and you read the pipeline the way it runs.
//
// A form per stage, plus the facts a form does not hold — what it
// returns, what a loop's body is. Those stay a card because they are
// derived: the declared output belongs to the stage spec and a loop's
// body is a stage list of its own, and inventing a control for either
// here would be a worse editor than the tool that writes them.
func pipelineStageSections(def PipelineDef, cat editorCatalog) []ui.Section {
	base := "api/pipelines/" + url_(def.ID) + "/stages"
	out := make([]ui.Section, 0, len(def.Stages))
	for i, s := range def.Stages {
		body := []ui.Component{
			ui.FormPanel{
				Source:  base + "?name=" + url_(s.Name),
				PostURL: base + "?name=" + url_(s.Name),
				Fields:  stageFormFields(def, s, cat),
			},
		}
		if extra := stageDerivedHTML(s); extra != "" {
			body = append(body, ui.Card{HTML: extra})
		}
		body = append(body, ui.Toolbar{Actions: []ui.ToolbarAction{{
			Label:   "Remove this stage",
			Title:   "Delete it. Refused while another stage still reads it, naming which.",
			Method:  "DELETE",
			URL:     "/orchestrate/" + base + "?name=" + url_(s.Name),
			Variant: "danger",
			Confirm: "Remove the stage \"" + s.Name + "\"?",
		}}})
		out = append(out, ui.Section{
			Title:    stageSectionTitle(i, s.Name),
			Wide:     true,
			Subtitle: stageSubtitle(s),
			Body:     ui.Stack{Children: body},
		})
		// A loop repeats these and a fanout may run one set per item, so
		// they are the stage's substance rather than a detail of it: each
		// gets its own section, indented, in the order it runs.
		out = append(out, pipelineBodySections(def, s, base, cat)...)
	}
	// The add form, last, so the rail ends where a new stage would go.
	out = append(out, ui.Section{
		Title:    "Add a stage",
		Wide:     true,
		Subtitle: "It lands at the end. Wire it up afterwards, where every option is a real stage.",
		Body: ui.FormPanel{
			PostURL:        "api/pipelines/" + url_(def.ID) + "/stages",
			SubmitLabel:    "Add it",
			RedirectURL:    "/orchestrate/pipeline?id=" + url_(def.ID),
			RedirectTarget: "_self",
			Fields: []ui.FormField{
				{Field: "name", Type: "text", Label: "Name",
					Help: "Lowercase, no dots — a dot would make {stage:a.b} ambiguous between a stage called a.b and field b of stage a."},
				{Field: "kind", Type: "select", Label: "What it does", Options: []ui.SelectOption{
					{Value: "worker", Label: "Worker — one model call"},
					{Value: "agent", Label: "Agent — dispatch to one of your agents"},
					{Value: "fanout", Label: "Fanout — run once per item of an earlier list"},
					{Value: "loop", Label: "Loop — repeat a body of stages"},
					{Value: "branch", Label: "Branch — read a bool and skip or stop"},
					{Value: "tool", Label: "Tool — call a tool directly"},
				}},
				{Field: "prompt", Type: "textarea", Rows: 4, Label: "Instructions",
					Help: "What it should DO. The rest is wired on its own panel once it exists."},
			},
		},
	})
	return out
}

// stageDerivedHTML is what the form does not hold: the contract this
// stage declares, and a loop's body.
func stageDerivedHTML(s PipelineStage) string {
	var facts []string
	// The parts of a declared field the rows control does NOT edit: a
	// list of legal values, and a nested shape. Shown only when a field
	// actually has one, so the note is never an unexplained absence —
	// and named as the tool's, because a control that half-edits a
	// structure is worse than one that says it does not.
	for _, f := range s.Output {
		if len(f.Enum) > 0 {
			facts = append(facts, f.Name+" may only be "+strings.Join(f.Enum, " | "))
		}
		if len(f.Fields) > 0 {
			inner := make([]string, 0, len(f.Fields))
			for _, n := range f.Fields {
				inner = append(inner, n.Name)
			}
			facts = append(facts, f.Name+" holds {"+strings.Join(inner, ", ")+"}")
		}
	}
	if len(s.Body) > 0 {
		inner := make([]string, 0, len(s.Body))
		for _, b := range s.Body {
			inner = append(inner, b.Name)
		}
		facts = append(facts, "body: "+strings.Join(inner, " → "))
	}
	if len(facts) == 0 {
		return ""
	}
	return `<div class="pipeline-stage-facts">` + HTMLEscape(strings.Join(facts, " · ")) +
		` — written with the pipeline tool, which owns the shapes that nest. Editing them here would half-edit a structure.</div>`
}

// stageSubtitle says what KIND of stage this is in one line, in the
// vocabulary the spec uses.
func stageSubtitle(s PipelineStage) string {
	kind := strings.TrimSpace(string(s.Kind))
	if kind == "" {
		kind = string(StageWorker)
	}
	switch PipelineStageKind(kind) {
	case StageAgent:
		return "Dispatched to " + chFirst(s.Agent, "an agent") + ", which answers with its own persona, tools and memory."
	case StageFanout:
		return "Runs once per element of " + chFirst(s.FanOver, "an earlier list") + ", in parallel, then collects the results into one block."
	case StageLoop:
		return "Repeats its body up to " + strconv.Itoa(s.Count) + " times" + chIf(s.Until != "", ", stopping early when "+s.Until, "") + "."
	case StageBranch:
		return "No model call: reads " + chFirst(s.When, "a bool") + " and " + chIf(s.SkipTo != "", "skips to "+s.SkipTo, "ends the pipeline") + " when it is true."
	case StageTool:
		return "Calls the " + chFirst(s.Tool, "(unnamed)") + " tool directly — no model, no tokens."
	}
	return "A worker step: one model call with this stage's instructions."
}

// stageDetailHTML is retained for the row-level preview the chat
// modal renders. The PAGE shows a form instead.
//
//nolint:unused

func stageDetailHTML(s PipelineStage) string {
	var b strings.Builder
	if p := strings.TrimSpace(s.Prompt); p != "" {
		b.WriteString(`<div class="pipeline-stage-prompt">` + HTMLEscape(p) + `</div>`)
	}
	var facts []string
	if len(s.Tools) > 0 {
		facts = append(facts, "tools: "+strings.Join(s.Tools, ", "))
	}
	if m := strings.TrimSpace(s.Model); m != "" {
		facts = append(facts, "model: "+m)
	}
	if s.Think != nil {
		facts = append(facts, "thinking: "+chIf(*s.Think, "on", "off"))
	}
	if len(s.Output) > 0 {
		names := make([]string, 0, len(s.Output))
		for _, f := range s.Output {
			n := f.Name
			if strings.TrimSpace(f.From) != "" {
				n += " (filled from " + f.From + ")"
			}
			names = append(names, n)
		}
		facts = append(facts, "returns: "+strings.Join(names, ", "))
	}
	if len(s.Body) > 0 {
		inner := make([]string, 0, len(s.Body))
		for _, b := range s.Body {
			inner = append(inner, b.Name)
		}
		facts = append(facts, "body: "+strings.Join(inner, " → "))
	}
	if len(facts) > 0 {
		b.WriteString(`<div class="pipeline-stage-facts">` + HTMLEscape(strings.Join(facts, " · ")) + `</div>`)
	}
	if b.Len() == 0 {
		return `<div class="pipeline-stage-facts">Nothing declared beyond its name.</div>`
	}
	return b.String()
}

// findingsHTMLPlain renders a findings list with no per-item link — the
// machine editor's version anchors each finding to the step it names,
// which needs sections addressed by slug. Pipelines will earn that when
// their stages become editable.
func findingsHTMLPlain(items []string, empty string) string {
	if len(items) == 0 {
		return `<div class="machine-findings-none">` + HTMLEscape(empty) + `</div>`
	}
	var b strings.Builder
	b.WriteString(`<ul class="machine-findings">`)
	for _, it := range items {
		b.WriteString(`<li>` + HTMLEscape(it) + `</li>`)
	}
	b.WriteString(`</ul>`)
	return b.String()
}

// chIf is the ternary Go does not have, for one-line prose assembly.
func chIf(cond bool, yes, no string) string {
	if cond {
		return yes
	}
	return no
}

// pipelineStageCSS. The findings classes are shared with the machine
// editor on purpose — a findings list should look the same wherever the
// framework shows one, and a second stylesheet is a second thing to
// keep in step.
const pipelineStageCSS = `
.pipeline-stage-prompt {
  white-space: pre-wrap; line-height: 1.5; font-size: 0.88rem;
}
.pipeline-stage-facts {
  margin-top: 0.5rem; font-size: 0.78rem; color: var(--text-mute);
}
/* The map, same shape and rules as the machine editor's: centred while
   it fits, scrolling from its left edge (where the first stage is) when
   it does not. */
.machine-map { padding: 0.3rem 0.5rem; }
.machine-map-cap {
  font-size: 0.72rem; letter-spacing: 0.04em; text-transform: uppercase;
  color: var(--text-mute); text-align: center;
}
.machine-map-body {
  overflow: auto; max-height: 42vh; padding-top: 0.35rem;
  display: flex; justify-content: center;
}
.machine-map-body > svg { flex: 0 0 auto; margin: 0 auto; }
.machine-map svg a { text-decoration: none; }
.machine-findings { margin: 0; padding-left: 1.1rem; }
.machine-findings > li { margin: 0 0 0.5rem 0; line-height: 1.5; }
.machine-findings > li:last-child { margin-bottom: 0; }
.machine-findings-none { color: var(--text-mute); }
`

// pipelineBodySections renders the stages inside one body-bearing stage,
// plus the form that adds another.
//
// Nothing for a stage that holds no body, and nothing for a kind that
// cannot: an "add a step" control under a worker stage would be an
// invitation to a refusal.
func pipelineBodySections(def PipelineDef, parent PipelineStage, base string, cat editorCatalog) []ui.Section {
	if parent.Kind != StageLoop && parent.Kind != StageFanout {
		return nil
	}
	out := make([]ui.Section, 0, len(parent.Body)+1)
	for _, b := range parent.Body {
		path := url_(parent.Name) + "." + url_(b.Name)
		out = append(out, ui.Section{
			Title:    bodyStageSectionTitle(parent.Name, b.Name),
			Wide:     true,
			Indent:   1,
			Subtitle: bodyStageSubtitle(parent, b),
			Body: ui.Stack{Children: []ui.Component{
				ui.FormPanel{
					Source:  base + "?name=" + path,
					PostURL: base + "?name=" + path,
					Fields:  bodyStageFormFields(def, b, cat),
				},
				ui.Toolbar{Actions: []ui.ToolbarAction{{
					Label:   "Remove this step",
					Title:   "Delete it from " + parent.Name + "'s body. Refused while another step in the same body reads it.",
					Method:  "DELETE",
					URL:     "/orchestrate/" + base + "?name=" + path,
					Variant: "danger",
					Confirm: "Remove " + strconv.Quote(b.Name) + " from " + strconv.Quote(parent.Name) + "?",
				}}},
			}},
		})
	}
	out = append(out, ui.Section{
		Title:    bodyStageSectionTitle(parent.Name, "add a step"),
		Wide:     true,
		Indent:   1,
		Subtitle: "It lands at the end of " + strconv.Quote(parent.Name) + "'s body. Wire it up afterwards, where every option is real.",
		Body: ui.FormPanel{
			PostURL:        base + "?parent=" + url_(parent.Name),
			SubmitLabel:    "Add it",
			RedirectURL:    "/orchestrate/pipeline?id=" + url_(def.ID),
			RedirectTarget: "_self",
			Fields: []ui.FormField{
				{Field: "name", Type: "text", Label: "Name",
					Help: "How the other steps in this body address it: {stage:NAME}. Unique within the body; lowercase, no dots."},
				{Field: "kind", Type: "select", Label: "What it does", Options: bodyKindOptions()},
				{Field: "prompt", Type: "textarea", Rows: 3, Label: "What it should do",
					Help: "Wire the rest of it afterwards, in its own section."},
			},
		},
	})
	return out
}

// bodyStageSubtitle says what a body step is and what it can see, which
// differs from a top-level stage in one way worth stating every time.
func bodyStageSubtitle(parent, b PipelineStage) string {
	sub := stageSubtitle(b)
	switch parent.Kind {
	case StageFanout:
		sub += " Runs once per item: {item} is the element, {branch} its number. What it produces is this branch's alone."
	case StageLoop:
		sub += " Runs every pass: {iteration} is the pass number, {prev} what the last one produced."
	}
	return strings.TrimSpace(sub)
}

// planHTML renders PipelineDef.Plan for the page.
//
// Two facts per stage, because they are the two a definition hides: what feeds
// it, and what it costs. The first is the one that bites — nothing
// auto-supplies a stage with the work before it, so a prompt with no
// placeholder sees its own text and nothing else, and the symptom at run time
// is a fluent answer to the wrong question rather than an error.
func planHTML(def PipelineDef) string {
	plan := def.Plan()
	if len(plan.Steps) == 0 {
		return `<div class="ui-mute">No stages yet — add one and this fills in.</div>`
	}
	var b strings.Builder
	b.WriteString(`<div style="font-weight:600;margin-bottom:0.5rem">` + HTMLEscape(plan.Summary()) + `</div>`)
	for _, s := range plan.Steps {
		indent := s.Depth * 18
		b.WriteString(`<div style="margin:0.35rem 0 0.35rem ` + strconv.Itoa(indent) + `px">`)
		b.WriteString(`<span style="font-weight:600">` + HTMLEscape(s.Name) + `</span>`)
		b.WriteString(`<span class="ui-mute"> — ` + HTMLEscape(s.RunBy) + `</span>`)
		if s.Max > 0 {
			calls := strconv.Itoa(s.Min)
			if s.Max != s.Min {
				calls = strconv.Itoa(s.Min) + "–" + strconv.Itoa(s.Max)
			}
			b.WriteString(`<span class="ui-mute"> · ` + calls + ` call` + HTMLEscape(pluralOf(s.Max)) + `</span>`)
		}
		b.WriteString(`<div class="ui-mute" style="font-size:0.82rem">`)
		if len(s.Reads) == 0 && s.Kind != StageTool && s.Kind != StageBranch {
			// The finding, stated where the shape is being read rather than
			// filed away in a checklist somebody opens separately.
			b.WriteString(`<span style="color:var(--danger)">reads nothing — its prompt places no {input}, {prev} or {stage:…}, so it sees only its own text</span>`)
		} else if len(s.Reads) > 0 {
			b.WriteString(`reads ` + HTMLEscape(strings.Join(s.Reads, ", ")))
		}
		if s.Note != "" {
			if len(s.Reads) > 0 || s.Kind == StageTool || s.Kind == StageBranch {
				b.WriteString(` · `)
			}
			b.WriteString(HTMLEscape(s.Note))
		}
		b.WriteString(`</div></div>`)
	}
	return b.String()
}

// pluralOf is the s, for a count rendered beside a noun.
func pluralOf(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
