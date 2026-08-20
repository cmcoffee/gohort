// A machine editor made of questions instead of JSON.
//
// The editor was a textarea holding the definition, which is fine if you
// already know what a phase is and hopeless otherwise — people do not
// think in JSON, and the concepts here (resident vs transient, next vs
// next_from, a guard) are unfamiliar enough that the format was hiding
// the meaning rather than carrying it.
//
// So the fields ask questions at the point of choice. "Does the
// conversation wait here, or hand straight on?" is the resident toggle.
// "Then go to" is a select of phases that actually exist. "Or let this
// step decide" lists the output fields this phase declares, because
// next_from names one of them and a free-text box invites a name that
// does not.
//
// THE SPEC IS BUILT SERVER-SIDE. Not for purity: the selects need the
// machine's own phase names and its declared output fields, which only
// the server has, and the help text is the part that carries the
// concepts and belongs where it can be reviewed and tested rather than
// in a string inside a browser file.
//
// The JSON editor stays. It is what the `machine` tool writes, what
// extras/ ships, and the fastest path for someone who does know the
// shape. This is the other door, not a replacement.

package orchestrate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// machineEditorSpec is what the modal mounts: a checklist of what is
// still missing, then the components to render in order.
// editorCatalog is everything the editor offers that comes from outside
// the machine: the user's agents (for delegation) and their tool pool
// (for narrowing a step). Bundled so the next externally-sourced list is
// a field, not a fourth positional slice at twelve call sites.
type editorCatalog struct {
	agents    []ui.SelectOption
	tools     []ui.SelectOption
	pipelines []ui.SelectOption
	machines  []ui.SelectOption
	// checklist is work remaining, composed by the caller (machineChecklist)
	// because part of it — whether a phase's tool names resolve — depends on
	// the user's own pool and the agents attached to this machine, which the
	// definition alone cannot know.
	checklist []string
}

func machineEditorSpec(def MachineDef, cat editorCatalog) map[string]any {
	base := machineAPIBase(def)
	return map[string]any{
		"id":   def.ID,
		"name": def.Name,
		// Validate's own findings, as work remaining rather than as a
		// refusal. Same function behind both, so the list can never
		// disagree with what a save will accept.
		"checklist": cat.checklist,
		// Kept separate from the checklist: advice is a guess about
		// intent and must never read as "this is broken".
		"advice": def.Advice(),
		// Which of those findings a draft-and-review can settle, so the
		// live refresh redraws the same buttons the page rendered rather
		// than dropping them the first time anything is saved.
		"rewrites": def.PromptRewrites(),
		// The pieces, separately, so a PAGE can put each phase's panel in
		// its own section. "components" below is the same set flattened
		// for the modal, which mounts them in order into one body.
		//
		// The page used to index into that flat list and get this wrong:
		// with the panels wrapped in one Stack, the first phase's section
		// received EVERY phase's form and the second received the "add"
		// button. The menu and the diagram were right and the bodies
		// under them were not, which is a worse failure than either being
		// wrong on its own — it teaches that the structure means nothing.
		"meta":   metaPanel(def, base),
		"phases": phasePanels(def, base, cat),
		"add":    addPanel(def, base),
		"components": []any{
			metaPanel(def, base),
			ui.Stack{Children: phasePanels(def, base, cat)},
			addPanel(def, base),
		},
	}
}

// metaPanel is the machine's own fields — name, when to use it, where a
// conversation starts.
func metaPanel(def MachineDef, base string) ui.FormPanel {
	return ui.FormPanel{
		Source:  base + "/meta",
		PostURL: base + "/meta",
		Fields: []ui.FormField{
			// The page is titled with this, and so is the browser tab.
			// Renaming without a rebuild leaves both showing the old
			// name — the same staleness a step's rename had, and safe
			// for the same reason: ReloadOnChange commits on blur, never
			// mid-word.
			{Field: "name", Type: "text", Label: "Name", ReloadOnChange: true},
			{Field: "description", Type: "textarea", Rows: 2, Label: "When to use this",
				Help: "Shown wherever someone picks a machine. Say what kind of conversation it is for, not how it works."},
			{Field: "start", Type: "select", Label: "Starts at", Options: phaseOptions(def, false),
				Help: "The step a new conversation begins in. Usually the one that decides what kind of turn this is."},
			// No ReloadOnChange, though this DOES invert the checklist (the
			// resident rule flips both ways). A meta save already triggers
			// the page's redraw, which re-fetches the editor spec and
			// repaints the findings, so the list is right without a rebuild.
			// Nothing on the page is TITLED by this flag, which is what the
			// name field's reload is for.
			{Field: "unattended", Type: "toggle", Label: "This RUNS instead of converses",
				Help: "OFF: a conversation. Somebody talks to it, and one step waits for their replies. " +
					"ON: a job, started once, with nobody watching. Two things change, and they are the inverse of a conversation: " +
					"NO step may wait for the person (turn off \"the conversation waits here\" on every one), and ONE step must finish it " +
					"by handing off nowhere — that step's result is the run's result. " +
					"An existing conversational machine will not simply flip: its waiting steps are what a run cannot use. " +
					"Use it for work that takes many steps and no input: an overnight investigation, a nightly report."},
		},
	}
}

// addPanel mints a NEW step. Deliberately short: a step that does not
// exist yet has no fields to route on, and offering those choices anyway
// invites a guess. It is wired up in its own section afterwards, where
// every option is real.
func addPanel(def MachineDef, base string) ui.ModalButton {
	return ui.ModalButton{
		Label:    "Add a step",
		Title:    "Add a step",
		Subtitle: "One part of the conversation. Name it and say what it does; wire where it goes afterwards.",
		Width:    "640px",
		Body: ui.FormPanel{
			PostURL:     base + "/phases",
			SubmitLabel: "Add step",
			// The sections, the rail and every other step's selects are
			// built server-side from the phase list, so a step added in
			// the browser exists nowhere on screen until the page is
			// rebuilt. Rather than leave somebody looking at a dialog
			// that closed and changed nothing, land them ON the new
			// step — the redirect carries the section anchor, so the
			// editor reopens with that step's form already open.
			RedirectURL:    "/orchestrate/machine?id={id}#{slug}",
			RedirectTarget: "_self",
			Fields:         phaseFormFields(def),
		},
	}
}

// setRoutingTargets stores the allowed values on the field next_from
// names. A no-op when the phase routes statically — there is nowhere to
// put them, and inventing a field to hold them would be worse.
func setRoutingTargets(ph *MachinePhase, targets []string) {
	from := strings.TrimSpace(ph.NextFrom)
	if from == "" {
		return
	}
	for i := range ph.Output {
		if ph.Output[i].Name == from {
			ph.Output[i].Enum = targets
			return
		}
	}
}

// routingTargetsOf reads them back for the form.
func routingTargetsOf(p MachinePhase) []string {
	from := strings.TrimSpace(p.NextFrom)
	if from == "" {
		return nil
	}
	for _, f := range p.Output {
		if f.Name == from {
			return f.Enum
		}
	}
	return nil
}

// machineAPIBase is where this machine's endpoints live, relative to the
// page. One definition, because the panels, the preview and the assist
// all address the same machine.
func machineAPIBase(def MachineDef) string { return "api/machines/" + url_(def.ID) }

