// Describe a CHANGE to a pipeline that already exists.
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
)

// handlePipelineRevise redrafts an existing pipeline from a description
// of what should change.
//
//	POST /api/pipelines/{id}/revise {"description": "..."} → {id, changed[]}
func (T *OrchestrateApp) handlePipelineRevise(w http.ResponseWriter, r *http.Request, udb Database, user string, def PipelineDef) {
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

	// The pipeline as it stands, in the dialect the drafter writes.
	// Its own export shape rather than prose about it: the model is
	// being asked to return this document edited, and describing the
	// document invites a rewrite from scratch.
	current, err := json.MarshalIndent(ExportPipeline(def), "", "  ")
	if err != nil {
		http.Error(w, "could not read the pipeline", http.StatusInternalServerError)
		return
	}
	ask := "Revise this pipeline.\n\nWHAT SHOULD CHANGE:\n" + want +
		"\n\nTHE PIPELINE AS IT STANDS:\n" + string(current) +
		"\n\nReturn the WHOLE pipeline with that change made. Keep every stage, prompt and setting the " +
		"change does not touch, byte for byte — an edit that rewrites what nobody asked about is a " +
		"worse answer than one that refuses. Keep the name unless the change is about the name."

	revised, derr := T.draftPipelineOnce(r.Context(), ask)
	if derr != nil {
		if revised, derr = T.draftPipelineOnce(r.Context(), ask+
			"\n\nYour previous reply could not be used: "+derr.Error()+
			"\nReply with ONLY the JSON object."); derr != nil {
			http.Error(w, "the revision could not be produced: "+derr.Error(), http.StatusBadGateway)
			return
		}
	}
	// A pipeline is refused unless it runs, so the repair pass is what
	// makes this door usable at all rather than a polish step.
	if verr := revised.Validate(); verr != nil {
		fixed, ferr := T.draftPipelineOnce(r.Context(), ask+
			"\n\nA previous attempt would not run:\n"+verr.Error()+
			"\nReturn a corrected pipeline.")
		if ferr != nil || fixed.Validate() != nil {
			http.Error(w, "the revision would not run, and a second attempt did not fix it:\n"+
				verr.Error()+"\n\nNothing was changed.", http.StatusBadGateway)
			return
		}
		revised = fixed
	}

	// Identity is the editor's, not the model's: the ID keeps every
	// agent that attached this pipeline pointing at it.
	before := def
	before.Previous = nil // one deep
	revised.ID = def.ID
	revised.Owner = user
	if strings.TrimSpace(revised.Name) == "" {
		revised.Name = def.Name
	}
	revised.Previous = &before

	saved := SavePipelineDef(udb, revised)
	changed := describePipelineChange(before, saved)
	Log("[orchestrate.pipelines] user=%q revised pipeline %q (id=%s): %s", user, saved.Name, saved.ID,
		strings.Join(changed, "; "))
	writeJSON(w, map[string]any{"id": saved.ID, "name": saved.Name, "changed": changed})
}

// handlePipelineUndo puts back the definition a revision replaced.
func (T *OrchestrateApp) handlePipelineUndo(w http.ResponseWriter, r *http.Request, udb Database, user string, def PipelineDef) {
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
	// One step, deliberately: keeping the revision as the new snapshot
	// would make undo a toggle, which promises something else.
	back.Previous = nil
	saved := SavePipelineDef(udb, back)
	Log("[orchestrate.pipelines] user=%q undid the last revision of pipeline %q", user, saved.Name)
	writeJSON(w, map[string]any{"id": saved.ID, "name": saved.Name})
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
		out = append(out, "nothing — the revision came back identical to what was there")
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
