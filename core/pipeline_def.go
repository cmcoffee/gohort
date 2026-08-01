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
	"strings"
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

	// StageLoop runs its Body stages repeatedly — Count times, or
	// fewer when Until goes true. Where fanout does BREADTH (the same
	// prompt across N independent items, in parallel), loop does DEPTH:
	// each pass sees what the previous pass produced. That carry is the
	// whole point, and it's why the two can't be collapsed into one
	// primitive.
	StageLoop PipelineStageKind = "loop"

	// StageBranch is the only stage that makes no LLM call: it reads a
	// bool field an earlier stage declared and, when true, either ends
	// the pipeline or skips forward past stages that no longer apply.
	// The two shapes it exists for are "the input was rejected, stop"
	// and "this was supplied already, skip the stage that derives it".
	StageBranch PipelineStageKind = "branch"

	// StageTool calls one of the caller's tools directly, with arguments
	// the AUTHOR wrote, and captures its result. No LLM in the loop.
	//
	// This is the escape hatch that keeps the stage vocabulary small.
	// Deterministic work — arithmetic, dedup, normalization, a cache
	// lookup, a specific API call — does not want a model's judgment,
	// and without this kind every app that needs some would argue for a
	// new stage kind of its own. It also removes the worst reason to ask
	// an LLM to do arithmetic: a stage can just call the calculator.
	//
	// Safer than a tool-equipped worker stage, not riskier: the
	// arguments come from a saved definition a human wrote and reviewed,
	// rather than from whatever the model decided to pass this run.
	StageTool PipelineStageKind = "tool"
)

// loopMaxIterations bounds any single loop stage. A pipeline is
// unattended, and a loop whose Until never fires would otherwise burn
// LLM calls until the context is cancelled. Count is validated against
// this at save time, so the ceiling is a definition error rather than a
// run-time surprise.
const loopMaxIterations = 25

