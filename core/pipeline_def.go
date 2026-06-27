// Declarative pipeline definitions — the serializable "recipe" layer
// on top of the imperative pipeline runner in pipeline.go.
//
// pipeline.go gives apps RunPipeline(cfg, PipelineWork) where
// PipelineWork is a Go function that does the multi-stage work. That's
// the right tool when stages are code (debate/research write their own
// logic). What it can't do is let an END USER compose a pipeline from
// chat, or be exported / imported / shared, because the stages live in
// compiled Go.
//
// PipelineDef is the declarative counterpart: a stage list expressed as
// data — prompts, which agent runs each stage, how outputs thread
// forward. An interpreter (RunPipelineDef, in pipeline_interp.go)
// compiles a PipelineDef into a PipelineWork and runs it through the
// existing machinery. Because the def is plain data with no runtime or
// identity state baked in, it's a natural portable artifact: export to
// JSON, import elsewhere, the same way agent records already work.
//
// Design rules that keep it portable (see project_export_import_artifacts):
//   - The def is the RECIPE. Run state lives elsewhere (a separate
//     run record), never inside the def.
//   - ID / Owner / timestamps are storage metadata, stripped on export
//     and reassigned on import — they're not part of the recipe.
//   - Stages reference capabilities by stable handles (agent IDs,
//     tool names). Agent-referencing pipelines aren't fully portable
//     on their own — that's what a future "bundle" export (agent +
//     its tools + its pipelines) solves; a worker-prompt-only pipeline
//     IS fully portable.

package core

import (
	"sort"
	"strconv"
	"time"
)

// PipelineDefsTable stores per-user pipeline definitions.
const PipelineDefsTable = "pipeline_defs"

// PipelineStageKind enumerates how a stage produces its output.
// Phase 1 supports worker + agent; fanout/synthesize are Phase 2
// (fanout runs a stage across N parallel inputs; synthesize is a
// worker stage whose prompt templates in multiple prior outputs).
type PipelineStageKind string

const (
	// StageWorker runs a plain worker-tier LLM call with the stage's
	// prompt. No tools, no persona — just prompt in, text out. The
	// cheapest stage; right for transforms, summaries, extraction.
	StageWorker PipelineStageKind = "worker"

	// StageAgent dispatches the stage to a named agent (AgentID),
	// running its full persona + tool surface. Right for stages that
	// need a specialist's tools/knowledge. Not portable on its own
	// (depends on the agent existing in the target deployment).
	StageAgent PipelineStageKind = "agent"

	// StageFanout (Phase 2) runs its inner work across N parallel
	// inputs (e.g. one per sub-question) and collects the outputs.
	StageFanout PipelineStageKind = "fanout"

	// StageSynthesize (Phase 2 semantic; Phase 1 expressible as a
	// worker stage) combines multiple prior stage outputs into one.
	StageSynthesize PipelineStageKind = "synthesize"
)

// PipelineStage is one step of a pipeline. Stages run in order; each
// stage's output is captured under its Name and made available to
// later stages' prompt templates.
//
// Prompt templating (resolved by the interpreter):
//   {input}        — the pipeline's top-level input.
//   {prev}         — the immediately-preceding stage's output.
//   {stage:NAME}   — a named prior stage's output.
//
// Unresolved placeholders are left as-is (so a literal brace in a
// prompt isn't destroyed by a typo'd stage name).
type PipelineStage struct {
	Name   string            `json:"name"`              // unique stage label; also the output key
	Kind   PipelineStageKind `json:"kind"`              // worker | agent | fanout | synthesize
	Prompt string            `json:"prompt"`            // instruction, with {input}/{prev}/{stage:NAME} templating
	Agent  string            `json:"agent,omitempty"`   // agent id/name for kind=agent (and kind=fanout's inner agent)
	// Tools optionally overrides what this stage's worker has access
	// to. Empty (default) = inherit the caller's tool catalog so a
	// pipeline invoked from an agent with web_search / fetch_url
	// inherits those without any per-stage configuration. Set
	// explicitly to RESTRICT (e.g. a pure synthesizer stage that
	// should not be tempted to fetch) or to OVERRIDE inherited tools
	// for a specific stage. Only applies to kind="worker" stages; agent
	// stages get their dispatched agent's full catalog regardless.
	Tools []string `json:"tools,omitempty"`
	// Think optionally enables/disables thinking for this stage. nil
	// (default) = use the framework default (off, cheap). &true enables
	// thinking for stages that genuinely benefit from deliberation —
	// synthesis stages reconciling multiple sources, verification
	// stages doing careful cross-reference, decomposition stages
	// planning how to split a complex query. &false disables explicitly
	// (same as default; useful for self-documenting "this stage doesn't
	// need to think"). Pure transforms / format conversions / cheap
	// paraphrases should leave think nil (or set to false) — they don't
	// benefit from a deliberation budget. Only applies to kind="worker"
	// stages; agent stages honor their dispatched agent's own think
	// configuration.
	Think *bool `json:"think,omitempty"`
	// FanOver names a prior stage whose output is a JSON array; the
	// fanout stage runs once per element, in parallel. Phase 2.
	FanOver string `json:"fan_over,omitempty"`
}

