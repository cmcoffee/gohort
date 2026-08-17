// Describe a CHANGE to a machine that already exists.
//
// The draft endpoint (machine_draft.go) turns a paragraph into a machine,
// and it is one shot: if the draft misses, the only recourse is editing
// twelve fields by hand. That is the wrong recourse for "make triage
// decide between three lanes instead of two" — a sentence somebody can
// say, against a machine already on screen.
//
// It reuses the drafter whole: same spec, same decoder, same repair
// pass. The only difference is what it is asked — the current machine
// goes in as the thing being revised, so the model edits rather than
// invents, and the ID stays put so every agent pointing at this machine
// keeps pointing at it.
//
// Undoable, and that is not a nicety. A revision can rewrite every
// prompt in the machine, and the prompts are the part somebody actually
// wrote; a control that can silently eat an afternoon's work is one
// nobody dares press.

package orchestrate

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// handleMachineRevise redrafts an existing machine from a description of
// what should change.
//
//	POST /api/machines/{id}/revise {"description": "..."} → {id, changed[], checklist}
func (T *OrchestrateApp) handleMachineRevise(w http.ResponseWriter, r *http.Request, udb Database, user string, def MachineDef) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if T.LLM == nil {
		http.Error(w, "worker LLM not configured", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	want := strings.TrimSpace(body.Description)
	if want == "" {
		http.Error(w, "say what should change", http.StatusBadRequest)
		return
	}
	if len(want) > maxDraftDescription {
		want = want[:maxDraftDescription]
	}

	// The machine as it stands, in the same dialect the drafter writes.
	// Its own export shape rather than a description of it: the model is
	// being asked to return this document edited, and handing it prose
	// about the document invites a rewrite from scratch.
	current, err := json.MarshalIndent(ExportMachine(def), "", "  ")
	if err != nil {
		http.Error(w, "could not read the machine", http.StatusInternalServerError)
		return
	}
	ask := "Revise this machine.\n\nWHAT SHOULD CHANGE:\n" + want +
		"\n\nTHE MACHINE AS IT STANDS:\n" + string(current) +
		"\n\nReturn the WHOLE machine with that change made. Keep every step, prompt, and setting the " +
		"change does not touch, byte for byte — an edit that rewrites what nobody asked about is a " +
		"worse answer than one that refuses. Keep the name unless the change is about the name."

	revised, derr := T.draftMachineOnce(r.Context(), ask)
	if derr != nil {
		if revised, derr = T.draftMachineOnce(r.Context(), ask+
			"\n\nYour previous reply could not be used: "+derr.Error()+
			"\nReply with ONLY the JSON object."); derr != nil {
			http.Error(w, "the revision could not be produced: "+derr.Error(), http.StatusBadGateway)
			return
		}
	}
	if probs := revised.Problems(); len(probs) > 0 {
		if fixed, ferr := T.draftMachineOnce(r.Context(), ask+
			"\n\nA previous attempt had these problems — return a corrected machine:\n- "+
			strings.Join(probs, "\n- ")); ferr == nil && len(fixed.Problems()) < len(probs) {
			revised = fixed
		}
	}

	// Identity is the editor's, not the model's. The ID keeps every
	// agent pointing here, and an empty name would land somebody in an
	// editor whose title vanished.
	before := def
	before.Previous = nil // one deep: the snapshot must not carry its own
	revised.ID = def.ID
	revised.Owner = user
	revised.Created = def.Created
	if strings.TrimSpace(revised.Name) == "" {
		revised.Name = def.Name
	}
	revised.Previous = &before

	saved := SaveMachineDef(udb, revised)
	changed := describeMachineChange(before, saved)
	Log("[orchestrate.machines] user=%q revised machine %q (id=%s): %s", user, saved.Name, saved.ID,
		strings.Join(changed, "; "))
	writeJSON(w, map[string]any{
		"id": saved.ID, "name": saved.Name,
		"changed":   changed,
		"checklist": saved.Problems(),
	})
}

// handleMachineUndo puts back the definition a revision replaced.
func (T *OrchestrateApp) handleMachineUndo(w http.ResponseWriter, r *http.Request, udb Database, user string, def MachineDef) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if def.Previous == nil {
		http.Error(w, "there is no revision to undo", http.StatusBadRequest)
		return
	}
	back := *def.Previous
	back.ID = def.ID
	back.Owner = user
	back.Created = def.Created
	// One step, deliberately. Keeping the revision as the new snapshot
	// would make undo a toggle, which is a different promise: somebody
	// pressing it twice expects to be where they started, not back where
	// they undid from.
	back.Previous = nil
	saved := SaveMachineDef(udb, back)
	Log("[orchestrate.machines] user=%q undid the last revision of machine %q (id=%s)", user, saved.Name, saved.ID)
	writeJSON(w, map[string]any{"id": saved.ID, "name": saved.Name, "checklist": saved.Problems()})
}

// describeMachineChange says what a revision actually did, in steps.
//
// The reply has to be checkable against the machine on screen. "Revised"
// is not: a model that ignored the instruction and one that followed it
// produce the same word, and the whole risk of this door is a rewrite
// nobody asked for.
func describeMachineChange(before, after MachineDef) []string {
	was := map[string]MachinePhase{}
	for _, p := range before.Phases {
		was[p.Name] = p
	}
	is := map[string]MachinePhase{}
	for _, p := range after.Phases {
		is[p.Name] = p
	}
	var added, removed, edited []string
	for name, p := range is {
		old, existed := was[name]
		switch {
		case !existed:
			added = append(added, name)
		case old.Prompt != p.Prompt:
			edited = append(edited, name+" (instructions)")
		case !samePhaseWiring(old, p):
			edited = append(edited, name+" (wiring)")
		}
	}
	for name := range was {
		if _, still := is[name]; !still {
			removed = append(removed, name)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(edited)

	var out []string
	if len(added) > 0 {
		out = append(out, "added "+strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		out = append(out, "removed "+strings.Join(removed, ", "))
	}
	if len(edited) > 0 {
		out = append(out, "changed "+strings.Join(edited, ", "))
	}
	if before.Name != after.Name {
		out = append(out, "renamed the machine to "+strconv.Quote(after.Name))
	}
	if before.StartPhase() != after.StartPhase() {
		out = append(out, "it now starts in "+after.StartPhase())
	}
	if len(out) == 0 {
		out = append(out, "nothing — the revision came back identical to what was there")
	}
	return out
}

// samePhaseWiring compares everything about a phase except its prose, so
// "changed X (wiring)" means something a picture would show.
func samePhaseWiring(a, b MachinePhase) bool {
	a.Prompt, b.Prompt = "", ""
	a.Desc, b.Desc = "", ""
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
