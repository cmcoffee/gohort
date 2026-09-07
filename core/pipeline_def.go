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
	"encoding/json"
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

	// StageMachine runs a stored MACHINE as a stage: its own phases, its
	// own blackboard, carried to completion, and its terminal step's
	// result becomes the stage's.
	//
	// The counterpart of the machine's pipeline phase, and the pair is
	// what makes the two primitives compose in both directions. It exists
	// for one shape in particular: a fanout body that runs a whole child
	// run per item. Research finds N gaps and fills each with a smaller
	// run of its own; a machine phase can only run ONE child, so without
	// this the fan is sequential.
	StageMachine PipelineStageKind = "machine"

	// StagePanel puts SEVERAL voices on the SAME question, in parallel,
	// optionally over several rounds where each round reads what the last
	// one said.
	//
	// The shape fanout and loop between them cannot express. Fanout is
	// breadth over DATA — one prompt, N items, each branch blind to the
	// others — and a loop is depth over TIME with one voice. A panel is
	// breadth over PERSPECTIVE on one question: the disagreement is the
	// product, which is why the voices have to see each other and why the
	// stage keeps every round rather than only the last.
	//
	// It composes rather than concludes. There is no built-in judge: a
	// panel emits a labeled transcript and the NEXT stage reads it, the
	// same way decompose → fanout → synthesize works. Baking the
	// synthesis in would make the one interesting decision — how
	// disagreement resolves — the framework's rather than the author's.
	StagePanel PipelineStageKind = "panel"
)

// loopMaxIterations bounds any single loop stage. A pipeline is
// unattended, and a loop whose Until never fires would otherwise burn
// LLM calls until the context is cancelled. Count is validated against
// this at save time, so the ceiling is a definition error rather than a
// run-time surprise.
// StageThinks reports whether a stage deliberates: its own setting, or the
// framework default (off) when it has none. THE accessor — nothing should read
// the fields directly, because there are two of them for one setting and only
// this knows which wins.
func StageThinks(s PipelineStage) bool { return StageThinkMode(s) == "on" }

// StageThinkMode is the stage's setting as the editor states it: "on", "off",
// or "" for inherit. Reads the legacy *bool when the string is unset, so a
// pipeline written before the change keeps the deliberation it was given.
func StageThinkMode(s PipelineStage) string {
	if m := strings.ToLower(strings.TrimSpace(s.ThinkMode)); m != "" {
		return m
	}
	if s.Think != nil {
		if *s.Think {
			return "on"
		}
		// Unreachable through the store — gob never gave a false pointer
		// back — but reachable from a legacy JSON recipe, which could
		// always carry think:false.
		return "off"
	}
	return ""
}

// normalizeStageThink folds the legacy field into the string one, in place and
// recursively, so everything downstream reads one field. Called on every load
// and every decode: a record migrates the first time it is touched, and is
// written back in the new shape the next time it is saved.
func normalizeStageThink(stages []PipelineStage) {
	for i := range stages {
		if stages[i].ThinkMode == "" && stages[i].Think != nil {
			stages[i].ThinkMode = StageThinkMode(stages[i])
		}
		stages[i].Think = nil
		normalizeStageThink(stages[i].Body)
	}
}

// UnmarshalJSON accepts the "think" key as either the string it is now or the
// boolean it used to be — an exported recipe outlives the field it was written
// against, and an import that failed on it would reject a pipeline that is
// perfectly good.
func (s *PipelineStage) UnmarshalJSON(data []byte) error {
	type alias PipelineStage // no methods, so no recursion
	aux := struct {
		Think json.RawMessage `json:"think"`
		*alias
	}{alias: (*alias)(s)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	// The shallower field wins the "think" key, so ThinkMode is untouched
	// above and set from the raw value here.
	s.ThinkMode = thinkModeFromJSON(aux.Think)
	return nil
}

// thinkModeFromJSON reads the two shapes "think" has ever had.
func thinkModeFromJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		switch m := strings.ToLower(strings.TrimSpace(asString)); m {
		case "on", "off":
			return m
		default:
			return ""
		}
	}
	var asBool bool
	if json.Unmarshal(raw, &asBool) == nil {
		if asBool {
			return "on"
		}
		return "off"
	}
	return ""
}

const loopMaxIterations = 25

// Panel bounds. Voices and rounds MULTIPLY — six voices over three rounds is
// eighteen model calls before anything has been synthesized — so both caps are
// deliberately small, and the stage says the multiplication out loud before it
// pays it. Eight is more voices than a person can read the disagreement
// between; four rounds is past where a debate stops moving.
const (
	panelMaxVoices = 8
	panelMaxRounds = 4
)

