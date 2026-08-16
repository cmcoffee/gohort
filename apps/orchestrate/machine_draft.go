// A machine drafted from a description.
//
// The editor's remaining assumption was that you arrive with a design:
// it computes every choice, but you still invent the steps. This closes
// that. Describe what you want in a paragraph, get a complete draft
// machine, and land in the editor to adjust it — authoring flips from
// build-from-scratch to review-and-edit, which is the mode every other
// assist in the app already works in.
//
// The drafter reads the SAME spec the machine tool teaches Builder
// (machineHelpText) and its output goes through the SAME decoder
// (parseMachinePhases), so a drafted machine cannot be a third dialect.
// A draft with problems still saves: the editor exists to fix things,
// and its checklist phrases them as work remaining. Refusing to store an
// imperfect draft would throw away the good steps with the bad one.

package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// maxDraftDescription bounds the request. A description is a paragraph;
// ten pages is something else wearing its clothes.
const maxDraftDescription = 4000

// handleMachineDraft authors a machine from a plain-language description.
//
//	POST /api/machines/draft {"description": "..."} → {id, name, checklist}
func (T *OrchestrateApp) handleMachineDraft(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
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
	desc := strings.TrimSpace(body.Description)
	if desc == "" {
		http.Error(w, "describe what the machine should do", http.StatusBadRequest)
		return
	}
	if len(desc) > maxDraftDescription {
		desc = desc[:maxDraftDescription]
	}

	ctx := r.Context()
	ask := "Draft a machine for this:\n\n" + desc
	def, derr := T.draftMachineOnce(ctx, ask)
	// One repair pass, fed the actual failure. A decode error or a
	// problem list is exactly the feedback a second attempt can use, and
	// the same shape runDeclaredOutput already applies to phase output.
	if derr == nil {
		if probs := def.Problems(); len(probs) > 0 {
			if fixed, ferr := T.draftMachineOnce(ctx, ask+
				"\n\nA previous draft had these problems — produce a corrected machine:\n- "+
				strings.Join(probs, "\n- ")); ferr == nil && len(fixed.Problems()) < len(probs) {
				def = fixed
			}
		}
	} else {
		if def, derr = T.draftMachineOnce(ctx, ask+
			"\n\nYour previous reply could not be used: "+derr.Error()+
			"\nReply with ONLY the JSON object."); derr != nil {
			http.Error(w, "the draft could not be produced: "+derr.Error(), http.StatusBadGateway)
			return
		}
	}

	def.Owner = user
	if strings.TrimSpace(def.Name) == "" {
		def.Name = "Drafted machine"
	}
	saved := SaveMachineDef(udb, def)
	Log("[orchestrate.machines] user=%q drafted machine %q (id=%s, %d steps, %d problems left)",
		user, saved.Name, saved.ID, len(saved.Phases), len(saved.Problems()))
	writeJSON(w, map[string]any{"id": saved.ID, "name": saved.Name, "checklist": saved.Problems()})
}

// draftMachineOnce makes one authoring call and decodes it through the
// machine tool's own path.
func (T *OrchestrateApp) draftMachineOnce(ctx context.Context, ask string) (MachineDef, error) {
	resp, err := T.LLM.Chat(ctx,
		[]Message{{Role: "user", Content: ask}},
		WithSystemPrompt(machineDraftSystem),
		WithRouteKey("app.orchestrate.suggest"),
	)
	if err != nil {
		return MachineDef{}, err
	}
	if resp == nil || strings.TrimSpace(resp.Content) == "" {
		return MachineDef{}, errors.New("empty reply")
	}
	raw := extractJSONObject(resp.Content)
	if raw == "" {
		return MachineDef{}, errors.New("the reply carried no JSON object")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return MachineDef{}, errors.New("the JSON did not parse: " + err.Error())
	}
	phases, err := parseMachinePhases(m["phases"])
	if err != nil {
		return MachineDef{}, err
	}
	if len(phases) == 0 {
		return MachineDef{}, errors.New("the draft declared no steps")
	}
	return MachineDef{
		Name:        strings.TrimSpace(stringArg(m, "name")),
		Description: strings.TrimSpace(stringArg(m, "description")),
		Start:       strings.TrimSpace(stringArg(m, "start")),
		Phases:      phases,
	}, nil
}

// machineDraftSystem teaches the drafter with the machine tool's own
// spec, so a drafted machine and a Builder-authored one are the same
// dialect. The additions are the frame (one shot, JSON out) and the
// taste rules the spec assumes a reader who already knows them.
const machineDraftSystem = `You design ONE machine from a person's description, in one shot.

The full specification of what a machine is and every field a phase takes:

` + machineHelpText + `

DESIGN RULES

Fewer steps is better. The canonical shape — work something out once, decide a lane once, then a
step the conversation settles in — is three steps, and most ideas fit it. Only add a step when the
description genuinely needs another position for the conversation to hold.

Every machine needs at least one resident step (a step the conversation waits in). A step that
decides between destinations lists them in "choices" — never declare a routing field by hand.
Prompts say HOW to go about the work; the declared fields say WHAT to produce, each description
written as the instruction for that field. Never ask for JSON in a prompt, never place {input} —
the message arrives on its own.

Name steps in lowercase, short: triage, hunch, verify, answer. Give every step a one-line "desc" —
it becomes the routing instruction other steps see. Set "think": "on" only on steps that genuinely
judge.

Reply with ONLY a JSON object: {"name": ..., "description": ..., "start": ..., "phases": [...]}.
The name is short and specific to what THIS machine does. No prose around the JSON.`
