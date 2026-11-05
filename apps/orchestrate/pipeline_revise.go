// Describe a CHANGE to a pipeline that already exists, through the page's
// ✨ Get assistance (definition_assist.go): the conversation settles what
// should change, propose drafts it for review, revise applies it.
//
// The machine editor's version (machine_revise.go) with one real
// difference: a pipeline is VALIDATED at every door, so a revision that
// would not run is refused rather than stored. That makes the repair
// pass load-bearing instead of a nicety — the model gets the
// validator's own sentence back and one more attempt, and if it still
// cannot produce something runnable, nothing is saved and the reason
// says why.
//
// It reuses the tool's decoder (parsePipelineStages), so a revised
// pipeline cannot be a third dialect any more than a drafted machine
// can.

package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/assist"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// draftPipelineRevision redrafts a pipeline with a change made, through
// the drafter and the validator's repair pass. Nothing is saved; the result
// carries only what the drafter writes (name, description, stages), and it
// runs, or an error says why not.
func (T *OrchestrateApp) draftPipelineRevision(ctx context.Context, def PipelineDef, want string) (PipelineDef, error) {
	// The pipeline as it stands, in the dialect the drafter writes.
	// Its own export shape rather than prose about it: the model is
	// being asked to return this document edited, and describing the
	// document invites a rewrite from scratch.
	current, err := json.MarshalIndent(ExportPipeline(def), "", "  ")
	if err != nil {
		return PipelineDef{}, jsonapi.Status(http.StatusInternalServerError, "could not read the pipeline")
	}
	ask := "Revise this pipeline.\n\nWHAT SHOULD CHANGE:\n" + want +
		"\n\nTHE PIPELINE AS IT STANDS:\n" + string(current) +
		"\n\nReturn the WHOLE pipeline with that change made. Keep every stage, prompt and setting the " +
		"change does not touch, byte for byte: an edit that rewrites what nobody asked about is a " +
		"worse answer than one that refuses. Keep the name unless the change is about the name."

	revised, derr := T.draftPipelineOnce(ctx, ask)
	if derr != nil {
		if revised, derr = T.draftPipelineOnce(ctx, ask+
			"\n\nYour previous reply could not be used: "+derr.Error()+
			"\nReply with ONLY the JSON object."); derr != nil {
			return PipelineDef{}, jsonapi.Status(http.StatusBadGateway, "the revision could not be produced: "+derr.Error())
		}
	}
	// A pipeline is refused unless it runs, so the repair pass is what
	// makes this door usable at all rather than a polish step.
	if verr := revised.Validate(); verr != nil {
		fixed, ferr := T.draftPipelineOnce(ctx, ask+
			"\n\nA previous attempt would not run:\n"+verr.Error()+
			"\nReturn a corrected pipeline.")
		if ferr != nil || fixed.Validate() != nil {
			return PipelineDef{}, jsonapi.Status(http.StatusBadGateway, "the revision would not run, and a second attempt did not fix it:\n"+
				verr.Error()+"\n\nNothing was changed.")
		}
		revised = fixed
	}
	return revised, nil
}

// withPipelineRevision is def with a revision's parts in place of its own:
// name, description and stages. Everything else (who it is shared with,
// its follow-ups, its suggestion, its flags, its identity) stays the
// stored definition's. A revision used to be saved whole, and the drafter
// writes none of those, so each one quietly dropped them.
func withPipelineRevision(def, rev PipelineDef) PipelineDef {
	out := def
	if name := strings.TrimSpace(rev.Name); name != "" {
		out.Name = name
	}
	out.Description = rev.Description
	out.Stages = rev.Stages
	return out
}

