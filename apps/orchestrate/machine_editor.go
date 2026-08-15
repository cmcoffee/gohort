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
func machineEditorSpec(def MachineDef) map[string]any {
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
		"components": []any{
			ui.FormPanel{
				Source:  base + "/meta",
				PostURL: base + "/meta",
				Fields: []ui.FormField{
					{Field: "name", Type: "text", Label: "Name"},
					{Field: "description", Type: "textarea", Rows: 2, Label: "When to use this",
						Help: "Shown wherever someone picks a machine. Say what kind of conversation it is for, not how it works."},
					{Field: "global", Type: "toggle", Label: "Offer to every agent",
						Help: "On: this machine appears for all of your agents. Off: only the ones you point at it. A machine is a whole workflow, so this is a bigger grant than it looks — leave it off unless the workflow genuinely suits everything you run."},
					{Field: "start", Type: "select", Label: "Starts at", Options: phaseOptions(def, false),
						Help: "The phase a new conversation begins in. Usually the one that decides what kind of turn this is."},
				},
			},
			ui.Table{
				Source: base + "/phases",
				RowKey: "name",
				Columns: []ui.Col{
					{Field: "name", Flex: 1},
					{Field: "kind", Label: "Ends the turn?", Mute: true},
					{Field: "goes", Label: "Then", Mute: true, Flex: 2},
					{Field: "declares", Label: "Hands on", Mute: true, Flex: 2},
				},
				RowActions: []ui.RowAction{
					ui.Expand("Edit", ui.FormPanel{
						Source:  base + "/phases?name={name}",
						PostURL: base + "/phases?name={name}",
						Fields:  phaseFormFields(def),
					}),
					{Type: "button", Label: "Delete", Method: "DELETE",
						PostTo:     base + "/phases?name={name}",
						Variant:    "danger",
						Confirm:    "Remove this phase? Anything routing to it will be reported as a problem until you point it somewhere else.",
						Optimistic: true},
				},
				EmptyText: "No phases yet. A phase is one step of the conversation: what the agent is doing, and whether the turn ends there.",
			},
			ui.ModalButton{
				Label:    "Add phase",
				Title:    "Add a phase",
				Subtitle: "One step of the conversation. Give it a name and say what it does; you can wire where it goes afterwards.",
				Width:    "640px",
				Body: ui.FormPanel{
					PostURL:     base + "/phases",
					SubmitLabel: "Add phase",
					Fields:      phaseFormFields(def),
				},
			},
		},
	}
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

// outputFieldOptions lists every output field declared ANYWHERE in the
// machine, for the next_from select.
//
// Every phase's, not just this one's, because the form is built once for
// a table of rows and a select cannot re-render per row. The help says
// the constraint the list cannot: it must be a field THIS phase
// declares. Validate catches the mismatch either way, and now says so
// while you are still looking at the form.
func outputFieldOptions(def MachineDef) []ui.SelectOption {
	out := []ui.SelectOption{{Value: "", Label: "— always the same phase —"}}
	seen := map[string]bool{}
	for _, p := range def.Phases {
		for _, f := range p.Output {
			if f.Name == "" || seen[f.Name] {
				continue
			}
			seen[f.Name] = true
			out = append(out, ui.SelectOption{Value: f.Name, Label: f.Name})
		}
	}
	return out
}