// toolChecklistOptions is the user's tool pool, plus any tool this step
// already names that the pool does not offer.
//
// The extras matter on an imported machine: its steps can name tools
// this deployment simply does not have, and a checklist only persists
// what it can show — so a name left off the list would be silently
// dropped by the next save. Broken-dependency posture: keep it, label
// it, and let the person uncheck it on purpose.
func toolChecklistOptions(offered []ui.SelectOption, current []string) []ui.SelectOption {
	// The explicit "nothing", first, because it is the one choice an empty
	// list can no longer express: unchecked means everything here, in both
	// kinds of step, and a control that can only widen is half a control.
	offered = append([]ui.SelectOption{{
		Value: noToolsSentinel, Label: "No tools at all",
		Group: "Nothing",
		Help:  "This step only decides or reshapes what it was given. Wins over anything else ticked below.",
	}}, offered...)
	known := make(map[string]bool, len(offered))
	for _, o := range offered {
		known[o.Value] = true
	}
	out := offered
	for _, name := range current {
		if name = strings.TrimSpace(name); name == "" || known[name] {
			continue
		}
		known[name] = true
		out = append(out[:len(out):len(out)], ui.SelectOption{
			Value: name, Label: name,
			Group: "Named by this machine, not available here",
			Help:  "kept so a save does not lose it; uncheck to drop it",
		})
	}
	return out
}

// phasePanels builds one editing panel per phase, each carrying the
// choices computed for THAT phase. Order follows the machine's own,
// which is the order somebody reads them in.
func phasePanels(def MachineDef, base string, cat editorCatalog) []ui.Component {
	out := make([]ui.Component, 0, len(def.Phases))
	for _, p := range def.Phases {
		q := base + "/phases?name=" + url_(p.Name)
		children := []ui.Component{
			// Order IS the reading order here — the rail, and which waiting
			// step catches a hand-off — so moving a step is frequent work.
			// It sat under the collapsed reference block at the very bottom,
			// which is where you put something nobody needs twice.
			ui.Toolbar{Actions: []ui.ToolbarAction{{
				Label:  "↑ Move up",
				Title:  "Move " + p.Name + " one place earlier. Order is the reading order and which waiting step catches a step that hands off nowhere. In the map it only swaps steps that sit at the same depth AND come from the same step — everything else is placed under whatever leads to it.",
				Method: "client",
				URL:    "machine_move_step",
				Data:   p.Name + "|up",
			}, {
				Label:  "↓ Move down",
				Title:  "Move " + p.Name + " one place later. Order is the reading order and which waiting step catches a step that hands off nowhere. In the map it only swaps steps that sit at the same depth AND come from the same step — everything else is placed under whatever leads to it.",
				Method: "client",
				URL:    "machine_move_step",
				Data:   p.Name + "|down",
			}}},
			ui.FormPanel{Source: q, PostURL: q, Fields: phaseFieldsFor(def, p, cat)},
		}
		// The tool limit saves on its own, so no other setting on this step
		// can carry it (see phaseToolFields).
		if phaseShowsTools(p) {
			children = append(children, ui.FormPanel{Source: q, PostURL: q, Fields: phaseToolFields(p, cat)})
		}
		children = append(children,
			// What the framework adds around the author's text, rendered
			// from the function a live turn calls. Collapsed, greyed, and
			// underneath: it is reference, not input.
			phasePreview(def, p),
			// Remove sits WITH the step it removes. It lived on the old
			// table's row and did not survive the move to per-step
			// sections, which left an editor that could only ever grow.
			//
			// A client action rather than a plain DELETE toolbar button:
			// the toolbar fires the request and does not refresh, so the
			// removed step would sit on screen looking removed-but-not.
			// Alone down here, deliberately: the two controls that used to
			// share this row are things you click often, and this one is
			// the one you cannot undo.
			ui.Toolbar{Actions: []ui.ToolbarAction{{
				Label:   "Remove this step",
				Title:   "Delete " + p.Name + " from this machine",
				Method:  "client",
				URL:     "machine_remove_step",
				Data:    p.Name,
				Variant: "danger",
				Confirm: removeStepConfirm(def, p.Name),
			}}},
		)
		out = append(out, ui.Stack{Children: children})
	}
	return out
}

// removeStepConfirm names what ELSE breaks, because the answer is
// knowable and the person deciding cannot see it from here.
func removeStepConfirm(def MachineDef, name string) string {
	// What ELSE this touches, worked out from the same walk that will do
	// the touching — so the warning cannot promise something the removal
	// does not do.
	preview := def
	preview.Phases = append([]MachinePhase(nil), def.Phases...)
	rewritten := preview.RemoveStep(name)

	msg := "Remove the step \"" + name + "\"?"
	if def.StartPhase() == name {
		msg += " This is where a conversation STARTS, so whichever step is first becomes the new beginning."
	}
	// A step whose ONLY way onward was this one is the case worth
	// spelling out: it is the one thing the removal cannot decide for
	// somebody, and it will sit in the checklist until they do.
	var stranded []string
	for _, p := range def.Phases {
		if p.Name != name && !p.Resident && strings.TrimSpace(p.Next) == name && len(p.RoutingChoices()) == 0 {
			stranded = append(stranded, p.Name)
		}
	}
	if others := withoutName(rewritten, "start"); len(others) > 0 {
		msg += " References to it in " + strings.Join(others, ", ") + " are removed with it."
	}
	if len(stranded) > 0 {
		msg += " " + strings.Join(stranded, " and ") + " then has nowhere to go, and you will need to say where."
	}
	return msg + " This cannot be undone."
}

// withoutName drops one entry from a list of step names — the pseudo
// entry RemoveStep uses to report that it moved the machine's start.
func withoutName(names []string, drop string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n != drop {
			out = append(out, n)
		}
	}
	return out
}

// phaseOptions lists the machine's phases for a select. withNone adds an
// empty choice for the fields where "nowhere" is a legitimate answer.
func phaseOptions(def MachineDef, withNone bool) []ui.SelectOption {
	var out []ui.SelectOption
	if withNone {
		out = append(out, ui.SelectOption{Value: "", Label: "— nowhere —"})
	}
	for _, p := range def.Phases {
		out = append(out, ui.SelectOption{Value: p.Name, Label: p.Name})
	}
	return out
}

// staticValueOptions is the wheel on the field NAME: the built-ins, by
// the name a field takes when it holds one.
//
// Built from the same table everything else reads, so a variable added
// to the vocabulary is selectable here without anybody remembering to
// come back. {state:…} is deliberately NOT offered — those name another
// step and its field, which is a picker with two dependent levels rather
// than a wheel, and typing one into "from" stays available.
func fieldKindOptions() []ui.SelectOption {
	out := []ui.SelectOption{{Value: "", Label: "— choose —"}}
	for _, v := range MachineVars() {
		if v.Ref == "{established}" {
			continue // a whole block is not one field's value
		}
		name := BuiltinFieldName(v.Ref)
		out = append(out, ui.SelectOption{Value: name, Label: name + " — " + v.Means})
	}
	// Last, and named plainly: this is the ordinary case, and the word
	// for it should be the one an author would use to a colleague. The
	// sentence version ("something this step works out") reads as a
	// description of one field rather than the standing choice it is.
	return append(out, ui.SelectOption{Value: customFieldKind, Label: "Variable"})
}

// customFieldKind is the kind marker for a field the step establishes
// itself. It is never a field NAME — outputsFromAny reads the typed name
// for these rows — so it cannot collide with the built-in vocabulary.
const customFieldKind = "custom"

// toolsLabel and toolsHelp say what the tools list MEANS here, which is
// two different things.
//
// A step the conversation waits in runs as the turn itself, so it has
// the agent's whole catalog and this narrows it — empty means
// everything. A step that passes on runs before the turn has a catalog
// at all and gets exactly what it names — empty means NOTHING. Saying
// "empty inherits all" in both places (as this did) tells an author
// their investigating step has every tool, when in fact it has none.
func toolsLabel(p MachinePhase) string {
	if p.Resident {
		return "Only these tools"
	}
	return "Tools this step may use"
}

// toolsShowWhen hides the list under a delegate only while there is
// nothing in it to take back out.
func toolsShowWhen(p MachinePhase) string {
	if len(p.Tools) > 0 {
		return ""
	}
	return "!agent"
}

// pipelineShowWhen hides the pipeline choice while a delegate owns the
// step, and keeps it visible when one is already stored so it can be
// removed. Same rule as toolsShowWhen, same reason: a stored value behind
// a control nothing can open is a finding nobody can act on.
func pipelineShowWhen(p MachinePhase) string {
	if strings.TrimSpace(p.Pipeline) != "" {
		return ""
	}
	return "!agent"
}