// pipelinePropose drafts a change to a pipeline for review, without saving
// it (definition_assist.go).
//
//	POST /api/pipelines/{id}/propose {"description": "..."} → {proposal, from, changed[], changes[]}
func (T *OrchestrateApp) pipelinePropose(c *jsonapi.Call, body struct {
	Description string `json:"description"`
}) (any, error) {
	p, perr := T.pipelineAt(c)
	if perr != nil {
		return nil, perr
	}
	if err := p.editable("revise"); err != nil {
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
	def := p.def
	revised, err := T.draftPipelineRevision(c.R.Context(), def, want)
	if err != nil {
		return nil, err
	}
	after := withPipelineRevision(def, revised)
	changes := pipelineChanges(def, after)
	changed := describePipelineChange(def, after)
	if len(changes) == 0 {
		changed = nil
	}
	return map[string]any{
		"proposal": revised,
		"from":     definitionFingerprint(def),
		"changed":  changed,
		"changes":  changes,
	}, nil
}

// pipelineRevise applies a proposal: the pipeline as stored with the
// proposal's parts in place, refused unless it runs, the version before
// kept for Undo.
//
//	POST /api/pipelines/{id}/revise {"proposal": {...}, "from": "..."} → {id, name, changed[]}
func (T *OrchestrateApp) pipelineRevise(c *jsonapi.Call, body struct {
	Proposal PipelineDef `json:"proposal"`
	From     string      `json:"from"`
}) (any, error) {
	p, perr := T.pipelineAt(c)
	if perr != nil {
		return nil, perr
	}
	if err := p.editable("revise"); err != nil {
		return nil, err
	}
	udb, user, def := p.ed.db, p.ed.owner, p.def
	if len(body.Proposal.Stages) == 0 {
		return nil, jsonapi.BadRequest("the proposal has no stages")
	}
	if body.From != definitionFingerprint(def) {
		return nil, jsonapi.Status(http.StatusConflict, "the pipeline changed after this was drafted: ask for the change again")
	}
	after := withPipelineRevision(def, body.Proposal)
	// Validated at every door, this one included: the proposal came back
	// from the page.
	if verr := after.Validate(); verr != nil {
		return nil, jsonapi.BadRequest("this change would not run, so nothing was saved:\n" + verr.Error())
	}
	before := def
	before.Previous = nil // one deep
	after.Owner = user
	after.Previous = &before

	saved := SavePipelineDef(udb, after)
	changed := describePipelineChange(before, saved)
	Log("[orchestrate.pipelines] user=%q revised pipeline %q (id=%s): %s", user, saved.Name, saved.ID,
		strings.Join(changed, "; "))
	return map[string]any{"id": saved.ID, "name": saved.Name, "changed": changed}, nil
}

// handlePipelineAssist is the conversation about a whole pipeline
// (definition_assist.go): POST /api/pipelines/{id}/assist {message, history}.
func (T *OrchestrateApp) handlePipelineAssist(w http.ResponseWriter, r *http.Request, def PipelineDef) {
	assist.Handler(assist.Spec{
		Route: "app.orchestrate.assist", Label: "Pipelines: whole-pipeline assist", App: "/orchestrate",
		Core: &T.AppCore, Think: assist.Off, Web: true,
		// The pipeline's route checked who is asking before it got here.
		Allow: func(http.ResponseWriter, *http.Request) bool { return true },
		Prompt: func(*http.Request, assist.Request) (string, error) {
			var problems []string
			if err := def.Validate(); err != nil {
				problems = append(problems, err.Error())
			}
			return definitionAssistPrompt("pipeline", ExportPipeline(def), problems), nil
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

// pipelineChanges is a proposal's review: each stage it adds, removes or
// changes, as it is and as it would be, in the order the pipeline runs them.
func pipelineChanges(before, after PipelineDef) []defChange {
	var out []defChange
	if before.Name != after.Name {
		out = append(out, defChange{Label: "Name", Current: before.Name, Value: after.Name})
	}
	if before.Description != after.Description {
		out = append(out, defChange{Label: "Description", Current: before.Description, Value: after.Description})
	}
	was := map[string]PipelineStage{}
	for _, s := range before.Stages {
		was[s.Name] = s
	}
	is := map[string]bool{}
	for _, s := range after.Stages {
		is[s.Name] = true
		old, existed := was[s.Name]
		switch {
		case !existed:
			out = append(out, defChange{Label: "New stage " + strconv.Quote(s.Name), Value: definitionJSON(s)})
		default:
			if old.Prompt != s.Prompt {
				out = append(out, defChange{Label: "Stage " + strconv.Quote(s.Name) + ": instructions", Current: old.Prompt, Value: s.Prompt})
			}
			if !samePipelineWiring(old, s) {
				ow, nw := old, s
				ow.Prompt, nw.Prompt = "", ""
				out = append(out, defChange{Label: "Stage " + strconv.Quote(s.Name) + ": settings", Current: definitionJSON(ow), Value: definitionJSON(nw)})
			}
		}
	}
	for _, s := range before.Stages {
		if !is[s.Name] {
			out = append(out, defChange{Label: "Removed stage " + strconv.Quote(s.Name), Current: definitionJSON(s)})
		}
	}
	// The order alone can change: the same stages, run in another order.
	if len(out) == 0 && len(before.Stages) == len(after.Stages) {
		var a, b []string
		for i := range before.Stages {
			a, b = append(a, before.Stages[i].Name), append(b, after.Stages[i].Name)
		}
		if strings.Join(a, "\n") != strings.Join(b, "\n") {
			out = append(out, defChange{Label: "The order of the stages", Current: strings.Join(a, "\n"), Value: strings.Join(b, "\n")})
		}
	}
	return out
}

// pipelineUndo puts back the definition a revision replaced.
func (T *OrchestrateApp) pipelineUndo(c *jsonapi.Call, _ struct{}) (any, error) {
	p, perr := T.pipelineAt(c)
	if perr != nil {
		return nil, perr
	}
	if err := p.editable("undo a change to"); err != nil {
		return nil, err
	}
	udb, user, def := p.ed.db, p.ed.owner, p.def
	if def.Previous == nil {
		return nil, jsonapi.BadRequest("there is no revision to undo")
	}
	back := *def.Previous
	back.ID = def.ID
	back.Owner = user
	// Who it reaches is not part of the revision: it is changed through
	// sharing and the publish request, never put back by an undo.
	back.AllowedUsers, back.Editors, back.Published = def.AllowedUsers, def.Editors, def.Published
	// One step, deliberately: keeping the revision as the new snapshot
	// would make undo a toggle, which promises something else.
	back.Previous = nil
	saved := SavePipelineDef(udb, back)
	Log("[orchestrate.pipelines] user=%q undid the last revision of pipeline %q", user, saved.Name)
	return map[string]any{"id": saved.ID, "name": saved.Name}, nil
}

// draftPipelineOnce makes one authoring call and decodes it through the
// pipeline tool's own path.
func (T *OrchestrateApp) draftPipelineOnce(ctx context.Context, ask string) (PipelineDef, error) {
	resp, err := T.LLM.Chat(ctx,
		[]Message{{Role: "user", Content: ask}},
		WithSystemPrompt(pipelineDraftSystem),
		WithRouteKey("app.orchestrate.suggest"),
	)
	if err != nil {
		return PipelineDef{}, err
	}
	if resp == nil || strings.TrimSpace(resp.Content) == "" {
		return PipelineDef{}, errors.New("empty reply")
	}
	raw := extractJSONObject(resp.Content)
	if raw == "" {
		return PipelineDef{}, errors.New("the reply carried no JSON object")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return PipelineDef{}, errors.New("the JSON did not parse: " + err.Error())
	}
	stages, err := parsePipelineStages(m["stages"])
	if err != nil {
		return PipelineDef{}, err
	}
	if len(stages) == 0 {
		return PipelineDef{}, errors.New("the draft declared no stages")
	}
	return PipelineDef{
		Name:        strings.TrimSpace(stringArg(m, "name")),
		Description: strings.TrimSpace(stringArg(m, "description")),
		Stages:      stages,
	}, nil
}

// pipelineDraftSystem teaches the drafter with the pipeline tool's own
// spec, so a revised pipeline and a Builder-authored one are the same
// dialect.
const pipelineDraftSystem = `You revise or design ONE pipeline, in one shot.

The full specification of what a pipeline is and every field a stage takes:

` + pipelineHelpText + `

DESIGN RULES

Change the least that answers the request. When you are given a pipeline to revise, every stage,
prompt and setting the change does not concern comes back exactly as it went in.

Declaring "output" fields IS the structured-output mechanism: never ask for JSON in a prompt,
never describe a shape, never give an example object. Say what to FIND.

A stage that must reach the web has to declare its tools; a stage that declares none is a pure
transform with no access to anything, which is right for synthesis and wrong for research.

Reply with ONLY a JSON object: {"name": ..., "description": ..., "stages": [...]}. No prose
around it.`

// describePipelineChange says what a revision actually did, in stages.
//
// The reply has to be checkable against the pipeline on screen.
// "Revised" is not: a model that ignored the instruction and one that
// followed it produce the same word, and the risk of this door is a
// rewrite nobody asked for.
func describePipelineChange(before, after PipelineDef) []string {
	was := map[string]PipelineStage{}
	for _, s := range before.Stages {
		was[s.Name] = s
	}
	is := map[string]PipelineStage{}
	for _, s := range after.Stages {
		is[s.Name] = s
	}
	var added, removed, edited []string
	for name, s := range is {
		old, existed := was[name]
		switch {
		case !existed:
			added = append(added, name)
		case old.Prompt != s.Prompt:
			edited = append(edited, name+" (instructions)")
		case !samePipelineWiring(old, s):
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
		out = append(out, "renamed the pipeline to "+strconv.Quote(after.Name))
	}
	if len(before.Stages) != len(after.Stages) {
		out = append(out, "now "+strconv.Itoa(len(after.Stages))+" stages")
	}
	if len(out) == 0 {
		out = append(out, "nothing: the revision came back identical to what was there")
	}
	return out
}

// samePipelineWiring compares everything about a stage except its prose,
// so "changed X (wiring)" means something the picture would show.
func samePipelineWiring(a, b PipelineStage) bool {
	a.Prompt, b.Prompt = "", ""
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// handlePipelineDraft authors a pipeline from a plain-language
// description — the "describe one" door, and the counterpart to
// machine_draft.go.
//
//	POST /api/pipelines/draft {"description": "..."} → {id, name}
//
// Same drafter, same decoder and the same repair discipline as a
// revision: a pipeline that would not run is not stored, because every
// other pipeline door refuses too and a stored one that cannot run is
// a tool an agent will call and be failed by.
func (T *OrchestrateApp) handlePipelineDraft(c *jsonapi.Call, body struct {
	Description string `json:"description"`
}) (any, error) {
	r := c.R
	user, udb := c.User, c.UDB
	if T.LLM == nil {
		return nil, jsonapi.Status(http.StatusServiceUnavailable, "worker LLM not configured")
	}
	desc := strings.TrimSpace(body.Description)
	if desc == "" {
		return nil, jsonapi.BadRequest("describe what the pipeline should do")
	}
	if len(desc) > maxDraftDescription {
		desc = desc[:maxDraftDescription]
	}

	ask := "Design a pipeline for this:\n\n" + desc
	def, derr := T.draftPipelineOnce(r.Context(), ask)
	if derr != nil {
		if def, derr = T.draftPipelineOnce(r.Context(), ask+
			"\n\nYour previous reply could not be used: "+derr.Error()+
			"\nReply with ONLY the JSON object."); derr != nil {
			return nil, jsonapi.Status(http.StatusBadGateway, "the draft could not be produced: "+derr.Error())
		}
	}
	if verr := def.Validate(); verr != nil {
		fixed, ferr := T.draftPipelineOnce(r.Context(), ask+
			"\n\nA previous attempt would not run:\n"+verr.Error()+
			"\nReturn a corrected pipeline.")
		if ferr != nil || fixed.Validate() != nil {
			return nil, jsonapi.Status(http.StatusBadGateway, "the draft would not run, and a second attempt did not fix it:\n"+verr.Error())
		}
		def = fixed
	}
	def.ID = ""
	def.Owner = user
	if strings.TrimSpace(def.Name) == "" {
		def.Name = "Drafted pipeline"
	}
	saved := SavePipelineDef(udb, def)
	Log("[orchestrate.pipelines] user=%q drafted pipeline %q (id=%s, %d stages)",
		user, saved.Name, saved.ID, len(saved.Stages))
	return map[string]any{"id": saved.ID, "name": saved.Name, "stages": len(saved.Stages)}, nil
}
