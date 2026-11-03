// Describe a CHANGE to a machine that already exists, through the page's
// ✨ Get assistance (definition_assist.go): the conversation settles what
// should change, propose drafts it for review, revise applies it.
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
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/assist"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// draftMachineRevision redrafts a machine with a change made, through the
// drafter and its repair pass. Nothing is saved; the result carries only
// what the drafter writes (name, description, start, steps).
func (T *OrchestrateApp) draftMachineRevision(ctx context.Context, def MachineDef, want string) (MachineDef, error) {
	// The machine as it stands, in the same dialect the drafter writes.
	// Its own export shape rather than a description of it: the model is
	// being asked to return this document edited, and handing it prose
	// about the document invites a rewrite from scratch.
	current, err := json.MarshalIndent(ExportMachine(def), "", "  ")
	if err != nil {
		return MachineDef{}, jsonapi.Status(http.StatusInternalServerError, "could not read the machine")
	}
	ask := "Revise this machine.\n\nWHAT SHOULD CHANGE:\n" + want +
		"\n\nTHE MACHINE AS IT STANDS:\n" + string(current) +
		"\n\nReturn the WHOLE machine with that change made. Keep every step, prompt, and setting the " +
		"change does not touch, byte for byte: an edit that rewrites what nobody asked about is a " +
		"worse answer than one that refuses. Keep the name unless the change is about the name."

	revised, derr := T.draftMachineOnce(ctx, ask)
	if derr != nil {
		if revised, derr = T.draftMachineOnce(ctx, ask+
			"\n\nYour previous reply could not be used: "+derr.Error()+
			"\nReply with ONLY the JSON object."); derr != nil {
			return MachineDef{}, jsonapi.Status(http.StatusBadGateway, "the revision could not be produced: "+derr.Error())
		}
	}
	if probs := revised.Problems(); len(probs) > 0 {
		if fixed, ferr := T.draftMachineOnce(ctx, ask+
			"\n\nA previous attempt had these problems, return a corrected machine:\n- "+
			strings.Join(probs, "\n- ")); ferr == nil && len(fixed.Problems()) < len(probs) {
			revised = fixed
		}
	}
	return revised, nil
}

// withMachineRevision is def with a revision's parts in place of its own:
// name, description, start and steps. Everything else (who it is shared
// with, its flags, its deny list, its identity) stays the stored
// definition's. A revision used to be saved whole, and the drafter writes
// none of those, so each one quietly dropped them.
func withMachineRevision(def, rev MachineDef) MachineDef {
	out := def
	if name := strings.TrimSpace(rev.Name); name != "" {
		out.Name = name
	}
	out.Description = rev.Description
	out.Start = rev.Start
	out.Phases = rev.Phases
	return out
}

// machinePropose drafts a change to a machine for review, without saving
// it (definition_assist.go).
//
//	POST /api/machines/{id}/propose {"description": "..."} → {proposal, from, changed[], changes[], checklist}
func (T *OrchestrateApp) machinePropose(c *jsonapi.Call, body struct {
	Description string `json:"description"`
}) (any, error) {
	mc, perr := T.machineAt(c)
	if perr != nil {
		return nil, perr
	}
	if err := mc.editable("revise"); err != nil {
		return nil, err
	}
	if T.LLM == nil {
		return nil, jsonapi.Status(http.StatusServiceUnavailable, "worker LLM not configured")
	}
	want := strings.TrimSpace(body.Description)
	if want == "" {
		return nil, jsonapi.BadRequest("say what should change")
	}
	if len(want) > maxDraftDescription {
		want = want[:maxDraftDescription]
	}
	def := mc.def
	revised, err := T.draftMachineRevision(c.R.Context(), def, want)
	if err != nil {
		return nil, err
	}
	after := withMachineRevision(def, revised)
	changes := machineChanges(def, after)
	changed := describeMachineChange(def, after)
	if len(changes) == 0 {
		changed = nil
	}
	return map[string]any{
		"proposal":  revised,
		"from":      definitionFingerprint(def),
		"changed":   changed,
		"changes":   changes,
		"checklist": after.Problems(),
	}, nil
}