// childMachineShowWhen hides the child-run choice while something else
// already runs the step, and keeps it visible when one is stored so it can
// be removed. Same rule as its two siblings.
func childMachineShowWhen(p MachinePhase) string {
	if strings.TrimSpace(p.Machine) != "" {
		return ""
	}
	return "!agent;!pipeline"
}

// delegatedShowWhen is the same rule for the single-value settings under
// a delegate. They are not even inert: when the delegate does not exist
// in this deployment the phase runs INLINE with exactly these, which is
// the case a portable machine meets most often.
func delegatedShowWhen(stored string) string {
	if strings.TrimSpace(stored) != "" {
		return ""
	}
	return "!agent"
}

// routingShowWhen keeps the two routing mechanisms exclusive while only
// one is in use, and shows BOTH when a step somehow has both.
//
// This was the sharpest instance of the whole class: choices hid itself
// behind "!next_from" and next_from hid itself behind "!choices", so a
// step carrying both — which no editor session produces but an import,
// an older save or the tool can — showed NEITHER control, while the
// checklist reported "keep one". There was no way to keep one.
func routingShowWhen(p MachinePhase, expr string) string {
	if len(p.Choices) > 0 && strings.TrimSpace(p.NextFrom) != "" {
		return ""
	}
	return expr
}

func toolsHelp(p MachinePhase) string {
	// The one state where this control is visible and inert. Saying so
	// where the control is beats leaving somebody to find it in a
	// findings list two sections down.
	if strings.TrimSpace(p.Agent) != "" {
		return "This step is delegated, so these do nothing — the delegate works from its own catalog. " +
			"Untick them, or clear the delegate above to let this step do the work itself."
	}
	if p.Resident {
		return "Check tools to narrow the agent's catalog while the conversation waits here; none checked = everything it normally has. " +
			"What the agent's attached SOURCES grant comes along either way — attaching is what granted those, and this list picks from the worker pool. " +
			"Tick one of a source's tools and this list governs them too, so you can say \"that source, not the others\". " +
			"Note this changes the tool list mid-conversation, which re-writes the cached prompt prefix."
	}
	return "Check tools to narrow what this step may reach; none checked = everything the agent normally has, the same rule a step that waits here follows. " +
		"What the agent's attached SOURCES grant comes along either way, unless you tick one of their tools and take charge of them here. " +
		"For a step that only decides or reshapes what it was given, tick \"No tools at all\" — it runs before the turn has a catalog, so that also spares it the cost of building one. " +
		"For work that needs a whole different reach, delegate the step to an agent instead (above)."
}

// builtinNameExpr is the row condition "this field IS a built-in",
// written in the form's own show_when grammar and built from the same
// table the resolver reads.
//
// Once a row is one of these there is nothing left to decide about it:
// the name is the choice, the value comes from the framework, and the
// type is text. So the name locks and the rest of the row goes away —
// picked once when the field is added, never edited into something it
// cannot be.
func builtinNameExpr() string {
	return "builtin:" + strings.Join(builtinFieldNames(), "|")
}

// builtinOrUnansweredExpr matches a row that is NOT a field of the
// step's own: one holding a built-in, or one whose kind is still
// unanswered. Everything that describes a field the step works out
// hides behind it, so a fresh row is one question — pick a built-in, or
// say this is something the step establishes — rather than a name box
// inviting you to start typing before deciding what you are naming.
//
// The trailing empty alternative is the unanswered case: the row
// condition grammar compares values as strings, so "" is a value like
// any other.
func builtinOrUnansweredExpr() string {
	return builtinNameExpr() + "|"
}


// builtinFieldNames is the vocabulary by the name a field takes when it
// holds one, minus the block that no single field can hold.
func builtinFieldNames() []string {
	var names []string
	for _, v := range MachineVars() {
		if v.Ref == "{established}" {
			continue
		}
		names = append(names, BuiltinFieldName(v.Ref))
	}
	return names
}

// builtinVarHelp writes the fixed vocabulary out, from the table core
// resolves against (MachineVars).
//
// Generated rather than described, for the reason everything else on
// this page is: a variable documented in one file and implemented in
// another is how one of them silently stops being true. These need no
// declaring and mean the same thing in every machine, which is exactly
// what makes them worth listing at the point somebody is writing.
func builtinVarHelp() string {
	var parts []string
	for _, v := range MachineVars() {
		part := v.Ref + " — " + v.Means
		if v.Auto != "" {
			part += " (" + v.Auto + ")"
		}
		parts = append(parts, part)
	}
	return "Always available: " + strings.Join(parts, "; ") + "."
}

// stateRefsAvailableTo lists the {state:...} references a phase can
// actually use — every field declared by every OTHER phase, spelled the
// way it goes in a prompt.
//
// This is the single biggest piece of guesswork in authoring one of
// these: the prompt is where somebody types {state:triage.observation}
// from memory, and Validate only says otherwise after a save. Computing
// it removes the memory step entirely.
func stateRefsAvailableTo(def MachineDef, phase string) []string {
	var out []string
	for _, p := range def.Phases {
		if p.Name == phase {
			continue // a phase cannot read what it is about to write
		}
		for _, f := range p.Output {
			if f.Name != "" {
				out = append(out, "{state:"+p.Name+"."+f.Name+"}")
			}
		}
	}
	return out
}

// ownFieldOptions lists the fields THIS phase declares, for next_from.
// The shared form could only offer every field in the machine and hope;
// a per-phase form can offer exactly the ones that are legal here.
func ownFieldOptions(p MachinePhase) []ui.SelectOption {
	out := []ui.SelectOption{{Value: "", Label: "— always the same phase —"}}
	for _, f := range p.Output {
		if f.Name == "" {
			continue
		}
		if f.Type != "" && f.Type != "string" {
			continue // only a string can name a phase
		}
		out = append(out, ui.SelectOption{Value: f.Name, Label: f.Name})
	}
	return out
}

// otherPhaseOptions lists every phase except this one — for keep, which
// names phases whose findings survive re-entry.
func otherPhaseOptions(def MachineDef, self string) []ui.SelectOption {
	var out []ui.SelectOption
	for _, p := range def.Phases {
		if p.Name != self {
			out = append(out, ui.SelectOption{Value: p.Name, Label: p.Name})
		}
	}
	return out
}

// attachAgentOptions lists the user's agents for the "Assign to agents"
// checklist, each labelled with what it runs TODAY when that is some
// other machine — moving an agent should be a choice made while looking
// at what it costs, not a discovery.
func attachAgentOptions(udb Database, user string, def MachineDef) []ui.SelectOption {
	names := map[string]string{}
	for _, m := range ListMachineDefs(udb, user) {
		names[m.ID] = m.Name
	}
	var out []ui.SelectOption
	for _, a := range listAgents(udb, user) {
		if isAppAgent(a.ID) || a.Hidden {
			continue
		}
		opt := ui.SelectOption{Value: a.ID, Label: chFirst(a.Name, a.ID)}
		if a.Machine != "" && a.Machine != def.ID {
			if n := names[a.Machine]; n != "" {
				opt.Help = "runs “" + n + "” today — checking this moves it here"
			} else {
				opt.Help = "points at a machine that no longer exists — checking this repairs it"
			}
		}
		out = append(out, opt)
	}
	return out
}

// agentOptions lists the user's agents, so delegation is a choice rather
// than a remembered name.
func agentOptions(udb Database, user string) []ui.SelectOption {
	out := []ui.SelectOption{{Value: "", Label: "— this agent runs the step —"}}
	for _, a := range listAgents(udb, user) {
		if isAppAgent(a.ID) || a.Hidden {
			continue
		}
		out = append(out, ui.SelectOption{Value: a.ID, Label: chFirst(a.Name, a.ID)})
	}
	return out
}