// phaseFormFields is the per-phase form. Every label is a question or a
// plain statement of consequence; the field names live in the JSON
// editor, where someone has already opted into the vocabulary.
func phaseFormFields(def MachineDef) []ui.FormField {
	return []ui.FormField{
		{Field: "name", Type: "text", Label: "Name", Placeholder: "triage",
			Help: "Short handle, lowercase. It is how other phases point here."},
		{Field: "desc", Type: "text", Label: "What this step does",
			Placeholder: "Work out whether there is something to explain",
			Help: "One line, for whoever reads the machine later. Not shown to the agent."},
		{Field: "prompt", Type: "textarea", Rows: 12, Label: "Instructions for this step",
			Help: "What the agent should be doing HERE, layered on top of its own persona. Write the JOB, not the identity, and write it to a person — \"work out whether there is something to explain, and say where it came from\" beats a specification. {input} is the person's message; {state:phase.field} is something an earlier phase handed on. " +
				"Do NOT ask for JSON or describe an output format: declare the fields below and the framework encodes and validates them for you. Format instructions here fight the ones it already has."},

		{Type: "header", Label: "Where the turn ends"},
		{Field: "resident", Type: "toggle", Label: "The conversation waits here",
			Help: "ON: a turn ENDS in this phase and the person replies into it — this is where a conversation lives. OFF: this step runs and hands straight on within the same turn, so the person never waits on it alone. A machine needs at least one phase with this on, or a turn has nowhere to finish."},
		{Field: "next", Type: "select", Label: "Then go to", Options: phaseOptions(def, true),
			Help: "Where a hand-off lands. Ignored when the conversation waits here."},
		{Field: "next_from", Type: "select", Label: "…unless this step decides", Options: outputFieldOptions(def),
			Help: "Let the step choose its own next phase by writing a phase NAME into one of the fields it hands on (below). Pick that field here. It must be a field THIS phase declares — anything else is reported as a problem. \"Then go to\" becomes the fallback when the value does not name a real phase."},

		{Field: "agent", Type: "text", Label: "Or hand this step to another agent",
			Placeholder: "Log analyst",
			Help: "Name or id of an agent that should do this step instead — one with its own persona, tools and memory. It gets the instructions above, works, and reports back; what it reports is then recorded in the fields below. Use it when the work needs different REACH rather than different wording. Only on steps that hand on: a step the conversation waits in cannot be delegated, because the person would be talking to something they did not open."},

		{Type: "header", Label: "What it hands on", Collapsed: true,
			Help: "Fields this step writes for later phases to read as {state:<phase>.<field>}. A step that decides something should hand on WHAT it decided, or the next step has to guess."},
		{Field: "output", Type: "rows", Label: "", AddLabel: "+ Add field",
			Placeholder: "(nothing handed on)",
			Columns: []ui.FormField{
				{Field: "name", Type: "text", Label: "Field", Width: 2, Placeholder: "hypothesis"},
				{Field: "type", Type: "select", Label: "Type", Options: []ui.SelectOption{
					{Value: "string", Label: "text"},
					{Value: "list", Label: "list"},
					{Value: "number", Label: "number"},
					{Value: "bool", Label: "yes/no"},
					{Value: "object", Label: "object"},
				}},
				{Field: "required", Type: "toggle", Label: "Required"},
				{Field: "desc", Type: "text", Label: "What goes in it", Width: 3},
			}},

		{Type: "header", Label: "Leaving early", Collapsed: true,
			Help: "A guard is checked when a turn arrives at a phase the conversation is already sitting in. It exists so a machine cannot trap someone in a step that stopped fitting."},
		{Field: "guard", Type: "textarea", Rows: 2, Label: "Leave this phase when…",
			Placeholder: "the person has moved to a different problem",
			Help: "Described in plain words, judged per turn. Leave empty and the conversation stays here until something else moves it."},
		{Field: "guard_to", Type: "select", Label: "…and go to", Options: phaseOptions(def, true),
			Help: "Where the guard sends them. Empty means back to the machine's start."},

		{Type: "header", Label: "How this step runs", Collapsed: true},
		{Field: "think", Type: "select", Label: "Reasoning", Options: []ui.SelectOption{
			{Value: "", Label: "Inherit the agent's setting"},
			{Value: "on", Label: "On — this step is a judgement"},
			{Value: "off", Label: "Off — this step is a transform"},
		},
			Help: "Turn it on where the step commits to something that could be wrong. Off for steps that reshape what they were given."},
		{Field: "model", Type: "select", Label: "Which model", Options: []ui.SelectOption{
			{Value: "", Label: "Inherit the agent's routing"},
			{Value: "worker", Label: "Worker — the cheap, local one"},
			{Value: "lead", Label: "Lead — the precise, remote one"},
		},
			Help: "Pin the tier for this step. A routing decision or a transform is worker work; a step that commits to an explanation is usually lead. Left inherited, the agent's own routing decides."},
		{Field: "keep", Type: "tags", Label: "On re-entry, keep only",
			Help: "Phase names whose findings survive RE-ENTERING this step (arriving at it a second time in one conversation). Empty keeps everything, which is the safe default — a re-route that silently wipes what earlier steps established is the expensive mistake. Name phases here only when coming back should mean starting that part over."},
		{Field: "tools", Type: "tags", Label: "Only these tools",
			Help: "Narrow the agent's catalog while it is in this phase. Empty = everything it normally has. Narrowing is how a step is kept from doing the next step's job — but note it changes the tool list mid-conversation, which re-writes the cached prompt prefix."},
	}
}

// url_ is a tiny path-escaper for ids that are already slugs; kept
// separate so the intent reads at the call site.
func url_(s string) string { return strings.TrimSpace(s) }

// handleMachineEditor serves the spec the modal mounts.
func (T *OrchestrateApp) handleMachineEditor(w http.ResponseWriter, r *http.Request, def MachineDef) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, machineEditorSpec(def))
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
		"model": p.Model, "keep": p.Keep,
	}
}

// phaseRow is one phase as the table shows it — in the terms the form
// asks about, not the field names it stores.
func phaseRow(p MachinePhase) map[string]any {
	kind := "hands on"
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
	declares := strings.Join(names, ", ")
	if declares == "" {
		declares = "—"
	}
	return map[string]any{
		"name": p.Name, "kind": kind, "goes": goes, "declares": declares,
		"outputs": strconv.Itoa(len(p.Output)),
	}
}