// PipelineStage is one step of a pipeline. Stages run in order; each
// stage's output is captured under its Name and made available to
// later stages' prompt templates.
//
// Prompt templating (resolved by the interpreter):
//
//	{input}        — the pipeline's top-level input.
//	{prev}         — the immediately-preceding stage's output.
//	{stage:NAME}   — a named prior stage's output.
//
// Unresolved placeholders are left as-is (so a literal brace in a
// prompt isn't destroyed by a typo'd stage name).
type PipelineStage struct {
	Name   string            `json:"name"`            // unique stage label; also the output key
	Kind   PipelineStageKind `json:"kind"`            // worker | agent | fanout | synthesize
	Prompt string            `json:"prompt"`          // instruction, with {input}/{prev}/{stage:NAME} templating
	Agent  string            `json:"agent,omitempty"` // agent id/name for kind=agent (and kind=fanout's inner agent)
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
	// Accepts "NAME" (the whole stage output, parsed as a list) or
	// "NAME.field" (a declared list field of a structured stage — see
	// Output). The field form is what a stage with an Output contract
	// needs, since its raw text is a JSON *object*, not a bare array.
	FanOver string `json:"fan_over,omitempty"`
	// Output declares the shape of this stage's result. Empty (the
	// default) = the stage returns free text and behaves exactly as it
	// always has. Non-empty = the interpreter appends a field contract
	// to the prompt, asks for JSON, validates the reply against these
	// fields, and exposes each one to later stages as
	// {stage:NAME.field}. Turns stage-to-stage threading from string
	// interpolation into data threading — which is what a predicate, a
	// loop carry, or a fan-over-one-field all need.
	//
	// Not valid on kind="fanout": a fanout's output is the joined
	// per-branch block, not a single JSON object.
	Output []PipelineField `json:"output,omitempty"`

	// Body is the stage list a kind="loop" stage repeats. Runs in order
	// once per iteration; ONE level only (a loop may not contain a
	// loop), the same depth rule PipelineField follows.
	//
	// Body stages are scoped to the loop: they may read outer stages
	// that ran before it, and each other, but nothing AFTER the loop can
	// reference them by name — they hold a different value every pass,
	// so a reference from outside would silently mean "whatever the last
	// iteration happened to leave." The loop's own name is what later
	// stages read.
	Body []PipelineStage `json:"body,omitempty"`

	// Count is how many times a loop runs: required for kind="loop",
	// 1..loopMaxIterations. With Until set this is the CEILING rather
	// than the exact count, which is what guarantees termination.
	Count int `json:"count,omitempty"`

	// Until optionally ends a loop early, as a "NAME.field" reference to
	// a bool field declared by one of the Body stages. Checked after
	// each full pass; true means stop. Requires that stage to declare
	// the field via Output — which is exactly why structured outputs had
	// to land before loop.
	Until string `json:"until,omitempty"`

	// Collect chooses a loop's output: "last" (default) is the final
	// pass's last stage, "all" joins every iteration into one labeled
	// block the way fanout does. Refinement loops want last; a loop
	// building a transcript wants all.
	Collect string `json:"collect,omitempty"`

	// When is a kind="branch" stage's condition: a "NAME.field"
	// reference to a bool an EARLIER stage declared. True takes the
	// branch, false falls through to the next stage.
	When string `json:"when,omitempty"`

	// Tool names the tool a kind="tool" stage calls. Resolved at run
	// time against the same set a worker stage would see (the caller's
	// catalog, narrowed by Tools when set) — tool availability is
	// per-user and per-agent, so it can't be checked when the pipeline
	// is saved.
	Tool string `json:"tool,omitempty"`

	// Args are the arguments passed to that tool: param name → template,
	// carrying the full templating vocabulary ({input}, {prev},
	// {stage:NAME}, {stage:NAME.field}, and {iteration} inside a loop).
	// Values render to strings; a tool wanting a number coerces the same
	// way it does for a model-supplied call.
	Args map[string]string `json:"args,omitempty"`

	// Model picks the LLM tier a worker stage runs on: "" or "worker"
	// (default, the local/primary tier) or "lead" (the precision tier).
	// The declarative equivalent of the RouteStage keys compiled apps
	// register — a pipeline's decompose and judge stages want the
	// stronger model while its transforms do not, and paying lead rates
	// for every stage is how a cheap pipeline stops being cheap.
	//
	// Applies to kind="worker"/"synthesize" and to a worker-mode
	// fanout. NOT valid on an agent stage (the dispatched agent's own
	// configuration decides its tier), nor on branch (no LLM call) or
	// loop (its body stages carry their own).
	//
	// Falls back to worker when no separate lead is configured, the
	// same as every other lead call.
	Model string `json:"model,omitempty"`

	// SkipTo is where a taken branch goes: the name of a LATER stage in
	// the same list. Empty means end the pipeline, returning whatever
	// the last stage produced.
	//
	// Forward-only, deliberately. A backward jump is iteration, and
	// iteration belongs to kind="loop" where Count bounds it — allowing
	// one here would reintroduce unbounded looping through the back
	// door, past the ceiling loops exist to enforce.
	SkipTo string `json:"skip_to,omitempty"`
}

// PipelineFieldType is the declared type of a structured output field.
// A closed set on purpose: each value has to render into a prompt
// contract, validate a decoded reply, and render back into a later
// stage's template, and every addition costs all three.
type PipelineFieldType string

const (
	FieldString PipelineFieldType = "string"
	FieldNumber PipelineFieldType = "number"
	FieldBool   PipelineFieldType = "bool"
	FieldList   PipelineFieldType = "list"
	FieldObject PipelineFieldType = "object"
)

// PipelineField is one declared field of a stage's structured output.
//
// This is a field list rather than a JSON Schema because of who writes
// it: Builder authors these and a user edits them in a form. A field
// list renders to a prompt instruction, to a validator, and to a UI
// table without any of them having to understand JSON Schema. The cost
// is depth — see Fields.
type PipelineField struct {
	// Name is the JSON key and the handle in {stage:NAME.name}.
	// Lowercase [a-z0-9_]+ so the templating stays unambiguous.
	Name string `json:"name"`
	// Type is one of the PipelineFieldType constants. Empty defaults to
	// string.
	Type PipelineFieldType `json:"type,omitempty"`
	// Desc is rendered into the prompt contract — this is how the model
	// learns what belongs in the field. Worth writing.
	Desc string `json:"desc,omitempty"`
	// Fields describes the element shape for Type=list and the member
	// shape for Type=object. ONE level only: a nested field may not
	// itself declare Fields. Deeper structure is still expressible, just
	// not addressable — it renders as JSON into the consuming prompt.
	Fields []PipelineField `json:"fields,omitempty"`
	// Required fails the stage when the model omits the field. Default
	// false: an absent optional field resolves to its type's zero value.
	Required bool `json:"required,omitempty"`
}