// pipelineOptions lists the user's pipelines so a step picks one rather
// than somebody typing a name from memory. Stores the NAME, not the id: a
// machine is portable and an exported recipe should name the pipeline
// somebody wrote, not the row it happened to live in here.
func pipelineOptions(udb Database, user string) []ui.SelectOption {
	out := []ui.SelectOption{{Value: "", Label: "— this agent runs the step —"}}
	for _, d := range ListPipelineDefs(udb, user) {
		out = append(out, ui.SelectOption{Value: d.Name, Label: chFirst(d.Name, d.ID), Help: firstLine(d.Description)})
	}
	return out
}

// childMachineOptions lists the machines a step may run as a child: the
// user's own, and only the ones that RUN. A conversational machine offered
// here would be a choice that cannot work, and finding that out at run time
// is worse than not being offered it.
//
// The machine being edited is excluded. Depth is capped at one, so a
// machine running itself is a run that could never complete.
func childMachineOptions(udb Database, user string, self MachineDef) []ui.SelectOption {
	out := []ui.SelectOption{{Value: "", Label: "— this agent runs the step —"}}
	for _, d := range ListMachineDefs(udb, user) {
		if !d.Unattended || d.ID == self.ID {
			continue
		}
		out = append(out, ui.SelectOption{Value: d.Name, Label: chFirst(d.Name, d.ID), Help: firstLine(d.Description)})
	}
	return out
}