// PanelMaxVoices and PanelMaxRounds are the caps as an editor states them, so
// a form's own limits and the validator's cannot drift.
const (
	// Wired into the voices field's help text. It was exported for that and then
	// left unreferenced for a while, with the form stating "Two to eight" in
	// prose — the exact drift the export exists to prevent, sitting inside the
	// mechanism meant to prevent it.
	PanelMaxVoices = panelMaxVoices
	PanelMaxRounds = panelMaxRounds
)

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
	// Reach is the coarse tool scope: "" inherits everything the caller
	// has, "read" keeps only what reads, "none" keeps nothing.
	//
	// The control to prefer, and the same one a machine's step carries
	// (MachinePhase.Reach — the two authoring surfaces share a
	// vocabulary on purpose). Tools below names EXACT strings against a
	// catalog assembled per turn out of things that move under it: an
	// MCP server publishes its tools when it connects, a credential
	// mints its own per session, an attachment mints more per agent. A
	// pipeline is invoked by whatever agent attached it, so the catalog
	// it inherits is not even stable between two callers — which makes a
	// name list a description of one of them. A capability does not
	// move: "this stage may look, not act" is true for every caller.
	Reach string `json:"reach,omitempty"`
	// Tools optionally overrides what this stage's worker has access
	// to, BY NAME, on top of whatever Reach allowed. Empty (default) =
	// inherit the caller's tool catalog so a pipeline invoked from an
	// agent with web_search / fetch_url inherits those without any
	// per-stage configuration. Set explicitly to RESTRICT (e.g. a pure
	// synthesizer stage that should not be tempted to fetch) or to
	// OVERRIDE inherited tools for a specific stage. Only applies to
	// kind="worker" stages; agent stages get their dispatched agent's
	// full catalog regardless.
	Tools []string `json:"tools,omitempty"`
	// ThinkMode enables or disables deliberation for this stage: "on",
	// "off", or "" to inherit the framework default (off, cheap).
	//
	// Turn it ON for stages that genuinely benefit — synthesis reconciling
	// several sources, verification cross-referencing, decomposition
	// planning a split. Pure transforms, format conversions and cheap
	// paraphrases should leave it alone. Only applies to kind="worker"
	// stages; an agent stage honors its dispatched agent's own setting.
	//
	// A STRING, and that is the whole point of the field. It used to be a
	// *bool, which cannot express "off" through the store at all: gob
	// encodes a false pointer as nothing, so it decoded back as nil, and
	// "off" silently became "inherit" on the next read. An author picked
	// "Off — this stage is a transform", saved, reopened, and found it
	// unset, with the run behaving as though they had never chosen. The
	// same reason MachinePhase.Think is a string, and now the two agree.
	ThinkMode string `json:"think,omitempty"`

	// Think is the pre-string field, kept ONLY so records written before
	// the change still decode — gob matches on field NAME, so removing it
	// would fail the whole record rather than one field, and renaming it
	// would drop the "on" that older stages legitimately carry.
	//
	// Never written and never read directly: normalizeStageThink folds it
	// into ThinkMode on load and clears it. json:"-" because the wire
	// format has always been the "think" key, which ThinkMode now holds.
	//
	// Deprecated: read StageThinks(stage) instead.
	Think *bool `json:"-"`
	// Panel names the voices a kind="panel" stage puts on the question.
	//
	// An entry that resolves to one of your agents dispatches to it — its
	// persona, its memory, its tools. An entry that does not is a ROLE: the
	// worker answers as it, with the label substituted into the prompt as
	// {voice}. That mixture is deliberate. A panel of perspectives ("the
	// pessimist", "the customer") is the common case and must not require
	// authoring three agents first; a panel of real agents is what you reach
	// for when the perspectives need their own tools and memory.
	Panel []string `json:"panel,omitempty"`
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
	//
	// On a kind="panel" stage it is the number of ROUNDS: how many times
	// the voices answer, each round reading what the last one said. One
	// (the default) is a poll; two is the smallest thing that can be
	// called a debate, because nobody has replied to anybody until the
	// second.
	Count int `json:"count,omitempty"`

	// CountFrom makes Count a RUN-TIME value: a template resolved when the
	// stage starts, so a panel's rounds can come from the submit form
	// ("{rounds}") or from an earlier stage that decided how many the question
	// warrants ("{stage:plan.rounds}"), instead of being fixed when the
	// pipeline was written.
	//
	// It exists because the number of rounds is the one thing about a debate
	// that belongs to the QUESTION rather than to the recipe, and a definition
	// is written once for every question it will ever run.
	//
	// Count stays the fallback and still bounds the stage. A reference that
	// does not resolve (nobody filled the field), does not parse, or asks for
	// more than the ceiling falls back or clamps — and says so in the
	// transcript, because a stage that quietly ran a different number of times
	// than the reader asked for is worse than one that refused.
	//
	// Only panel and loop read a count at all. Anywhere else it is refused
	// rather than ignored.
	CountFrom string `json:"count_from,omitempty"`

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

	// Machine names the machine a kind=machine stage runs, by name or id.
	// The machine must be unattended: a stage has nobody waiting in it.
	Machine string `json:"machine,omitempty"`

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

	// Enum constrains a string field to a fixed set of values. Empty
	// means anything.
	//
	// It earns its place by removing hand-written copies of a list the
	// definition already knows. A machine's routing field is the case
	// that forced it: next_from names a field whose VALUE is the next
	// phase, and until now the allowed phases were prose in the field's
	// description — written by hand, drifting from the phase names, and
	// invisible to both the validator and the diagram. Declared instead,
	// one list generates the instruction, gets checked at save time, and
	// can be drawn.
	Enum []string `json:"enum,omitempty"`

	// From fills the field from a VARIABLE instead of asking the model
	// for it: "{original_input}", "{now}", "{state:triage.observation}".
	//
	// The value is already known, so asking a model to copy it across is
	// three ways worse than taking it — it costs tokens, it can be
	// paraphrased, and it can be left out. A field whose answer is "what
	// they originally asked" is not a judgement, and nothing that is not
	// a judgement should be a prompt.
	//
	// Filled AFTER the step runs and merged into its result, so it lands
	// on the blackboard exactly like a field the model answered and
	// everything downstream reads it the same way. It is left out of the
	// output contract entirely: the model is never shown a field it is
	// not being asked for.
	//
	// Honoured by both: a machine step fills from the built-in
	// vocabulary, a pipeline stage from {input} / {prev} /
	// {stage:NAME.field}. It lives on the shared field because it is the
	// same idea in both — the caller already has the value, so spending
	// a model on copying it across buys a paraphrase and a chance of
	// omission.
	From string `json:"from,omitempty"`
}

