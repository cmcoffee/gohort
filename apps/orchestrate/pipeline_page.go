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
			ui.Stack{Row: true, Children: []ui.Component{
				// Import exists in the HTTP layer and had nothing calling
				// it — the same gap machines had until v0.6.201. A file
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
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	def, found := LoadPipelineDef(udb, user, id)
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
		Head:   ui.NewHead().CSS(pipelineStageCSS),
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
				}}},
			}},
		}},
	}
	page.Sections = append(page.Sections, pipelineStageSections(def,
		editorCatalog{agents: agentOptions(udb, user), tools: availableWorkerToolOptions(user)})...)
	page.Sections = append(page.Sections,
		ui.Section{
			Title:    "Worth a look",
			Wide:     true,
			Subtitle: "Nothing here refuses a save. It is what the pipeline looks like it might not have meant.",
			Body:     ui.Card{HTML: findingsHTMLPlain(def.Advice(), "Nothing — the stages read as instructions rather than specifications.")},
		})
	page.ServeHTTP(w, r)
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
	if len(facts) == 0 {
		return ""
	}
	return `<div class="pipeline-stage-facts">` + HTMLEscape(strings.Join(facts, " · ")) +
		` — declared with the pipeline tool, which is where the shapes that need nesting belong.</div>`
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