// phaseFieldsFor builds the form for ONE phase, with every choice
// computed for it: the fields it declares, the phases it can reach, the
// state it can read. Nothing here is typed from memory.
func phaseFieldsFor(def MachineDef, p MachinePhase, cat editorCatalog) []ui.FormField {
	refs := stateRefsAvailableTo(def, p.Name)
	promptLabel := "How to go about it"
	promptHelp := "The METHOD, not the output. What each field below should contain is already an instruction — the framework sends every field you declare, with the description you gave it — so this is for what a list of fields cannot say: where to look first, what a good answer requires, and the mistake this step tends to make. " +
		"\"Read enough to have a real hypothesis rather than a plausible one\" belongs here; \"return a hypothesis field\" does not. " +
		"Open \"What this step actually receives\" below to read exactly what it gets."
	if p.Resident {
		promptLabel = "Instructions for this step"
		promptHelp = "What the agent should be doing HERE, layered on top of its own persona. Write the JOB, not the identity, and write it to a person. " +
			"What earlier steps established and where else the conversation can go are composed for you — do not paste them in. " +
			"Open \"What this step actually receives\" below to read exactly what it gets."
	} else if len(p.Output) == 0 {
		promptHelp = "What this step should do. It declares no fields yet, so this text is the whole instruction — " +
			"once you add fields below, each one becomes an instruction of its own and this can shrink to the method: where to look, what a good answer requires, what usually goes wrong."
	}
	if p.Resident {
		promptHelp += " This prompt is pinned across turns, so {input}, {prev} and {now} do not exist here — the person's message is already in the conversation, and what earlier steps established is composed for you. The stable variables work: {original_input}, {user}, {agent}, {step}, {machine}."
	} else {
		promptHelp += " You do not have to ask for the person's message or for what earlier steps worked out: both are handed to this step whether you mention them or not. " +
			"Place a variable only when you want the value INSIDE a sentence. " + builtinVarHelp()
	}
	if len(refs) > 0 {
		promptHelp += " One field at a time, if you need it that way: " + strings.Join(refs, "  ") +
			" — but the same values already arrive pinned, so reach for one only when the phrasing genuinely matters."
	}

	fields := []ui.FormField{
		// Renaming rewrites every reference machine-wide (RenameStep) and
		// reloads the page: the rail, the other steps' selects and this
		// form's own post URL all carry the old name until it does.
		{Field: "name", Type: "text", Label: "Name", ReloadOnChange: true,
			Help: "Short handle, lowercase. It is how other steps point here. Renaming updates every step that refers to this one."},
		{Field: "desc", Type: "text", Label: "What this step does",
			Help: "One line, for whoever reads the machine later. Not shown to the agent."},
		// The ✨ opens the shared assist workbench: a draft beside a
		// conversation about it. Worth having HERE more than anywhere
		// else in the app, because this is the one box whose right answer
		// depends on parts the author cannot see — what the framework
		// composes around it, and what the other steps already establish.
		// The endpoint knows all of that (machine_suggest.go).
		{Type: "header", Label: "What kind of step is this?",
			Help: "The one answer everything below depends on. A step the conversation WAITS in replies to the person and cannot record fields — its reply goes to them, not to a decoder. A step that passes through records what it worked out and hands to the next one."},
		// The sections below are built from this answer, server-side, so
		// the promise in the help text ("the sections below change to
		// match") is only true if the page rebuilds. Toggling it left a
		// step showing controls its new kind cannot use — an output
		// contract on a step that now waits, a guard on one that does
		// not — which is the same lie as an added step that never
		// appears. A toggle saves on change, never on a debounce, so
		// reloading here costs nothing typed.
		{Field: "resident", Type: "toggle", Label: "The conversation waits here", ReloadOnChange: true,
			Help: "ON: a turn ENDS here and the person replies into it — this is where a conversation lives, and the sections below change to match. OFF: the step runs, records what it establishes, and passes straight on within the same turn. A machine needs at least one step with this on, or a turn has nowhere to finish."},
	}
	if !p.Resident {
		// Filed WITH the kind toggle, because it is the same class of
		// question: both reshape the form rather than fill it in. A
		// delegate takes over the model, the reasoning and the tools —
		// which is why those controls disappear under one — so an author
		// who answers this last has configured three things that stopped
		// applying while they were doing it.
		fields = append(fields,
			ui.FormField{Field: "pipeline", Type: "select", Label: "Or run it through a pipeline",
				Options: cat.pipelines, ShowWhen: pipelineShowWhen(p),
				Help: "A pipeline is a RECIPE rather than an agent: fixed stages, fan out over a list, loop until something is true. " +
					"Reach for it when the step is a procedure you want run the same way every time, and for an agent when it needs judgement and its own tools. " +
					"A pipeline whose last stage declares the same fields this step declares costs one model call instead of two, because the shape it produced is taken as the step's own."},
			ui.FormField{Field: "machine", Type: "select", Label: "Or run a whole machine for it",
				Options: cat.machines, ShowWhen: childMachineShowWhen(p),
				Help: "A CHILD RUN: another machine, started for this step, with its own steps and its own working set, " +
					"and its result comes back as this step's. Only machines marked \"this RUNS instead of converses\" can be run this way, " +
					"because nobody is waiting inside a step. Depth is capped at one, so a child may not run a child. " +
					"Reach for it when the work is a smaller version of the same shape."},
			ui.FormField{Field: "agent", Type: "select", Label: "Who runs this step", Options: cat.agents,
				Help: "Leave it with this agent, or give it to another one — with its own persona, tools and memory. A delegate gets the instructions below, works, and reports back; what it reports is recorded further down. Use it when the work needs different REACH, not different wording. How the step runs — model, reasoning, tools — becomes the delegate's own configuration."},
		)
	}
	// The instructions come AFTER the two questions that decide what they
	// are FOR. Both change this box under the author's hand — its label,
	// its help, and the whole group beneath it — so a form that asked for
	// ten lines of prose first was asking somebody to write against a
	// spec they had not been shown yet.
	fields = append(fields,
		ui.FormField{Field: "prompt", Type: "textarea", Rows: 10, Label: promptLabel, Help: promptHelp,
			SuggestURL:   machineAPIBase(def) + "/suggest",
			AssistPrompt: "Draft the method for this step: how to go about the work, in plain sentences written to a person. Not the output shape, which the declared fields already carry."},
	)
	if p.Resident {
		fields = append(fields,
			// The section keeps its place in the reading order even
			// though it has no controls. Flipping a step to "waits here"
			// used to make "What this step establishes" vanish without a
			// word, which reads as a bug — the rule is worth a sentence
			// exactly where the thing it removed used to be.
			ui.FormField{Type: "header", Label: "What this step establishes",
				Help: "Nothing, and that is the rule rather than an omission. A step the conversation waits in replies to the PERSON, " +
					"so there is no decoder to hand fields to, and its reply is never pinned to the blackboard — that would paste it into every later step's prompt, forever. " +
					"Anything later steps need has to be worked out by the step that feeds this one."},
			ui.FormField{Field: "next", Type: "select", Label: "After one turn, go to", Options: phaseOptions(def, true),
				Help: "Leave empty for the usual case — the conversation stays here. Set it to make this a ONE-turn step: it replies once, then moves on. That is how an intake beat asks its questions and continues."},
			ui.FormField{Type: "header", Label: "Leaving early"},
			ui.FormField{Field: "guard", Type: "textarea", Rows: 2, Label: "Leave this step when…",
				Placeholder: "the person has moved to a different problem",
				Help: "Plain words, judged on each new turn that arrives here. Empty means the conversation stays until something else moves it. Costs one model call per turn, so say something worth checking."},
			ui.FormField{Field: "guard_to", Type: "select", Label: "…and go to", Options: phaseOptions(def, true),
				Help: "Empty means back to the start."},
			// Both ways a conversation leaves on the AGENT's initiative —
			// change_phase mid-turn, and the guard naming somewhere — are
			// bounded by this one list. It lives here, with the other
			// leaving controls, and only on a step the conversation waits
			// in: those are the only steps a turn is ever parked in, so on
			// a step that passes on it would be a control that could never
			// apply.
			ui.FormField{Field: "exits_to", Type: "checklist", Label: "It may move the conversation to", Options: otherPhaseOptions(def, p.Name),
				Help: "Leave every box empty and the conversation can be moved to any step — right for most machines. Tick some and the agent may only move it to those, which is how the two arms of a branch stay separate. " +
					"It bounds what the AGENT decides on its own, by either door: change_phase mid-turn, and the guard above naming somewhere to go. Where this step hands off itself, and the target you set above, are your wiring and always allowed."},
		)
	} else {
		fields = append(fields,
			ui.FormField{Type: "header", Label: "What this step establishes",
				Help: "The things it works out — and, between them, most of the instruction. Each field is sent to the model with the description you write here, so a description is a directive: \"the single best explanation, stated so it could be wrong\" does more work than \"the hypothesis\". " +
					"Each one is validated on its own and arrives in every later step under \"Established earlier in this conversation\", labelled with this step's name. A step that decides something should record WHAT it decided, or the next step has to guess. " +
					"Name a field after a built-in and it is FILLED instead of asked for: the model never sees it, its description goes unused, and it holds text — everything a variable carries is words."},
			ui.FormField{Field: "output", Type: "rows", Label: "", AddLabel: "+ Add field",
				Placeholder: "(nothing yet)",
				Columns: []ui.FormField{
					// WHAT KIND of thing this is, chosen before anything is
					// typed. A combo box asked both questions at once —
					// click it and you are typing, with the built-ins
					// hidden behind a dropdown arrow nobody looks for — so
					// the two decisions are now in order: pick a built-in
					// and the framework fills it, or pick Variable and name
					// it yourself.
					//
					// Deliberately NOT locked once answered. It settled the
					// moment it was picked, which turned a mis-click into
					// a remove-and-re-add; the row reshapes on every change
					// anyway, so letting somebody correct themselves costs
					// nothing and is what everything else on this page
					// already allows.
					{Field: "builtin", Type: "select", Label: "What is this?", Width: 4,
						Options: fieldKindOptions(),
						Help:    "A built-in is filled from what the framework already holds. A variable is something this step works out."},
					// The placeholder shows the SHAPE, not an example noun.
					// A bare "hypothesis" sitting in a box labelled Field
					// reads as neither a value nor an instruction — it
					// just asks the reader why hypothesis.
					{Field: "name", Type: "text", Label: "Name", Width: 3, HideWhen: builtinOrUnansweredExpr(),
						Placeholder: "short_lowercase_name",
						Help:        "What to call this value. Later steps read it as {state:<step>.<name>}, and it is the label it appears under in what they are handed."},
					// The columns below are the STEP's work to configure. A
					// field filled from a built-in has none of it: its type
					// is text, it is always present, and there is nothing
					// to instruct. Leaving them on screen would be three
					// controls that quietly do nothing.
					{Field: "type", Type: "select", Label: "Type", Width: 2, HideWhen: builtinOrUnansweredExpr(), Options: []ui.SelectOption{
						{Value: "string", Label: "text"},
						{Value: "list", Label: "list"},
						{Value: "number", Label: "number"},
						{Value: "bool", Label: "yes/no"},
						{Value: "object", Label: "object"},
					}},
					{Field: "required", Type: "toggle", Label: "Required", HideWhen: builtinOrUnansweredExpr()},
					// The instruction, on its own line. It is where the work
					// of the step is actually specified, and sharing a line
					// with the short cells meant either it was too narrow
					// to write in or they were too narrow to read.
					{Field: "desc", Type: "textarea", Rows: 3, OwnLine: true, HideWhen: builtinOrUnansweredExpr(),
						Label:       "What to work out — write it as the instruction for this field",
						Placeholder: "e.g. the single best explanation, stated so it could be wrong. Not three ranked possibilities: one, committed to."},
				}},

			ui.FormField{Type: "header", Label: "What it adds to the running lists", Collapsed: true,
				Help: "Most steps decide something and hand it on. Some CONTRIBUTE to a list the whole run is building — " +
					"the answers so far, the sources, the questions still open. A list lives under its own name, so several steps can add to one, " +
					"and it survives coming back here (\"keep only\" prunes step findings, never the lists). Read one in a prompt with {state:LIST}."},
			ui.FormField{Field: "accumulates", Type: "rows", Label: "", AddLabel: "+ Add to a list",
				Placeholder: "(this step adds to nothing)",
				Columns: []ui.FormField{
					{Field: "name", Type: "text", Label: "List", Width: 3,
						Help: "The list's own name, e.g. answers. Not the same as any step name — they share the blackboard."},
					{Field: "from", Type: "select", Label: "Takes", Width: 3, Options: ownFieldOptions(p),
						Help: "One of THIS step's own output fields. A list field adds its elements; a single value adds itself."},
					{Field: "mode", Type: "select", Label: "How", Width: 3, Options: []ui.SelectOption{
						{Value: "", Label: "Append — add to the end"},
						{Value: "union", Label: "Union — skip what is already there"},
						{Value: "replace", Label: "Replace — this becomes the list"},
					}},
					{Field: "by", Type: "text", Label: "Same when", Width: 3,
						Placeholder: "e.g. id",
						Help: "Union only: the field that decides two entries are the same one. Leave empty to compare whole values."},
				}},
			ui.FormField{Type: "header", Label: "Where it goes next"},
			ui.FormField{Field: "next", Type: "select", Label: "Then go to", Options: phaseOptions(def, true),
				Help: "Where this step hands off when it does not choose."},
			// Picking the destinations IS the whole routing decision. The
			// field that carries the choice, its type, and the list of
			// legal values are all derivable from this, so the framework
			// derives them (core: MachinePhase.Choices → next_step).
			// Hidden while the step routes by hand. The two mechanisms are
			// mutually exclusive — the field wins, and Problems() says so
			// — but saying it AFTER somebody has ticked five boxes is the
			// worst way to enforce it. Live, because show_when reads the
			// form rather than the stored record: pick a field to route
			// on and this goes away under your hand.
			ui.FormField{Field: "choices", Type: "checklist", Label: "…or let this step choose between", ShowWhen: routingShowWhen(p, "!next_from"),
				Options: otherPhaseOptions(def, p.Name),
				Help: "Tick the steps it may send the conversation to and it decides at run time. You do not declare a field for the decision: the framework adds next_step to what this step returns, writes the instruction naming each destination and what that step is for, draws those arrows, and refuses to save a name that is not a step. \"Then go to\" is the fallback if the choice does not resolve."},
		)
		// The hand-wired form, kept because machines that use it exist and
		// because a routing value that is ALSO a real finding is worth
		// naming yourself. Collapsed, and shown open only when it is in
		// use, so it stops being the first thing anybody meets.
		if strings.TrimSpace(p.NextFrom) != "" || len(ownFieldOptions(p)) > 1 {
			// And the mirror image: while the step has choices ticked,
			// the hand-wired controls are the ones that would be ignored.
			// "!choices" is live too — an empty checklist counts as
			// unanswered (core/ui: hasValue), which is what makes the two
			// halves of this exclusivity symmetrical instead of one
			// server-side and one not.
			fields = append(fields,
				ui.FormField{Type: "header", Label: "Routing by hand", ShowWhen: routingShowWhen(p, "!choices"),
					Collapsed: strings.TrimSpace(p.NextFrom) == "",
					Help:      "Only if the destination is also a finding worth naming — \"severity\", say, where the value routes AND means something. Otherwise use the list above."},
				ui.FormField{Field: "next_from", Type: "select", Label: "Route on the field", ShowWhen: routingShowWhen(p, "!choices"),
					Options: ownFieldOptions(p),
					Help:    "One of this step's own text fields, whose value is a step NAME. A step can route ONE way: picking a field here hides the list above, and ticking that list hides this."},
				ui.FormField{Field: "targets", Type: "checklist", Label: "…which may name", ShowWhen: routingShowWhen(p, "!choices"),
					Options: otherPhaseOptions(def, p.Name),
					Help:    "The steps that field is allowed to name. Leave empty and anything the step returns is tried, with \"Then go to\" as the fallback."},
			)
		}
	}
	fields = append(fields,
		// State, not runtime. What survives coming back here is about what
		// this step SEES; it sat with the model select only because both
		// were leftovers, and it made "How this step runs" mean two things.
		ui.FormField{Type: "header", Label: "Coming back to this step"},
		ui.FormField{Field: "keep", Type: "checklist", Label: "On re-entry, keep only", Options: otherPhaseOptions(def, p.Name),
			Help: "Steps whose findings survive coming BACK here a second time. Choose none to keep everything, which is the safe default — a re-route that silently wipes what earlier steps established is the expensive mistake."},
		// NOT collapsed. Which model answers here is a routing decision an
		// author makes deliberately and re-reads constantly; behind a shut
		// accordion it reads as a setting nobody has touched. The one thing
		// that WAS worth hiding — the tool limit — has left this panel
		// entirely (see phaseToolFields).
		ui.FormField{Type: "header", Label: "How this step runs"},
		// Hidden the moment the step is delegated: a delegate runs on its
		// OWN model, reasoning and tools, so these would be controls
		// somebody can change and be ignored for. The same rule the
		// built-in field rows follow.
		ui.FormField{Field: "model", Type: "select", Label: "Which model", ShowWhen: delegatedShowWhen(p.Model), Options: []ui.SelectOption{
			{Value: "", Label: "Inherit the agent's routing"},
			{Value: "worker", Label: "Worker — the cheap, local one"},
			{Value: "lead", Label: "Lead — the precise, remote one"},
		},
			Help: "A routing decision or a transform is worker work; a step that commits to an explanation is usually lead."},
		ui.FormField{Field: "think", Type: "select", Label: "Reasoning", ShowWhen: delegatedShowWhen(p.Think), Options: []ui.SelectOption{
			{Value: "", Label: "Inherit the agent's setting"},
			{Value: "on", Label: "On — this step is a judgement"},
			{Value: "off", Label: "Off — this step is a transform"},
		}},
	)
	return fields
}