// resolved returns the field's effective type, defaulting empty to
// string so a half-filled authoring call still produces a usable field.
func (f PipelineField) resolved() PipelineFieldType {
	if f.Type == "" {
		return FieldString
	}
	return f.Type
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
	// Global scopes the pipeline to ALL of the owner's agents (minus any that
	// deny it via AgentRecord.DisabledPipelines), the way a global tool lives
	// in the user-wide pool. Off = available only to the agents that list its
	// ID in AttachedPipelines. Managed via the scope pill; a local decision,
	// so it's stripped on export (an imported pipeline lands non-global).
	Global  bool      `json:"global,omitempty"`
	Created time.Time `json:"created,omitempty"` // stripped on export
	Updated time.Time `json:"updated,omitempty"` // stripped on export
}

// Validate checks a pipeline def is runnable: at least one stage,
// unique non-empty stage names, agent stages name an agent, declared
// output fields are well-formed, and every {stage:NAME} /
// {stage:NAME.field} / fan_over reference points at an EARLIER stage
// (no forward refs or cycles — stages run strictly in order). Returns
// the first problem found, or nil.
//
// Reference checking happens before the current stage is registered, so
// a self-reference fails the same way a forward reference does. (The
// previous implementation registered the stage first and then tried to
// special-case self-fanout, which let `fan_over: <self>` through to a
// runtime error.)
func (d PipelineDef) Validate() error {
	if len(d.Stages) == 0 {
		return Error("pipeline has no stages")
	}
	// done maps an already-validated stage name to its declared output
	// fields. Built as the walk proceeds, so a lookup miss IS the
	// forward/unknown-reference error.
	done := make(map[string]map[string]PipelineFieldType, len(d.Stages))
	return validateStageList(d.Stages, done, false)
}

// validateStageList validates one stage list against the scope built so
// far, recursing once into a loop's Body. done accumulates every
// validated stage's declared fields, so a lookup miss IS the
// forward/unknown-reference error. inLoop marks the recursive call, which
// is how the one-level depth rule is enforced.
// Every INDEPENDENT problem is reported, not just the first.
//
// A pipeline is authored as one object, so its mistakes arrive as a set: a
// wrong field type, an output on a stage that can't take one, and a fan_over
// written as a prompt template were all present in the same submission, and
// returning them one at a time turned one fix into three round-trips, each
// costing a full re-send of the definition. Checks that would CASCADE are still
// suppressed — an unnamed stage skips its own remaining checks, and a field
// reference into a stage whose output failed is that stage's problem, not a
// second one.
func validateStageList(stages []PipelineStage, done map[string]map[string]PipelineFieldType, inLoop bool) error {
	probs := stageListProblems(stages, done, inLoop)
	switch len(probs) {
	case 0:
		return nil
	case 1:
		return Error(probs[0])
	}
	return Error("this pipeline has " + strconv.Itoa(len(probs)) + " problems — fix them all in one revision:\n- " + strings.Join(probs, "\n- "))
}