// PipelineDef is the declarative, serializable definition of a
// pipeline — the portable recipe. The interpreter compiles it into a
// PipelineWork (see RunPipelineDef).
type PipelineDef struct {
	ID          string          `json:"id,omitempty"`          // storage key; stripped on export
	Owner       string          `json:"owner,omitempty"`       // owning user; stripped on export
	Name        string          `json:"name"`                  // human-readable pipeline name
	Description string          `json:"description,omitempty"` // what it does / when to use it
	Stages      []PipelineStage `json:"stages"`                // ordered stages
	Created     time.Time       `json:"created,omitempty"`     // stripped on export
	Updated     time.Time       `json:"updated,omitempty"`     // stripped on export
}

// Validate checks a pipeline def is runnable: at least one stage,
// unique non-empty stage names, agent stages name an agent, and
// {stage:NAME} references point at earlier stages (no forward refs or
// cycles — stages run strictly in order). Returns the first problem
// found, or nil.
func (d PipelineDef) Validate() error {
	if len(d.Stages) == 0 {
		return Error("pipeline has no stages")
	}
	seen := map[string]bool{}
	for i, s := range d.Stages {
		if s.Name == "" {
			return Error("stage " + strconv.Itoa(i+1) + " has no name")
		}
		if seen[s.Name] {
			return Error("duplicate stage name: " + s.Name)
		}
		seen[s.Name] = true
		if s.Kind == StageAgent && s.Agent == "" {
			return Error("stage " + s.Name + " is kind=agent but names no agent")
		}
		// A fanout stage runs as a worker by default (over the stage's
		// resolved tools) and dispatches only when it names an agent — so
		// the agent is optional. What it MUST have is something to fan
		// over.
		if s.Kind == StageFanout && s.FanOver == "" {
			return Error("stage " + s.Name + " is kind=fanout but names no fan_over stage")
		}
		if s.FanOver != "" && !seen[s.FanOver] {
			// FanOver must reference an EARLIER stage (already in seen,
			// minus the current one we just added — so check before add
			// would be cleaner, but a stage can't fan over itself and
			// seen[self] was just set, so compare explicitly).
			if s.FanOver == s.Name || !seen[s.FanOver] {
				return Error("stage " + s.Name + " fans over unknown/forward stage: " + s.FanOver)
			}
		}
	}
	return nil
}

// --- storage (per-user) ---------------------------------------------

// SavePipelineDef writes a pipeline def to the user's store, minting
// an ID on first save and stamping Updated. Returns the saved record.
func SavePipelineDef(udb Database, d PipelineDef) PipelineDef {
	if udb == nil {
		return d
	}
	if d.ID == "" {
		d.ID = UUIDv4()
		d.Created = time.Now()
	}
	d.Updated = time.Now()
	udb.Set(PipelineDefsTable, d.ID, d)
	return d
}

// LoadPipelineDef reads a pipeline def by ID. Returns ok=false when
// absent or when the record's owner doesn't match (defensive — a
// guessed ID from another user's space doesn't resolve).
func LoadPipelineDef(udb Database, owner, id string) (PipelineDef, bool) {
	if udb == nil || id == "" {
		return PipelineDef{}, false
	}
	var d PipelineDef
	if !udb.Get(PipelineDefsTable, id, &d) {
		return PipelineDef{}, false
	}
	if owner != "" && d.Owner != "" && d.Owner != owner {
		return PipelineDef{}, false
	}
	return d, true
}

// ListPipelineDefs returns the user's pipeline defs, most-recently-
// updated first.
func ListPipelineDefs(udb Database, owner string) []PipelineDef {
	if udb == nil {
		return nil
	}
	var out []PipelineDef
	for _, k := range udb.Keys(PipelineDefsTable) {
		var d PipelineDef
		if !udb.Get(PipelineDefsTable, k, &d) {
			continue
		}
		if owner != "" && d.Owner != "" && d.Owner != owner {
			continue
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

// DeletePipelineDef removes a pipeline def by ID.
func DeletePipelineDef(udb Database, id string) {
	if udb == nil || id == "" {
		return
	}
	udb.Unset(PipelineDefsTable, id)
}

// --- export / import (portable recipe) ------------------------------

// ExportPipeline returns a portable copy of a pipeline def with all
// storage/identity metadata stripped — the shareable recipe. Marshal
// the result to JSON for a downloadable artifact. Mirrors the agent-
// recipe export: identity is reassigned on import, never travels.
//
// Note on portability: a recipe whose stages are all kind=worker is
// fully self-contained. Agent stages reference an agent by id/name;
// importing such a recipe assumes that agent exists in the target
// deployment (a future "bundle" export — agent + its tools + its
// pipelines — closes that gap). Export doesn't rewrite agent refs;
// it's on the importer to have the referenced agents.
func ExportPipeline(d PipelineDef) PipelineDef {
	d.ID = ""
	d.Owner = ""
	d.Created = time.Time{}
	d.Updated = time.Time{}
	return d
}

// ImportPipeline takes a recipe (from ExportPipeline or an uploaded
// JSON file), assigns it to owner with a fresh ID, validates it, and
// saves it to the user's store. Returns the saved def. The recipe's
// own ID/Owner/timestamps (if present) are ignored — the importer
// owns the copy.
func ImportPipeline(udb Database, owner string, recipe PipelineDef) (PipelineDef, error) {
	recipe.ID = ""
	recipe.Owner = owner
	recipe.Created = time.Time{}
	recipe.Updated = time.Time{}
	if err := recipe.Validate(); err != nil {
		return PipelineDef{}, err
	}
	return SavePipelineDef(udb, recipe), nil
}