// phaseToolFields is the tool limit, alone, for its own panel.
//
// Alone because a FormPanel with no SubmitLabel auto-saves by POSTing the
// WHOLE record on every field change (see save() in the form runtime:
// "PATCH endpoints take just the changed field; POST gets full record").
// While this control shared a panel with the model select, changing a step
// to lead posted the tool list along with it — so a save whose intent was
// "run this step on the precise model" was also the save that wrote what
// the step may reach. Nothing decided that; the two just travelled in the
// same request. A separate panel is a separate request, and applyPhaseEdit
// touches only the keys a body carries, so neither can write the other now.
//
// Collapsed, because it is the rare setting and an unopened accordion is
// the right resting state for one — but the header carries the count, so a
// step that IS limited says so without being opened. A restriction you
// cannot see is the whole failure this came out of.
func phaseToolFields(p MachinePhase, cat editorCatalog) []ui.FormField {
	label := toolsLabel(p)
	switch {
	case PhaseReach(p) == ReachNone:
		label += " — nothing"
	case PhaseReach(p) == ReachRead:
		label += " — read-only"
	case len(p.Tools) > 0:
		label += " — " + strconv.Itoa(len(p.Tools)) + " named"
	}
	return []ui.FormField{
		ui.FormField{Type: "header", Label: label, Collapsed: true},
		// The primary control, and the only part of this panel that stays
		// true when the machine is run by a different agent or carried to
		// another deployment. A capability travels; a tool name does not.
		ui.FormField{Field: "reach", Type: "select", Label: "Tools this step may reach",
			Options: []ui.SelectOption{
				{Value: ReachAll, Label: "Everything the agent has",
					Help: "The default. What the agent carries is what this step can use."},
				{Value: ReachRead, Label: "Read-only — nothing that writes or reaches the network",
					Help: "For a step that gathers and reports. Searching, listing and reading stay; posting, running and fetching go."},
				{Value: ReachNone, Label: "Nothing — this step only decides",
					Help: "For a step that routes, classifies, or reshapes what it was handed. Also spares it building a catalog it will not use."},
			},
			Help: "Says what KIND of thing this step may do. Prefer it to naming tools: a catalog is assembled fresh every turn — an MCP server publishes its tools when it connects, a credential mints its own per session, an attachment mints more per agent — so a name can stop resolving without anybody changing this machine."},
		// The precise instrument, below the coarse one and shown only
		// where it can act: with the reach set to nothing there is
		// nothing left to narrow, and a control that cannot do anything
		// is a control somebody will spend time on anyway.
		ui.FormField{Type: "header", Label: "Narrow by name (advanced)",
			ShowWhen: "reach:!none"},
		ui.FormField{Field: "tools", Type: "checklist", Label: toolsLabel(p),
			ShowWhen:    "reach:!none",
			Options:     toolChecklistOptions(cat.tools, p.Tools),
			Placeholder: "(no tools to offer)",
			Help:        toolsHelp(p)},
	}
}

// phaseShowsTools decides whether the tool panel is rendered at all.
//
// This was a ShowWhen ("!agent") while the control lived beside the
// delegate field. A ShowWhen reads the values of ITS OWN form, and the
// delegate is now in the other panel, where this one cannot see it — so the
// decision moves to the server, which knows the same thing. Costs liveness:
// setting a delegate hides this on the next render rather than instantly.
//
// Hidden under a delegate because a delegate works from its own catalog and
// the list would be inert — EXCEPT when the step is already carrying one.
// Then hiding it is how a finding becomes unactionable: the checklist says
// "it names tools AND delegates, keep one" and one of the two ways to keep
// one is behind a control nothing can open.
func phaseShowsTools(p MachinePhase) bool {
	return len(p.Tools) > 0 || strings.TrimSpace(p.Agent) == ""
}