// machineRevise applies a proposal: the machine as stored with the
// proposal's parts in place, the version before kept for Undo.
//
//	POST /api/machines/{id}/revise {"proposal": {...}, "from": "..."} → {id, name, changed[], checklist}
func (T *OrchestrateApp) machineRevise(c *jsonapi.Call, body struct {
	Proposal MachineDef `json:"proposal"`
	From     string     `json:"from"`
}) (any, error) {
	mc, perr := T.machineAt(c)
	if perr != nil {
		return nil, perr
	}
	if err := mc.editable("revise"); err != nil {
		return nil, err
	}
	udb, user, def := mc.ed.db, mc.ed.owner, mc.def
	if len(body.Proposal.Phases) == 0 {
		return nil, jsonapi.BadRequest("the proposal has no steps")
	}
	// Drafted against a version that is no longer the stored one: applying
	// it would quietly undo whatever changed in between.
	if body.From != definitionFingerprint(def) {
		return nil, jsonapi.Status(http.StatusConflict, "the machine changed after this was drafted: ask for the change again")
	}
	before := def
	before.Previous = nil // one deep: the snapshot must not carry its own
	after := withMachineRevision(def, body.Proposal)
	after.Owner = user
	after.Previous = &before

	saved := SaveMachineDef(udb, after)
	changed := describeMachineChange(before, saved)
	Log("[orchestrate.machines] user=%q revised machine %q (id=%s): %s", user, saved.Name, saved.ID,
		strings.Join(changed, "; "))
	return map[string]any{
		"id": saved.ID, "name": saved.Name,
		"changed":   changed,
		"checklist": saved.Problems(),
	}, nil
}

// handleMachineAssist is the conversation about a whole machine
// (definition_assist.go): POST /api/machines/{id}/assist {message, history}.
func (T *OrchestrateApp) handleMachineAssist(w http.ResponseWriter, r *http.Request, def MachineDef) {
	assist.Handler(assist.Spec{
		Route: "app.orchestrate.assist", Label: "Machines: whole-machine assist", App: "/orchestrate",
		Core: &T.AppCore, Think: assist.Off, Web: true,
		// The machine's route checked who is asking before it got here.
		Allow: func(http.ResponseWriter, *http.Request) bool { return true },
		Prompt: func(*http.Request, assist.Request) (string, error) {
			return definitionAssistPrompt("machine", ExportMachine(def), def.Problems()), nil
		},
		Parse: func(_ assist.Request, raw string) assist.Reply {
			reply, change := parseDefinitionAssist(raw)
			rp := assist.Reply{Reply: reply}
			if change != "" {
				rp.Extra = map[string]any{"change": change}
			}
			return rp
		},
	})(w, r)
}

// machineChanges is a proposal's review: each step it adds, removes or
// changes, as it is and as it would be, in the order the machine runs them.
func machineChanges(before, after MachineDef) []defChange {
	var out []defChange
	if before.Name != after.Name {
		out = append(out, defChange{Label: "Name", Current: before.Name, Value: after.Name})
	}
	if before.Description != after.Description {
		out = append(out, defChange{Label: "Description", Current: before.Description, Value: after.Description})
	}
	if before.StartPhase() != after.StartPhase() {
		out = append(out, defChange{Label: "Where it starts", Current: before.StartPhase(), Value: after.StartPhase()})
	}
	was := map[string]MachinePhase{}
	for _, p := range before.Phases {
		was[p.Name] = p
	}
	is := map[string]bool{}
	for _, p := range after.Phases {
		is[p.Name] = true
		old, existed := was[p.Name]
		switch {
		case !existed:
			out = append(out, defChange{Label: "New step " + strconv.Quote(p.Name), Value: definitionJSON(p)})
		default:
			if old.Prompt != p.Prompt {
				out = append(out, defChange{Label: "Step " + strconv.Quote(p.Name) + ": instructions", Current: old.Prompt, Value: p.Prompt})
			}
			if !samePhaseWiring(old, p) {
				ow, nw := old, p
				ow.Prompt, nw.Prompt = "", ""
				out = append(out, defChange{Label: "Step " + strconv.Quote(p.Name) + ": settings", Current: definitionJSON(ow), Value: definitionJSON(nw)})
			}
		}
	}
	for _, p := range before.Phases {
		if !is[p.Name] {
			out = append(out, defChange{Label: "Removed step " + strconv.Quote(p.Name), Current: definitionJSON(p)})
		}
	}
	return out
}

// machineUndo puts back the definition a revision replaced.
func (T *OrchestrateApp) machineUndo(c *jsonapi.Call, _ struct{}) (any, error) {
	mc, perr := T.machineAt(c)
	if perr != nil {
		return nil, perr
	}
	if err := mc.editable("undo a change to"); err != nil {
		return nil, err
	}
	udb, user, def := mc.ed.db, mc.ed.owner, mc.def
	if def.Previous == nil {
		return nil, jsonapi.BadRequest("there is no revision to undo")
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
	return map[string]any{"id": saved.ID, "name": saved.Name, "checklist": saved.Problems()}, nil
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
		out = append(out, "nothing: the revision came back identical to what was there")
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