// stageListProblems is validateStageList's collector. Split out so a LOOP
// BODY's problems flatten into the caller's list instead of arriving as one
// already-joined string: nested, the count was wrong and the header printed
// twice ("this pipeline has 2 problems" followed by a bullet repeating it).
func stageListProblems(stages []PipelineStage, done map[string]map[string]PipelineFieldType, inLoop bool) []string {
	var probs []string
	badOutput := map[string]bool{} // stages whose declared output didn't parse
	for i, s := range stages {
		// Identity first: every check below reads the name, and reporting
		// "stage : output field..." for a nameless stage helps nobody. These
		// end the stage rather than adding to it.
		switch {
		case s.Name == "":
			probs = append(probs, "stage "+strconv.Itoa(i+1)+" has no name")
			continue
		case done[s.Name] != nil || badOutput[s.Name]:
			probs = append(probs, "duplicate stage name: "+s.Name)
			continue
		case strings.Contains(s.Name, "."):
			// A dot would make {stage:a.b} ambiguous between a stage
			// named "a.b" and field "b" of stage "a".
			probs = append(probs, "stage name may not contain a dot: "+s.Name)
			continue
		}
		add := func(err error) {
			if err != nil {
				probs = append(probs, err.Error())
			}
		}
		if s.Kind == StageAgent && s.Agent == "" {
			probs = append(probs, "stage "+s.Name+" is kind=agent but names no agent")
		}
		// A fanout stage runs as a worker by default (over the stage's
		// resolved tools) and dispatches only when it names an agent — so
		// the agent is optional. What it MUST have is something to fan
		// over.
		if s.Kind == StageFanout && s.FanOver == "" {
			probs = append(probs, "stage "+s.Name+" is kind=fanout but names no fan_over stage")
		}
		if s.Kind == StageFanout && len(s.Output) > 0 {
			probs = append(probs, "stage "+s.Name+": output is not valid on kind=fanout (a fanout produces a joined per-branch block, not one JSON object)")
		}
		if s.Kind != StageLoop && len(s.Body) > 0 {
			probs = append(probs, "stage "+s.Name+": body is only valid on kind=loop")
		}
		if s.Kind == StageLoop {
			own, bodyProbs := validateLoopStage(s, done, inLoop)
			add(own)
			probs = append(probs, bodyProbs...)
		}
		if s.Kind == StageBranch {
			add(validateBranchStage(s, stages, i, done, inLoop))
		} else if s.When != "" || s.SkipTo != "" {
			probs = append(probs, "stage "+s.Name+": when/skip_to are only valid on kind=branch")
		}
		add(validateStageModel(s))
		if s.Kind == StageTool {
			add(validateToolStage(s, done))
		} else if s.Tool != "" || len(s.Args) > 0 {
			probs = append(probs, "stage "+s.Name+": tool/args are only valid on kind=tool")
		}
		for _, ref := range stageRefs(s.Prompt) {
			if name, _ := SplitStageRef(ref); !badOutput[name] {
				add(checkStageRef(s.Name, "prompt", ref, done))
			}
		}
		if s.FanOver != "" {
			src, field := SplitStageRef(s.FanOver)
			if !badOutput[src] {
				add(checkStageRef(s.Name, "fan_over", s.FanOver, done))
				// A field reference has to BE a list; the whole-output form
				// is parsed leniently at run time and can't be checked here.
				if field != "" && done[src] != nil {
					if t := done[src][field]; t != FieldList {
						probs = append(probs, "stage "+s.Name+" fans over "+s.FanOver+", which is declared "+string(t)+", not list")
					}
				}
			}
		}
		own, err := validateOutputFields(s.Name, s.Output, false)
		if err != nil {
			probs = append(probs, err.Error())
			badOutput[s.Name] = true
		}
		// Registered even when it failed, so a LATER stage referencing this one
		// by name doesn't also report "unknown stage" — one mistake, one line.
		if own == nil {
			own = map[string]PipelineFieldType{}
		}
		done[s.Name] = own
	}
	return probs
}

// validateLoopStage checks a kind="loop" stage and its Body. The body is
// validated against a COPY of the outer scope: body stages can read
// what ran before the loop and each other, but their names never reach
// the caller's scope, so a later stage referencing one is an unknown-
// stage error rather than a silent read of whichever value the last
// iteration left behind.
func validateLoopStage(s PipelineStage, done map[string]map[string]PipelineFieldType, inLoop bool) (error, []string) {
	if inLoop {
		return Error("stage " + s.Name + ": loops do not nest — one level only (put the inner work in its own pipeline and call it from a stage)"), nil
	}
	if len(s.Body) == 0 {
		return Error("stage " + s.Name + " is kind=loop but has no body stages to repeat"), nil
	}
	if len(s.Output) > 0 {
		return Error("stage " + s.Name + ": output is not valid on kind=loop (a loop's output is its last pass, or the joined passes when collect=all)"), nil
	}
	if s.Count < 1 {
		return Error("stage " + s.Name + ": kind=loop needs count (how many times to repeat, 1-" + strconv.Itoa(loopMaxIterations) + ")"), nil
	}
	if s.Count > loopMaxIterations {
		return Error("stage " + s.Name + ": count " + strconv.Itoa(s.Count) + " exceeds the maximum of " + strconv.Itoa(loopMaxIterations) + " — a pipeline runs unattended, so the ceiling is fixed"), nil
	}
	switch strings.TrimSpace(s.Collect) {
	case "", "last", "all":
	default:
		return Error("stage " + s.Name + ": collect must be \"last\" or \"all\", got " + strconv.Quote(s.Collect)), nil
	}
	// Body scope starts as a copy of the outer one so the body can read
	// earlier stages without leaking its own names back out.
	inner := make(map[string]map[string]PipelineFieldType, len(done)+len(s.Body))
	for k, v := range done {
		inner[k] = v
	}
	bodyProbs := stageListProblems(s.Body, inner, true)
	if ref := strings.TrimSpace(s.Until); ref != "" {
		if err := checkLoopUntil(s, ref, done, inner); err != nil {
			return err, bodyProbs
		}
	}
	return nil, bodyProbs
}