// phaseFormFields is the ADD form — a phase that does not exist yet, so
// its choices cannot be computed from it. Deliberately short: name it,
// say what it does, wire it afterwards where every option is real.
func phaseFormFields(def MachineDef) []ui.FormField {
	return []ui.FormField{
		{Field: "name", Type: "text", Label: "Name", Placeholder: "short_lowercase_name",
			Help: "Short handle, lowercase — triage, hunch, verify. It is how other steps point here."},
		{Field: "desc", Type: "text", Label: "What this step does",
			Placeholder: "e.g. Work out whether there is something to explain",
			Help: "One line, for whoever reads the machine later. Not shown to the agent."},
		{Field: "prompt", Type: "textarea", Rows: 8, Label: "Instructions for this step",
			Help: "What the agent should be doing here. Write the JOB, not the identity, and write it to a person. Do not ask for JSON: declare fields in the step's own section afterwards and the framework encodes them."},
		{Field: "resident", Type: "toggle", Label: "The conversation waits here",
			Help: "ON: a turn ENDS here and the person replies into it. OFF: it runs and hands straight on within the same turn."},
	}
}
// url_ is a tiny path-escaper for ids that are already slugs; kept
// separate so the intent reads at the call site.
func url_(s string) string { return strings.TrimSpace(s) }

// handleMachineEditor serves the spec the modal mounts.
func (T *OrchestrateApp) handleMachineEditor(w http.ResponseWriter, r *http.Request, udb Database, user string, def MachineDef) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, machineEditorSpec(def, editorCatalog{
		agents: agentOptions(udb, user), tools: phaseToolOptions(user),
		pipelines: pipelineOptions(udb, user), machines: childMachineOptions(udb, user, def),
		checklist: machineChecklist(udb, user, def),
	}))
}

// handleMachineMeta reads and merges the machine-level fields.
//
// A MERGE, not a replace: this form holds three fields and the record
// has phases. A form panel that posts what it holds would otherwise take
// the phases with it — which is the exact failure patchAgent exists to
// prevent on the agent record.
func (T *OrchestrateApp) handleMachineMeta(w http.ResponseWriter, r *http.Request, udb Database, user string, def MachineDef) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{
			"name": def.Name, "description": def.Description,
			"start": def.StartPhase(), "unattended": def.Unattended,
		})
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if v, ok := body["name"]; ok {
			def.Name = strings.TrimSpace(fmt.Sprint(v))
		}
		if v, ok := body["description"]; ok {
			def.Description = strings.TrimSpace(fmt.Sprint(v))
		}
		if v, ok := body["start"]; ok {
			def.Start = strings.TrimSpace(fmt.Sprint(v))
		}
		if _, ok := body["unattended"]; ok {
			def.Unattended = BoolArg(body, "unattended")
		}
		// Saved even when it does not validate. This is an editor: a
		// machine half-built is the normal state while somebody is
		// building it, and refusing to store the third field until the
		// tenth exists is how an editor becomes a puzzle. The checklist
		// says what is still wrong, and enterMachine already degrades to
		// an ordinary turn rather than breaking a conversation.
		saved := SaveMachineDef(udb, def)
		writeJSON(w, map[string]any{"ok": true, "checklist": machineChecklist(udb, user, saved)})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleMachinePhases is the phase CRUD the table and its forms use.
func (T *OrchestrateApp) handleMachinePhases(w http.ResponseWriter, r *http.Request, udb Database, user string, def MachineDef) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))

	switch r.Method {
	case http.MethodGet:
		if name != "" {
			ph, found := def.Phase(name)
			if !found {
				http.NotFound(w, r)
				return
			}
			// ?preview=1 — just the composed block and its framing, for
			// the in-place refresh after a save. Same function the page
			// renders from, so the two cannot drift.
			if r.URL.Query().Get("preview") == "1" {
				block, note := phasePreviewParts(def, ph)
				writeJSON(w, map[string]any{"block": block, "note": note})
				return
			}
			writeJSON(w, phaseRecord(ph))
			return
		}
		rows := make([]map[string]any, 0, len(def.Phases))
		for _, p := range def.Phases {
			rows = append(rows, phaseRow(p))
		}
		writeJSON(w, rows)

	case http.MethodPost, http.MethodPut, http.MethodPatch:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		target := name
		if target == "" {
			target = strings.TrimSpace(fmt.Sprint(body["name"]))
		}
		if target == "" || target == "<nil>" {
			http.Error(w, "give the phase a name", http.StatusBadRequest)
			return
		}
		idx := -1
		for i, p := range def.Phases {
			if p.Name == target {
				idx = i
				break
			}
		}
		// A CREATE names the step in its body and posts to the bare
		// collection. A form still addressing ?name=X after X was renamed
		// or removed is stale, and treating its save as a create would
		// resurrect the old step next to the new one — the kind of ghost
		// nobody notices until the router starts offering both.
		if idx < 0 && name != "" {
			http.Error(w, "no step called "+strconv.Quote(name)+" — it was renamed or removed; reload the editor", http.StatusNotFound)
			return
		}
		// And the mirror image: the ADD form naming a step that already
		// exists must not quietly merge onto it — the rename path refuses
		// this exact collision, and a create that half-overwrites a step
		// someone built is worse than either.
		if idx >= 0 && name == "" {
			http.Error(w, "a step called "+strconv.Quote(target)+" already exists — edit it in its own section, or pick another name", http.StatusBadRequest)
			return
		}
		var ph MachinePhase
		if idx >= 0 {
			ph = def.Phases[idx]
		} else {
			ph = MachinePhase{Name: target}
		}
		applyPhaseEdit(&ph, body)
		if idx >= 0 {
			// A rename. Two names are in play, and both need care: the
			// new one must not collide with another step (references
			// would silently re-point at it), and every reference to the
			// old one is rewritten so a rename is one edit rather than a
			// scavenger hunt through the checklist.
			//
			// The edited copy is placed FIRST and RenameStep runs on the
			// slice after — never the other way round: RenameStep also
			// rewrites the renamed step's OWN references (a guard_to
			// pointing at itself, its old name in its own guard), and a
			// stale copy assigned afterwards would quietly undo exactly
			// those.
			if ph.Name != target {
				if _, taken := def.Phase(ph.Name); taken {
					http.Error(w, "a step called "+strconv.Quote(ph.Name)+" already exists — renaming this one onto it would point its references somewhere else", http.StatusBadRequest)
					return
				}
				def.Phases[idx] = ph
				def.RenameStep(target, ph.Name)
				Log("[orchestrate.machines] user=%q renamed step %q to %q in %q (references rewritten)", user, target, ph.Name, def.Name)
			} else {
				def.Phases[idx] = ph
			}
		} else {
			def.Phases = append(def.Phases, ph)
			// The first phase added to an empty machine is where it
			// starts, because there is nowhere else it could.
			if strings.TrimSpace(def.Start) == "" {
				def.Start = ph.Name
			}
		}
		saved := SaveMachineDef(udb, def)
		Log("[orchestrate.machines] user=%q edited phase %q of %q", user, ph.Name, def.Name)
		// id/name/slug are what the ADD form's redirect substitutes to
		// reopen the editor on the step just created. The per-step
		// panels post here too and ignore them.
		writeJSON(w, map[string]any{
			"ok": true, "checklist": machineChecklist(udb, user, saved),
			"id": saved.ID, "name": ph.Name, "slug": ui.SectionSlug(ph.Name),
		})

	case http.MethodDelete:
		// The references go with it. A name left pointing at a deleted
		// step is not work the author forgot — it is part of the
		// deletion, and reporting it as something to fix blames somebody
		// for doing what they meant.
		rewritten := def.RemoveStep(name)
		saved := SaveMachineDef(udb, def)
		Log("[orchestrate.machines] user=%q removed phase %q from %q (rewrote %v)", user, name, def.Name, rewritten)
		writeJSON(w, map[string]any{"deleted": name, "rewritten": rewritten, "checklist": machineChecklist(udb, user, saved)})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// applyPhaseEdit merges a form's fields onto a phase. Only keys the form
// actually sent are touched, so a partial save from one section cannot
// blank a field another section owns.
func applyPhaseEdit(ph *MachinePhase, body map[string]any) {
	str := func(k string) (string, bool) {
		v, ok := body[k]
		if !ok {
			return "", false
		}
		return strings.TrimSpace(fmt.Sprint(v)), true
	}
	if v, ok := str("name"); ok && v != "" {
		ph.Name = v
	}
	if v, ok := str("desc"); ok {
		ph.Desc = v
	}
	if v, ok := str("prompt"); ok {
		ph.Prompt = v
	}
	if v, ok := str("next"); ok {
		ph.Next = v
	}
	if v, ok := str("next_from"); ok {
		ph.NextFrom = v
	}

	if _, ok := body["choices"]; ok {
		ph.Choices = stringSliceFromArgs(body, "choices")
	}
	if v, ok := str("agent"); ok {
		ph.Agent = v
	}
	if v, ok := str("pipeline"); ok {
		ph.Pipeline = v
	}
	if v, ok := str("machine"); ok {
		ph.Machine = v
	}
	if v, ok := str("guard"); ok {
		ph.Guard = v
	}
	if v, ok := str("guard_to"); ok {
		ph.GuardTo = v
	}
	if v, ok := str("think"); ok {
		ph.Think = normalizePhaseThink(v)
	}
	if v, ok := str("model"); ok {
		ph.Model = v
	}
	if _, ok := body["keep"]; ok {
		ph.Keep = stringSliceFromArgs(body, "keep")
	}
	if _, ok := body["exits_to"]; ok {
		ph.ExitsTo = stringSliceFromArgs(body, "exits_to")
	}
	if _, ok := body["resident"]; ok {
		ph.Resident = BoolArg(body, "resident")
	}
	if v, ok := str("reach"); ok {
		ph.Reach = strings.ToLower(strings.TrimSpace(v))
	}
	if _, ok := body["tools"]; ok {
		ph.Tools = stringSliceFromArgs(body, "tools")
	}
	if v, ok := body["output"]; ok {
		ph.Output = outputsFromAny(v)
	}
	if v, ok := body["accumulates"]; ok {
		ph.Accumulates = accumulatorsFromAny(v)
	}
	// "targets" is not a phase field of its own: it is the allowed-value
	// set of the field next_from points at. Kept flat in the form because
	// that is where the choice is made — a person picking where a step can
	// go should not have to know it is stored on the field.
	//
	// Applied AFTER output, necessarily: the rows the form posts carry no
	// enum (outputsFromAny never reads one), so writing targets onto the
	// OLD rows and then replacing them wiped the routing set on every
	// full-record save.
	if _, ok := body["targets"]; ok {
		setRoutingTargets(ph, stringSliceFromArgs(body, "targets"))
	}
}

// accumulatorsFromAny reads the rows field back into contributions. A row
// with neither a list nor a field is the empty one the editor leaves
// behind, not an authoring mistake, so it is dropped in silence.
func accumulatorsFromAny(v any) []MachineAccumulator {
	rows, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []MachineAccumulator
	for _, r := range rows {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		acc := MachineAccumulator{
			Name: strings.TrimSpace(mapStr(m, "name")),
			From: strings.TrimSpace(mapStr(m, "from")),
			Mode: strings.ToLower(strings.TrimSpace(mapStr(m, "mode"))),
			By:   strings.TrimSpace(mapStr(m, "by")),
		}
		if acc.Name == "" && acc.From == "" {
			continue
		}
		out = append(out, acc)
	}
	return out
}

// accumulatorRows renders a phase's contributions for the rows editor.
func accumulatorRows(p MachinePhase) []map[string]any {
	out := make([]map[string]any, 0, len(p.Accumulates))
	for _, a := range p.Accumulates {
		out = append(out, map[string]any{"name": a.Name, "from": a.From, "mode": a.Mode, "by": a.By})
	}
	return out
}

// outputsFromAny reads the rows field back into declared output fields.
// Rows with no name are dropped rather than stored: the editor adds a
// blank row the moment somebody clicks Add, and an unnamed field is a
// problem the checklist would then report at them for existing.
func outputsFromAny(v any) []PipelineField {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]PipelineField, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(fmt.Sprint(m["name"]))
		if name == "<nil>" {
			name = ""
		}
		// A row that picked a built-in IS that built-in, whatever is in
		// the name box — the box is hidden for those rows, and a stale
		// value left in it from before the choice must not win.
		if kind := strings.TrimSpace(fmt.Sprint(m["builtin"])); kind != "" && kind != "<nil>" && kind != customFieldKind {
			if _, isBuiltin := BuiltinRefForFieldName(kind); isBuiltin {
				name = kind
			}
		}
		if name == "" {
			continue
		}
		f := PipelineField{Name: name}
		if t := strings.TrimSpace(fmt.Sprint(m["type"])); t != "" && t != "<nil>" {
			f.Type = PipelineFieldType(t)
		}
		if d := strings.TrimSpace(fmt.Sprint(m["desc"])); d != "" && d != "<nil>" {
			f.Desc = d
		}
		// A "from" written by hand survives; a field NAMED after a
		// built-in needs nothing here, because that rule lives in core
		// where every door passes through it (MachinePhase.normalized).
		if from := strings.TrimSpace(fmt.Sprint(m["from"])); from != "" && from != "<nil>" {
			f.From = from
		}
		f.Required = BoolArg(m, "required")
		out = append(out, f)
	}
	return out
}