// resolved returns the field's effective type, defaulting empty to
// string so a half-filled authoring call still produces a usable field.
func (f PipelineField) resolved() PipelineFieldType {
	if f.Type == "" {
		return FieldString
	}
	return f.Type
}

// ModelOutput is what the model is actually asked for: everything the
// stage declares, minus the fields already filled from a variable.
//
// The same split a machine step makes, for the same two reasons: show a
// model a field it is not being asked for and it answers anyway, usually
// with a paraphrase of a value that was already correct; leave a filled
// field out of the RESULT and everything downstream loses it.
func (s PipelineStage) ModelOutput() []PipelineField {
	out := make([]PipelineField, 0, len(s.Output))
	for _, f := range s.Output {
		if strings.TrimSpace(f.From) == "" {
			out = append(out, f)
		}
	}
	return out
}

// StaticFields are the fields filled from a variable rather than asked
// for, in declared order.
func (s PipelineStage) StaticFields() []PipelineField {
	var out []PipelineField
	for _, f := range s.Output {
		if strings.TrimSpace(f.From) != "" {
			out = append(out, f)
		}
	}
	return out
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
	// SessionMeta promotes declared output fields onto a run's SUMMARY: the
	// sidebar row a PipelinePanel lists it as, alongside its title and date.
	// Each entry is a "stage.field" reference into a TOP-LEVEL stage's declared
	// Output, and the field's value is carried by its own name.
	//
	// It exists because a run history is browsed, not read. A list of titles
	// and timestamps answers "when did I run this"; what a reader actually
	// scans for is the ANSWER — which side won, how confident the judge was,
	// whether anything was flagged — and without this the only way to that is
	// opening runs one at a time until the right one turns up.
	//
	// Top-level stages only, for the same reason nothing after a loop may
	// reference its body by name: a body stage holds a different value every
	// pass, so promoting one would summarize a run by whatever the last
	// iteration happened to leave behind.
	SessionMeta []string `json:"session_meta,omitempty"`
	// Global scopes the pipeline to ALL of the owner's agents (minus any that
	// deny it via AgentRecord.DisabledPipelines), the way a global tool lives
	// in the user-wide pool. Off = available only to the agents that list its
	// ID in AttachedPipelines. Managed via the scope pill; a local decision,
	// so it's stripped on export (an imported pipeline lands non-global).
	Global  bool      `json:"global,omitempty"`
	Created time.Time `json:"created,omitempty"` // stripped on export
	Updated time.Time `json:"updated,omitempty"` // stripped on export

	// AllowedUsers is the peer-share recipient set: which OTHER users of this
	// deployment may read and run this pipeline. Empty = private to the owner,
	// which is what every pipeline was until now.
	//
	// The RECIPE travels; the authority does not. A recipient runs the owner's
	// definition against their OWN agents, tools and credentials, so a shared
	// pipeline grants a way of working rather than a way into the owner's
	// namespace — the same rule AgentRecord.AllowedUsers states for a shared
	// agent, and the reason sharing one is not a secret-disclosure decision.
	//
	// Editing the definition stays the owner's: a recipient reads, runs and
	// copies. Stripped on export, like Owner and Global — a recipient list is a
	// fact about THIS deployment's users and means nothing in another one.
	AllowedUsers []string `json:"allowed_users,omitempty"`

	// Previous is the definition this one replaced, kept so a wholesale
	// rewrite can be taken back. Exactly ONE deep, and set only by the
	// doors that REPLACE a pipeline rather than edit part of it (today:
	// describe-a-change). A stage form does not need it — the field is
	// right there — but a revision drafted from a paragraph can rewrite
	// every prompt in the pipeline, and prompts are the part somebody
	// actually wrote. Stripped on export. Same field, same reasoning, as
	// MachineDef.Previous.
	Previous *PipelineDef `json:"previous,omitempty"`
}