// untilShape is the one sentence that turns "until is wrong" into "here is what
// until is". Appended to every until refusal, because the field is the least
// guessable thing in the vocabulary: it is not a condition, it is the NAME of a
// bool a body stage promised to return.
const untilShape = "until reads ONE bool field, by bare name: until:\"check.done\", where a body stage named check declares output:[{\"name\":\"done\",\"type\":\"bool\"}]. It is not an expression — there is no ==, no quotes, no braces. To stop when a critic is satisfied, have the critic stage declare a bool (\"satisfied\") alongside its prose and point until at it."

// checkLoopUntil validates a loop's early exit.
//
// Every refusal carries the shape, because the observed failures were not typos
// — they were an author reaching for a CONDITION. Written as
// {stage:critic.feedback} == 'SATISFIED', then as stage:critic.feedback ==
// 'SATISFIED', then abandoned: three rounds, after which the loop was replaced
// by five hand-copied stage pairs that cannot stop early at all.
func checkLoopUntil(s PipelineStage, ref string, done, inner map[string]map[string]PipelineFieldType) error {
	bad := func(why string) error {
		return Error("stage " + s.Name + ": until " + why + ". " + untilShape)
	}
	if strings.ContainsAny(ref, "={}<>!'\"") || strings.Contains(ref, " ") {
		return bad("is written as a condition (" + strconv.Quote(ref) + ")")
	}
	name, field := SplitStageRef(ref)
	if field == "" {
		return bad("names a stage (" + strconv.Quote(ref) + ") and not a field of one")
	}
	if _, ok := inner[name]; !ok {
		return bad("references " + strconv.Quote(name) + ", which is not a stage in this loop's body")
	}
	t, declared := inner[name][field]
	if !declared {
		return bad("references " + strconv.Quote(ref) + ", but " + name + " declares no output field " + strconv.Quote(field))
	}
	if t != FieldBool {
		return bad("references " + ref + ", which is declared " + string(t) + ", not bool")
	}
	if _, outer := done[name]; outer {
		return Error("stage " + s.Name + ": until references " + ref + ", which is OUTSIDE the loop — its value never changes between passes, so the loop would either run once or all " + strconv.Itoa(s.Count) + " times. Point it at a body stage.")
	}
	return nil
}