// phaseRecord is one phase as the edit form reads it.
func phaseRecord(p MachinePhase) map[string]any {
	rows := make([]map[string]any, 0, len(p.Output))
	for _, f := range p.Output {
		// The kind column is derived, never stored: a field named after
		// a built-in IS that built-in (core decides this, at every door),
		// so the form is told what the definition already means rather
		// than carrying a second copy that could disagree with it.
		kind, name := customFieldKind, f.Name
		if _, isBuiltin := BuiltinRefForFieldName(f.Name); isBuiltin {
			// The kind carries the identity for these, and the name box
			// is hidden — so leave it EMPTY. Handing back "now" would
			// pre-fill that box the moment somebody switched the row to
			// Variable, and a variable named after a built-in is read as
			// that built-in at every door: the switch would appear to do
			// nothing.
			kind, name = f.Name, ""
		}
		rows = append(rows, map[string]any{
			"name": name, "type": string(f.Type), "required": f.Required,
			"desc": f.Desc, "from": f.From, "builtin": kind,
		})
	}
	return map[string]any{
		"name": p.Name, "desc": p.Desc, "prompt": p.Prompt,
		"resident": p.Resident, "next": p.Next, "next_from": p.NextFrom, "agent": p.Agent,
		"pipeline": p.Pipeline, "machine": p.Machine, "accumulates": accumulatorRows(p),
		"guard": p.Guard, "guard_to": p.GuardTo,
		"think": p.Think, "reach": PhaseReach(p), "tools": p.Tools, "output": rows,
		"model": p.Model, "keep": p.Keep, "targets": routingTargetsOf(p), "exits_to": p.ExitsTo,
		"choices": p.Choices,
	}
}

// phaseRow is one phase as the table shows it — in the terms the form
// asks about, not the field names it stores.
func phaseRow(p MachinePhase) map[string]any {
	kind := "moves on"
	if p.Resident {
		kind = "waits here"
	}
	goes := p.Next
	switch {
	case p.RoutesBy() != "" && len(p.RoutingChoices()) > 0:
		goes = "chooses: " + strings.Join(p.RoutingChoices(), ", ")
		if p.Next != "" {
			goes += " (else " + p.Next + ")"
		}
	case p.NextFrom != "" && p.Next != "":
		goes = "decided by " + p.NextFrom + " (else " + p.Next + ")"
	case p.NextFrom != "":
		goes = "decided by " + p.NextFrom
	case p.Resident && p.Next == "":
		goes = "—"
	case goes == "":
		goes = "nowhere yet"
	}
	var names []string
	for _, f := range p.Output {
		names = append(names, f.Name)
	}
	establishes := strings.Join(names, ", ")
	if establishes == "" {
		establishes = "—"
	}
	return map[string]any{
		"name": p.Name, "kind": kind, "goes": goes, "establishes": establishes,
		"outputs": strconv.Itoa(len(p.Output)),
	}
}
