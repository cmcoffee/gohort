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
func machineEditorSpec(def MachineDef, agents []ui.SelectOption) map[string]any {
	base := "api/machines/" + url_(def.ID)
	return map[string]any{
		"id":   def.ID,
		"name": def.Name,
		// Validate's own findings, as work remaining rather than as a
		// refusal. Same function behind both, so the list can never
		// disagree with what a save will accept.
		"checklist": def.Problems(),
		// Kept separate from the checklist: advice is a guess about
		// intent and must never read as "this is broken".
		"advice": def.Advice(),
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
		"phases": phasePanels(def, base, agents),
		"add":    addPanel(def, base),
		"components": []any{
			metaPanel(def, base),
			ui.Stack{Children: phasePanels(def, base, agents)},
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
			{Field: "name", Type: "text", Label: "Name"},
			{Field: "description", Type: "textarea", Rows: 2, Label: "When to use this",
				Help: "Shown wherever someone picks a machine. Say what kind of conversation it is for, not how it works."},
			{Field: "global", Type: "toggle", Label: "Offer to every agent",
				Help: "On: this machine appears for all of your agents. Off: only the ones you point at it. A machine is a whole workflow, so this is a bigger grant than it looks — leave it off unless the workflow genuinely suits everything you run."},
			{Field: "start", Type: "select", Label: "Starts at", Options: phaseOptions(def, false),
				Help: "The step a new conversation begins in. Usually the one that decides what kind of turn this is."},
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
			Fields:      phaseFormFields(def),
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

// phasePanels builds one editing panel per phase, each carrying the
// choices computed for THAT phase. Order follows the machine's own,
// which is the order somebody reads them in.
func phasePanels(def MachineDef, base string, agents []ui.SelectOption) []ui.Component {
	out := make([]ui.Component, 0, len(def.Phases))
	for _, p := range def.Phases {
		q := base + "/phases?name=" + url_(p.Name)
		out = append(out, ui.Stack{Children: []ui.Component{
			ui.FormPanel{Source: q, PostURL: q, Fields: phaseFieldsFor(def, p, agents)},
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
			ui.Toolbar{Actions: []ui.ToolbarAction{{
				Label:   "Remove this step",
				Title:   "Delete " + p.Name + " from this machine",
				Method:  "client",
				URL:     "machine_remove_step",
				Data:    p.Name,
				Variant: "danger",
				Confirm: removeStepConfirm(def, p.Name),
			}}},
		}})
	}
	return out
}

// removeStepConfirm names what ELSE breaks, because the answer is
// knowable and the person deciding cannot see it from here.
func removeStepConfirm(def MachineDef, name string) string {
	var refs []string
	for _, p := range def.Phases {
		if p.Name == name {
			continue
		}
		if p.Next == name || p.GuardTo == name {
			refs = append(refs, p.Name)
		}
	}
	msg := "Remove the step \"" + name + "\"?"
	if def.StartPhase() == name {
		msg += " This is where a conversation STARTS, so the machine will not run until another step is chosen as the start."
	}
	if len(refs) > 0 {
		msg += " " + strings.Join(refs, " and ") + " routes here and will be left pointing at nothing."
	}
	return msg + " This cannot be undone."
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

// phaseFieldsFor builds the form for ONE phase, with every choice
// computed for it: the fields it declares, the phases it can reach, the
// state it can read. Nothing here is typed from memory.
func phaseFieldsFor(def MachineDef, p MachinePhase, agents []ui.SelectOption) []ui.FormField {
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
		promptHelp += " This prompt is pinned across turns, so {input} is not available — the person's message is already in the conversation."
	} else {
		promptHelp += " {input} is the person's message."
	}
	if len(refs) > 0 {
		promptHelp += " If you need a value INSIDE a sentence, the references are " + strings.Join(refs, "  ") +
			" — but the same values already arrive pinned, so reach for one only when the phrasing genuinely matters."
	}

	fields := []ui.FormField{
		{Field: "name", Type: "text", Label: "Name", Help: "Short handle, lowercase. It is how other steps point here."},
		{Field: "desc", Type: "text", Label: "What this step does",
			Help: "One line, for whoever reads the machine later. Not shown to the agent."},
		{Field: "prompt", Type: "textarea", Rows: 10, Label: promptLabel, Help: promptHelp},

		{Type: "header", Label: "What kind of step is this?",
			Help: "The one answer everything below depends on. A step the conversation WAITS in replies to the person and cannot record fields — its reply goes to them, not to a decoder. A step that passes through records what it worked out and hands to the next one."},
		{Field: "resident", Type: "toggle", Label: "The conversation waits here",
			Help: "ON: a turn ENDS here and the person replies into it — this is where a conversation lives, and the sections below change to match. OFF: the step runs, records what it establishes, and passes straight on within the same turn. A machine needs at least one step with this on, or a turn has nowhere to finish."},
	}
	if !p.Resident {
		// Who does the work comes before what it produces and where it
		// goes — it is a property of the step itself, and the answer
		// changes how the instructions above should read.
		fields = append(fields,
			ui.FormField{Field: "agent", Type: "select", Label: "Who runs this step", Options: agents,
				Help: "Leave it with this agent, or give it to another one — with its own persona, tools and memory. A delegate gets the instructions above, works, and reports back; what it reports is recorded below. Use it when the work needs different REACH, not different wording."},
		)
	}
	if p.Resident {
		fields = append(fields,
			ui.FormField{Field: "next", Type: "select", Label: "After one turn, go to", Options: phaseOptions(def, true),
				Help: "Leave empty for the usual case — the conversation stays here. Set it to make this a ONE-turn step: it replies once, then moves on. That is how an intake beat asks its questions and continues."},
			ui.FormField{Type: "header", Label: "Leaving early"},
			ui.FormField{Field: "guard", Type: "textarea", Rows: 2, Label: "Leave this step when…",
				Placeholder: "the person has moved to a different problem",
				Help: "Plain words, judged on each new turn that arrives here. Empty means the conversation stays until something else moves it. Costs one model call per turn, so say something worth checking."},
			ui.FormField{Field: "guard_to", Type: "select", Label: "…and go to", Options: phaseOptions(def, true),
				Help: "Empty means back to the start."},
		)
	} else {
		fields = append(fields,
			ui.FormField{Type: "header", Label: "What this step establishes",
				Help: "The things it works out — and, between them, most of the instruction. Each field is sent to the model with the description you write here, so a description is a directive: \"the single best explanation, stated so it could be wrong\" does more work than \"the hypothesis\". " +
					"Each one is validated on its own and arrives in every later step under \"Established earlier in this conversation\", labelled with this step's name. A step that decides something should record WHAT it decided, or the next step has to guess."},
			ui.FormField{Field: "output", Type: "rows", Label: "", AddLabel: "+ Add field",
				Placeholder: "(nothing yet)",
				Columns: []ui.FormField{
					{Field: "name", Type: "text", Label: "Field", Width: 2},
					{Field: "type", Type: "select", Label: "Type", Options: []ui.SelectOption{
						{Value: "string", Label: "text"},
						{Value: "list", Label: "list"},
						{Value: "number", Label: "number"},
						{Value: "bool", Label: "yes/no"},
						{Value: "object", Label: "object"},
					}},
					{Field: "required", Type: "toggle", Label: "Required"},
					// The instruction, and the widest thing on the row. A
					// single-line cell taught people to write three words;
					// this is where the work of the step is actually
					// specified, so it has to look like somewhere you
					// write.
					{Field: "desc", Type: "textarea", Rows: 3, Width: 6,
						Label:       "What to work out — write it as the instruction for this field",
						Placeholder: "the single best explanation, stated so it could be wrong. Not three ranked possibilities: one, committed to."},
				}},

			ui.FormField{Type: "header", Label: "Where it goes next"},
			ui.FormField{Field: "next", Type: "select", Label: "Then go to", Options: phaseOptions(def, true),
				Help: "Where this step hands off."},
			ui.FormField{Field: "next_from", Type: "select", Label: "…unless this step decides", Options: ownFieldOptions(p),
				Help: "Let the step choose where to go by writing a step NAME into one of the text fields it establishes. Only this step's own text fields are offered, because only those can carry it. \"Then go to\" becomes the fallback."},
			ui.FormField{Field: "targets", Type: "checklist", Label: "…and it may choose", Options: otherPhaseOptions(def, p.Name),
				Help: "The steps that field is allowed to name. Declaring them writes the instruction for you (each step's own description becomes its explanation), lets the diagram draw those arrows instead of every possible one, and makes a name that does not exist a save-time error rather than a silent fallback. Leave empty and anything the step returns is tried, with \"Then go to\" as the fallback."},
		)
	}
	fields = append(fields,
		ui.FormField{Type: "header", Label: "How this step runs", Collapsed: true},
		ui.FormField{Field: "model", Type: "select", Label: "Which model", Options: []ui.SelectOption{
			{Value: "", Label: "Inherit the agent's routing"},
			{Value: "worker", Label: "Worker — the cheap, local one"},
			{Value: "lead", Label: "Lead — the precise, remote one"},
		},
			Help: "A routing decision or a transform is worker work; a step that commits to an explanation is usually lead."},
		ui.FormField{Field: "think", Type: "select", Label: "Reasoning", Options: []ui.SelectOption{
			{Value: "", Label: "Inherit the agent's setting"},
			{Value: "on", Label: "On — this step is a judgement"},
			{Value: "off", Label: "Off — this step is a transform"},
		}},
		ui.FormField{Field: "keep", Type: "checklist", Label: "On re-entry, keep only", Options: otherPhaseOptions(def, p.Name),
			Help: "Steps whose findings survive coming BACK here a second time. Choose none to keep everything, which is the safe default — a re-route that silently wipes what earlier steps established is the expensive mistake."},
		ui.FormField{Field: "tools", Type: "tags", Label: "Only these tools",
			Help: "Narrow the agent's catalog while it is in this step. Empty = everything it normally has. Note this changes the tool list mid-conversation, which re-writes the cached prompt prefix."},
	)
	return fields
}

// phaseFormFields is the ADD form — a phase that does not exist yet, so
// its choices cannot be computed from it. Deliberately short: name it,
// say what it does, wire it afterwards where every option is real.
func phaseFormFields(def MachineDef) []ui.FormField {
	return []ui.FormField{
		{Field: "name", Type: "text", Label: "Name", Placeholder: "triage",
			Help: "Short handle, lowercase. It is how other steps point here."},
		{Field: "desc", Type: "text", Label: "What this step does",
			Placeholder: "Work out whether there is something to explain",
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
	writeJSON(w, machineEditorSpec(def, agentOptions(udb, user)))
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
			"start": def.StartPhase(), "global": def.Global,
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
		if _, ok := body["global"]; ok {
			def.Global = BoolArg(body, "global")
		}
		// Saved even when it does not validate. This is an editor: a
		// machine half-built is the normal state while somebody is
		// building it, and refusing to store the third field until the
		// tenth exists is how an editor becomes a puzzle. The checklist
		// says what is still wrong, and enterMachine already degrades to
		// an ordinary turn rather than breaking a conversation.
		saved := SaveMachineDef(udb, def)
		writeJSON(w, map[string]any{"ok": true, "checklist": saved.Problems()})
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
		var ph MachinePhase
		if idx >= 0 {
			ph = def.Phases[idx]
		} else {
			ph = MachinePhase{Name: target}
		}
		applyPhaseEdit(&ph, body)
		if idx >= 0 {
			def.Phases[idx] = ph
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
		writeJSON(w, map[string]any{"ok": true, "checklist": saved.Problems()})

	case http.MethodDelete:
		out := def.Phases[:0:0]
		for _, p := range def.Phases {
			if p.Name != name {
				out = append(out, p)
			}
		}
		def.Phases = out
		saved := SaveMachineDef(udb, def)
		Log("[orchestrate.machines] user=%q removed phase %q from %q", user, name, def.Name)
		writeJSON(w, map[string]any{"deleted": name, "checklist": saved.Problems()})

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
	// "targets" is not a phase field of its own: it is the allowed-value
	// set of the field next_from points at. Kept flat in the form because
	// that is where the choice is made — a person picking where a step can
	// go should not have to know it is stored on the field.
	if _, ok := body["targets"]; ok {
		setRoutingTargets(ph, stringSliceFromArgs(body, "targets"))
	}
	if v, ok := str("agent"); ok {
		ph.Agent = v
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
	if _, ok := body["resident"]; ok {
		ph.Resident = BoolArg(body, "resident")
	}
	if _, ok := body["tools"]; ok {
		ph.Tools = stringSliceFromArgs(body, "tools")
	}
	if v, ok := body["output"]; ok {
		ph.Output = outputsFromAny(v)
	}
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
		if name == "" || name == "<nil>" {
			continue
		}
		f := PipelineField{Name: name}
		if t := strings.TrimSpace(fmt.Sprint(m["type"])); t != "" && t != "<nil>" {
			f.Type = PipelineFieldType(t)
		}
		if d := strings.TrimSpace(fmt.Sprint(m["desc"])); d != "" && d != "<nil>" {
			f.Desc = d
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
		rows = append(rows, map[string]any{
			"name": f.Name, "type": string(f.Type), "required": f.Required, "desc": f.Desc,
		})
	}
	return map[string]any{
		"name": p.Name, "desc": p.Desc, "prompt": p.Prompt,
		"resident": p.Resident, "next": p.Next, "next_from": p.NextFrom, "agent": p.Agent,
		"guard": p.Guard, "guard_to": p.GuardTo,
		"think": p.Think, "tools": p.Tools, "output": rows,
		"model": p.Model, "keep": p.Keep, "targets": routingTargetsOf(p),
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