// validateToolStage checks a kind="tool" stage. The tool NAME can't be
// verified here — availability is per-user and per-agent, resolved at
// run time — but everything about the call's shape can be.
func validateToolStage(s PipelineStage, done map[string]map[string]PipelineFieldType) error {
	if strings.TrimSpace(s.Tool) == "" {
		return Error("stage " + s.Name + " is kind=tool but names no tool")
	}
	if strings.TrimSpace(s.Prompt) != "" {
		return Error("stage " + s.Name + ": a tool stage takes args, not a prompt — there is no model to prompt. Put the values in args.")
	}
	if strings.TrimSpace(s.Agent) != "" {
		return Error("stage " + s.Name + ": agent does not apply to a tool stage — use kind=agent to dispatch, or kind=tool to call a tool directly")
	}
	if s.Think != nil {
		return Error("stage " + s.Name + ": think does not apply to a tool stage — no model runs")
	}
	// Every {stage:...} in an argument is a real reference and gets the
	// same forward/unknown check a prompt does. Arg names are sorted so
	// the FIRST error reported is stable across runs rather than
	// whichever key Go's map iteration happened to reach first.
	names := make([]string, 0, len(s.Args))
	for k := range s.Args {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		for _, ref := range stageRefs(s.Args[k]) {
			if err := checkStageRef(s.Name, "args."+k, ref, done); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateStageModel checks the per-stage tier: a closed set, and only
// on the kinds that actually make a worker call. Rejecting it elsewhere
// rather than ignoring it is the point — a tier silently dropped on an
// agent stage would read as "I asked for lead and got worker", which is
// indistinguishable from a routing bug.
func validateStageModel(s PipelineStage) error {
	tier := strings.ToLower(strings.TrimSpace(s.Model))
	if tier == "" {
		return nil
	}
	if tier != "worker" && tier != "lead" {
		return Error("stage " + s.Name + ": model must be \"worker\" or \"lead\", got " + strconv.Quote(s.Model))
	}
	switch s.Kind {
	case StageWorker, StageSynthesize, "":
		return nil
	case StageFanout:
		if strings.TrimSpace(s.Agent) != "" {
			return Error("stage " + s.Name + ": model does not apply to a fanout that dispatches to an agent — the agent's own configuration decides its tier")
		}
		return nil
	case StageAgent:
		return Error("stage " + s.Name + ": model does not apply to an agent stage — the dispatched agent's own configuration decides its tier")
	case StageBranch:
		return Error("stage " + s.Name + ": a branch makes no LLM call, so model does not apply")
	case StageTool:
		return Error("stage " + s.Name + ": a tool stage makes no LLM call, so model does not apply")
	case StageLoop:
		return Error("stage " + s.Name + ": set model on the loop's BODY stages, not the loop itself")
	}
	return nil
}

// validateBranchStage checks a kind="branch" stage. stages/at locate it
// in its own list, which is what makes the forward-only jump checkable
// at save time rather than at run time.
func validateBranchStage(s PipelineStage, stages []PipelineStage, at int, done map[string]map[string]PipelineFieldType, inLoop bool) error {
	if len(s.Output) > 0 || len(s.Body) > 0 {
		return Error("stage " + s.Name + ": a branch makes no LLM call, so it has no output or body — it only reads an earlier stage's field")
	}
	ref := strings.TrimSpace(s.When)
	if ref == "" {
		return Error("stage " + s.Name + " is kind=branch but has no when (a \"NAME.field\" bool reference to an earlier stage)")
	}
	name, field := SplitStageRef(ref)
	if field == "" {
		return Error("stage " + s.Name + ": when must name a BOOL FIELD (e.g. \"frame.rejected\"), not just a stage")
	}
	if err := checkStageRef(s.Name, "when", ref, done); err != nil {
		return err
	}
	if t := done[name][field]; t != FieldBool {
		return Error("stage " + s.Name + ": when references " + ref + ", which is declared " + string(t) + ", not bool")
	}
	target := strings.TrimSpace(s.SkipTo)
	if target == "" {
		// Ending the pipeline from inside a loop body is ambiguous — stop
		// the pass, the loop, or the whole run? The loop already has a
		// purpose-built early exit, so point the author at it rather than
		// inventing an answer.
		if inLoop {
			return Error("stage " + s.Name + ": a branch inside a loop body cannot end the pipeline — use the loop's until to stop early, or set skip_to to jump within the body")
		}
		return nil
	}
	for i, other := range stages {
		if other.Name != target {
			continue
		}
		if i <= at {
			return Error("stage " + s.Name + ": skip_to " + target + " points backwards — a branch only jumps FORWARD. Repeating work is what kind=loop is for, where count bounds it.")
		}
		return nil
	}
	return Error("stage " + s.Name + ": skip_to names " + target + ", which is not a later stage in the same list")
}

// builtinTemplateTokens are the template names the interpreter resolves itself.
// An author who writes {stage:prev} has the right idea through the wrong door:
// prev is real, it is just not a stage. (reservedTemplateVars in the
// interpreter is the same set plus "stage", guarding a different seam — what a
// submit form may not redefine.)
var builtinTemplateTokens = map[string]bool{
	"input": true, "prev": true, "item": true, "iteration": true, "iterations": true,
}

// checkStageRef verifies one "NAME" or "NAME.field" reference resolves
// to an already-validated stage (and, for the field form, to a field
// that stage actually declares). where names the site of the reference
// so the error tells the author which part of the stage to fix.
func checkStageRef(stage, where, ref string, done map[string]map[string]PipelineFieldType) error {
	// A reference written as a PROMPT template — "{stage:decompose.items}" or
	// "{decompose.items}" — is the single most common way this fails, because
	// prompts really do use that form and the two sites look alike. Say which
	// form belongs here instead of reporting the braces as part of a stage name
	// nobody named that.
	if trimmed := strings.TrimSpace(ref); strings.ContainsAny(trimmed, "{}") {
		bare := strings.TrimSpace(strings.Trim(trimmed, "{}"))
		bare = strings.TrimPrefix(bare, "stage:")
		return Error("stage " + stage + " " + where + " is written as a prompt template (" + trimmed +
			"). It takes a BARE reference — " + strconv.Quote(bare) + " — not {…}. The {stage:NAME.field} form is for PROMPT text only.")
	}
	name, field := SplitStageRef(ref)
	// {stage:prev} and friends: the author reached for a real token through the
	// wrong door. Reporting "no stage named prev" is true and useless — prev
	// exists, it is just spelled {prev}.
	if builtinTemplateTokens[strings.ToLower(name)] && field == "" {
		return Error("stage " + stage + " " + where + " references " + strconv.Quote(name) +
			", which is a BUILT-IN, not a stage. Write {" + strings.ToLower(name) + "} — the {stage:NAME} form is only for a stage you named yourself.")
	}
	fields, ok := done[name]
	if !ok {
		// "unknown or later stage: X" named two possibilities and neither fix.
		// They are opposite fixes — rename, or reorder — and the reader cannot
		// choose between them without knowing that position in the array IS
		// execution order. So state the rule, not just the symptom.
		return Error("stage " + stage + " " + where + " references " + strconv.Quote(name) +
			", which has not run at that point. Either no stage is named that, or it is listed AFTER this one — " +
			"stages run in the order given and a reference only ever reaches BACKWARD, so a later stage has to be moved earlier in the array.")
	}
	if field == "" {
		return nil
	}
	if _, ok := fields[field]; !ok {
		if len(fields) == 0 {
			// The loop case, and the single most repeated failure in authoring:
			// reaching into a loop for the body stage that produced the result.
			// Body names hold a different value every pass, so they are not
			// addressable from outside — the loop answers under its OWN name.
			return Error("stage " + stage + " " + where + " references " + ref + ", but " + name +
				" declares no output fields. If " + name + " is a LOOP, read its result as {stage:" + name +
				"} — a loop's body stage names are not visible outside it (they hold a different value each pass), and collect:\"all\" makes that output every pass rather than the last.")
		}
		return Error("stage " + stage + " " + where + " references " + ref + ", but stage " + name + " declares no output field " + field)
	}
	return nil
}

// SplitStageRef splits a stage reference into its stage name and
// optional field. "plan" → ("plan", ""); "plan.queries" → ("plan",
// "queries"). Only the first dot separates — stage names can't contain
// one (Validate enforces that), so anything after it is the field.
func SplitStageRef(ref string) (name, field string) {
	ref = strings.TrimSpace(ref)
	if i := strings.Index(ref, "."); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

// stageRefs extracts the inner text of every {stage:...} occurrence in a
// template — "plan" from {stage:plan}, "plan.queries" from
// {stage:plan.queries}. Unterminated occurrences are ignored (they can't
// resolve at run time either, and they're left in the prompt verbatim).
func stageRefs(tmpl string) []string {
	const open = "{stage:"
	var out []string
	rest := tmpl
	for {
		i := strings.Index(rest, open)
		if i < 0 {
			return out
		}
		rest = rest[i+len(open):]
		j := strings.Index(rest, "}")
		if j < 0 {
			return out
		}
		out = append(out, strings.TrimSpace(rest[:j]))
		rest = rest[j+1:]
	}
}

// validateOutputFields checks one stage's declared output shape and
// returns its field-name → type map for later reference checking.
// nested guards the one-level depth limit.
func validateOutputFields(stage string, fields []PipelineField, nested bool) (map[string]PipelineFieldType, error) {
	out := make(map[string]PipelineFieldType, len(fields))
	for i, f := range fields {
		if f.Name == "" {
			return nil, Error("stage " + stage + ": output field " + strconv.Itoa(i+1) + " has no name")
		}
		if !isFieldName(f.Name) {
			return nil, Error("stage " + stage + ": output field " + f.Name + " must be lowercase letters, digits, or underscore")
		}
		if _, dup := out[f.Name]; dup {
			return nil, Error("stage " + stage + ": duplicate output field " + f.Name)
		}
		switch f.resolved() {
		case FieldString, FieldNumber, FieldBool, FieldList, FieldObject:
		default:
			// Name the valid set. Without it the author has to guess which
			// spelling of "a bunch of things" this vocabulary uses — and the
			// obvious guesses ("array", "string[]") each cost a round-trip,
			// after which the usual recovery is to abandon the declared output
			// entirely and hand-parse a JSON string, losing the field
			// addressing that fan_over and until depend on.
			return nil, Error("stage " + stage + ": output field " + f.Name + " has unknown type " + strconv.Quote(string(f.Type)) +
				" — use one of: string, number, bool, list, object. A list of items (sub-questions, findings, links) is type list, which is also what fan_over reads.")
		}
		if len(f.Fields) > 0 {
			if nested {
				return nil, Error("stage " + stage + ": output field " + f.Name + " nests too deep — one level only (deeper structure still renders as JSON, it just isn't addressable)")
			}
			if k := f.resolved(); k != FieldList && k != FieldObject {
				return nil, Error("stage " + stage + ": output field " + f.Name + " is type " + string(k) + " and cannot declare nested fields")
			}
			if _, err := validateOutputFields(stage, f.Fields, true); err != nil {
				return nil, err
			}
		}
		out[f.Name] = f.resolved()
	}
	return out, nil
}

// isFieldName reports whether s is a safe output-field handle:
// lowercase letters, digits, and underscore. Keeps {stage:X.field}
// unambiguous and the JSON keys predictable. Hand-rolled rather than a
// regexp — it's three comparisons and avoids a package-level compile.
func isFieldName(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return false
		}
	}
	return s != ""
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
	}
	if d.Created.IsZero() {
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

// ExportPipeline returns a portable copy of a pipeline def with
// storage/identity metadata stripped — the shareable recipe. Marshal
// the result to JSON for a downloadable artifact.
//
// ID is the ONE identity field that DOES travel (same rule as
// collections): agents reference pipelines by ID (AttachedPipelines),
// so preserving it is what lets an agent+pipeline bundle land with its
// wiring intact. Owner and timestamps are reassigned on import, never
// travel.
//
// Note on portability: a recipe whose stages are all kind=worker is
// fully self-contained. Agent stages reference an agent by id/name;
// the "pipeline" artifact type (orchestrate) normalizes those refs to
// agent NAMES on export and folds the agents into the bundle — this
// plain export doesn't rewrite them, so it's on the importer to have
// the referenced agents.
func ExportPipeline(d PipelineDef) PipelineDef {
	d.Owner = ""
	d.Created = time.Time{}
	d.Updated = time.Time{}
	d.Global = false // scope is a local decision; imported pipelines land non-global
	return d
}

// ImportPipeline takes a recipe (from ExportPipeline or an uploaded
// JSON file), assigns it to owner, validates it, and saves it to the
// user's store. Returns the saved def. The traveled ID is KEPT when
// it's free — it's what an agent in the same bundle references via
// AttachedPipelines — and reminted when it would collide, so importing
// the same recipe twice makes a copy instead of clobbering. Owner and
// timestamps are always the importer's.
func ImportPipeline(udb Database, owner string, recipe PipelineDef) (PipelineDef, error) {
	if recipe.ID != "" {
		if _, exists := LoadPipelineDef(udb, "", recipe.ID); exists {
			recipe.ID = ""
		}
	}
	recipe.Owner = owner
	recipe.Created = time.Time{}
	recipe.Updated = time.Time{}
	if err := recipe.Validate(); err != nil {
		return PipelineDef{}, err
	}
	return SavePipelineDef(udb, recipe), nil
}
