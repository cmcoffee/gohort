// Pipeline interpreter — runs a declarative PipelineDef (the recipe)
// by executing its stages in order. This is the bridge between the
// data definition (pipeline_def.go) and the imperative runner
// (pipeline.go): RunPipelineDefAsync compiles a def into a
// PipelineWork and hands it to RunPipeline for lifecycle management.
//
// Layering note: worker stages are a plain WorkerChat call, which
// core can do itself. AGENT stages dispatch to a named agent, which
// lives in the app layer (orchestrate) — core can't import it. So the
// caller supplies a PipelineDispatch hook for agent stages; core
// drives everything else. Same inversion the agent loop uses for
// SubAgentRunner.

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// PipelineDispatch runs a single agent stage: dispatch `input` to the
// agent identified by `agentID` and return its reply. The app wires
// this to its agent runner (orchestrate's RunAgentSync), filling in
// owner/runtime-user context the interpreter doesn't carry. Nil is
// fine for pipelines with no agent stages; an agent stage with no
// dispatch hook is a run-time error.
type PipelineDispatch func(ctx context.Context, agentID, input string) (string, error)

// PipelineMachineRunner runs a stored MACHINE for a kind=machine stage and
// returns its terminal step's text plus that step's declared fields.
//
// A hook for the same reason PipelineDispatch is one: core executes the
// recipe and knows nothing about whose machines these are. The host
// resolves the reference against its own store, decides what the run may
// reach, and enforces its own nesting rules.
//
// Nil is not an error until a stage needs it, at which point the stage
// says so — the same posture a pipeline with agent stages and no dispatch
// hook already has.
type PipelineMachineRunner func(ctx context.Context, ref, input string) (string, map[string]any, error)

// PipelineHooks are the host-supplied halves of a run: what a stage needs
// that the interpreter cannot know by itself.
//
// A struct rather than four more parameters, because the entry points that
// took dispatch, then a status func, then a tool catalog were already at
// the edge of readable and a fourth would have tipped them over.
type PipelineHooks struct {
	Dispatch PipelineDispatch      // kind=agent
	Machine  PipelineMachineRunner // kind=machine
	Status   func(string)          // progress lines
	Tools    []AgentToolDef        // the inherited catalog a worker stage narrows
}

// RunPipelineDefSync executes a pipeline definition inline and returns
// the final stage's output. Use when the caller needs the result
// directly (an LLM tool call that runs a pipeline and uses the
// answer). For slow / fire-and-forget pipelines, prefer
// RunPipelineDefAsync. status (optional) receives a short line per
// stage for live progress. Worker stages run without tools (cheap
// LLM-only transforms). To let workers inherit the caller's tool
// catalog, use RunPipelineDefSyncWithTools.
// PipelineEvent is one unit of pipeline progress. It mirrors the block
// protocol core/ui's PipelinePanel already speaks (block → chunk → block_done,
// plus soft status lines), so a surface can render a pipeline run as a live
// per-stage transcript instead of a single progress line.
//
// This exists because the apps worth migrating onto pipelines — a debate, a
// deep-research run — are watched while they run. Their whole UI is "see which
// stage is working and what it produced". A pipeline that can only report
// "stage 3 starting" cannot replace them however expressive its stages are.
type PipelineEvent struct {
	Kind  string // "block" | "chunk" | "block_done" | "status"
	ID    string // stable block id within one run; empty for status
	Type  string // block type — the stage kind, so a surface can style per kind
	Title string // human label for the block
	Text  string // chunk body, or the status line
}

// PipelineSink receives progress events. Must tolerate being called from
// several goroutines: fanout branches run in parallel.
type PipelineSink func(PipelineEvent)

// statusSink adapts the plain status callback every existing caller passes.
// Block/chunk events are dropped — a caller that only wanted status lines
// keeps getting exactly those, so adding the richer protocol changes nothing
// for surfaces that haven't opted in.
func statusSink(status func(string)) PipelineSink {
	if status == nil {
		return nil
	}
	return func(ev PipelineEvent) {
		if ev.Kind == "status" {
			status(ev.Text)
		}
	}
}

// RunPipelineDefSyncWithSink is the streaming entry point: same execution as
// RunPipelineDefSync, but progress arrives as typed events rather than
// pre-formatted strings.
func (T *AppCore) RunPipelineDefSyncWithSink(ctx context.Context, def PipelineDef, input string, dispatch PipelineDispatch, sink PipelineSink, inheritedTools []AgentToolDef) (string, error) {
	return T.executePipelineDefSink(ctx, def, input, dispatch, sink, inheritedTools)
}

func (T *AppCore) RunPipelineDefSync(ctx context.Context, def PipelineDef, input string, dispatch PipelineDispatch, status func(string)) (string, error) {
	return T.executePipelineDef(ctx, def, input, dispatch, status, nil)
}

// RunPipelineDefSyncWithTools is the tools-aware variant. inheritedTools
// is the calling context's tool catalog (typically an agent's resolved
// worker pool). Worker stages whose Tools field is non-empty use those
// specific tools (overrides inherited). Worker stages with an empty
// Tools field fall back to inheritedTools — letting workers actually
// fetch/search via the calling agent's tools without per-stage config.
// When both are empty, the worker runs in the historic tool-less mode.
func (T *AppCore) RunPipelineDefSyncWithTools(ctx context.Context, def PipelineDef, input string, dispatch PipelineDispatch, status func(string), inheritedTools []AgentToolDef) (string, error) {
	return T.executePipelineDef(ctx, def, input, dispatch, status, inheritedTools)
}

// RunPipelineDefAsync compiles a pipeline definition into a
// PipelineWork and runs it through RunPipeline (queue, slot, session
// lifecycle, notification, cleanup). onResult, if set, receives the
// final output when the pipeline completes — the caller persists it /
// delivers it. Returns the pipeline ID immediately.
// RunPipelineDefSyncFields is RunPipelineDefSyncWithTools plus the declared
// fields behind the text it returns.
//
// A pipeline whose final stage declared an Output has already produced a
// validated, typed result; a caller that wants those values should not have
// to ask a model to read them back out of the text. That second call is what
// this exists to avoid (see the machine's pipeline phase, which otherwise
// pays for a shaping call to recover a shape the pipeline already had).
//
// Returns nil fields when the final stage declared nothing, which is not an
// error: a prose pipeline is a normal pipeline.
func (T *AppCore) RunPipelineDefSyncFields(ctx context.Context, def PipelineDef, input string, dispatch PipelineDispatch, status func(string), inheritedTools []AgentToolDef) (string, map[string]any, error) {
	out, r, err := T.executePipelineDefRun(ctx, def, input, nil, dispatch, statusSink(status), inheritedTools)
	if err != nil || r == nil {
		return out, nil, err
	}
	return out, r.outputs[r.lastStage].Fields, nil
}

func (T *AppCore) RunPipelineDefAsync(cfg PipelineConfig, def PipelineDef, input string, dispatch PipelineDispatch, onResult func(string)) string {
	return T.RunPipeline(cfg, func(ctx context.Context, pc *PipelineCtx) error {
		out, err := T.executePipelineDef(ctx, def, input, dispatch, pc.Status, nil)
		if err != nil {
			return err
		}
		if onResult != nil {
			onResult(out)
		}
		return nil
	})
}

// executePipelineDef is the stage loop shared by the sync + async
// entry points. Stages run in order; each output is captured under its
// name and made available to later stages via {stage:NAME} templating.
// Returns the final stage's output.
func (T *AppCore) executePipelineDef(ctx context.Context, def PipelineDef, input string, dispatch PipelineDispatch, status func(string), inheritedTools []AgentToolDef) (string, error) {
	return T.executePipelineDefSink(ctx, def, input, dispatch, statusSink(status), inheritedTools)
}

func (T *AppCore) executePipelineDefSink(ctx context.Context, def PipelineDef, input string, dispatch PipelineDispatch, sink PipelineSink, inheritedTools []AgentToolDef) (string, error) {
	return T.executePipelineDefVars(ctx, def, input, nil, dispatch, sink, inheritedTools)
}

// executePipelineDefVars is executePipelineDefSink plus RUN-scoped template
// values. Unexported and called from within core (the run surface), so the four
// exported Run* entry points keep the signatures they have.
func (T *AppCore) executePipelineDefVars(ctx context.Context, def PipelineDef, input string, vars map[string]string, dispatch PipelineDispatch, sink PipelineSink, inheritedTools []AgentToolDef) (string, error) {
	out, _, err := T.executePipelineDefRun(ctx, def, input, vars, dispatch, sink, inheritedTools)
	return out, err
}

// executePipelineDefRun is executePipelineDefVars plus the run itself, for
// the one caller that needs more than the final text: the fields behind it.
func (T *AppCore) executePipelineDefRun(ctx context.Context, def PipelineDef, input string, vars map[string]string, dispatch PipelineDispatch, sink PipelineSink, inheritedTools []AgentToolDef) (string, *pipelineRun, error) {
	return T.executePipelineHooks(ctx, def, input, vars, sink, PipelineHooks{Dispatch: dispatch, Tools: inheritedTools})
}

// RunPipelineDefHooks executes a pipeline with every host-supplied half in
// one place, and returns the final text plus the fields behind it.
//
// The entry point to reach for when a pipeline may contain a kind=machine
// stage; the older Run* signatures have no way to carry that hook and a
// machine stage refuses cleanly under them.
func (T *AppCore) RunPipelineDefHooks(ctx context.Context, def PipelineDef, input string, h PipelineHooks) (string, map[string]any, error) {
	out, r, err := T.executePipelineHooks(ctx, def, input, nil, statusSink(h.Status), h)
	if err != nil || r == nil {
		return out, nil, err
	}
	return out, r.outputs[r.lastStage].Fields, nil
}

func (T *AppCore) executePipelineHooks(ctx context.Context, def PipelineDef, input string, vars map[string]string, sink PipelineSink, h PipelineHooks) (string, *pipelineRun, error) {
	if err := def.Validate(); err != nil {
		return "", nil, err
	}
	r := &pipelineRun{
		app:       T,
		input:     input,
		dispatch:  h.Dispatch,
		machines:  h.Machine,
		sink:      sink,
		inherited: h.Tools,
		vars:      vars,
		outputs:   make(map[string]stageOutput, len(def.Stages)),
		blockSeq:  new(atomic.Int64),
	}
	out, _, err := r.runList(ctx, def.Stages, input, "Stage")
	return out, r, err
}

// pipelineRun is one execution of a pipeline definition — the state every
// stage needs, gathered so the stage runner isn't an eight-argument
// function. Created by executePipelineDef and threaded through loop
// bodies unchanged, which is what lets a body stage read outer stages.
type pipelineRun struct {
	app       *AppCore
	input     string
	dispatch  PipelineDispatch
	machines  PipelineMachineRunner
	sink      PipelineSink
	inherited []AgentToolDef
	// vars are RUN-scoped template values — the submit form's fields, keyed
	// "{name}". Kept on the run rather than threaded through runList because a
	// loop body builds its own vars ({iteration}) and passes those down: a
	// threaded value would be visible everywhere EXCEPT inside a loop, which is
	// where a refinement pipeline does its work.
	vars      map[string]string
	outputs   map[string]stageOutput
	lastStage string // the stage whose output the run is returning
	// blockSeq hands out block ids. A POINTER, because a fanout body runs
	// its branches in child scopes (forBranch) and each one would otherwise
	// start its own numbering: two branches would both claim "stage-1" and
	// the transcript would fold their blocks together.
	blockSeq *atomic.Int64
	// quiet suppresses this run's own transcript blocks. Set on a fanout
	// branch: N branches x K body stages of individual blocks would bury a
	// transcript that reads one entry per stage, and the fanout's joined
	// block is the artifact somebody wants. Branch PROGRESS still emits, as
	// status.
	quiet bool
}

// forBranch is one fanout branch's scope.
//
// Its outputs are its OWN, seeded with a copy of what the parent had
// established when the fan started. A loop can share the parent's map
// because its passes are sequential; branches run in parallel, so a shared
// map is both a data race and a semantic collision — branch 3 writing
// "analyze" while branch 1 is still reading its own.
//
// Everything else is shared on purpose: the sink (one transcript), the id
// space (see blockSeq), the dispatch hook, and the inherited tools.
func (r *pipelineRun) forBranch() *pipelineRun {
	outs := make(map[string]stageOutput, len(r.outputs)+4)
	for k, v := range r.outputs {
		outs[k] = v
	}
	return &pipelineRun{
		app:       r.app,
		input:     r.input,
		dispatch:  r.dispatch,
		machines:  r.machines,
		sink:      r.sink,
		inherited: r.inherited,
		vars:      r.vars,
		outputs:   outs,
		blockSeq:  r.blockSeq,
		quiet:     true,
	}
}

// status emits a soft progress line. Kept as a method so the interpreter's
// existing r.status(...) calls read unchanged.
func (r *pipelineRun) status(text string) {
	r.emit(PipelineEvent{Kind: "status", Text: text})
}

// emit delivers one event, if anyone is listening.
func (r *pipelineRun) emit(ev PipelineEvent) {
	if r == nil || r.sink == nil {
		return
	}
	r.sink(ev)
}

// openBlock announces a stage and returns its id plus the closer. One block
// per stage execution — a loop body stage opens a fresh block per pass, which
// is what makes a loop legible in the transcript rather than one card that
// silently rewrites itself.
func (r *pipelineRun) openBlock(stage PipelineStage, title string) (string, func(body string)) {
	if r == nil || r.sink == nil || r.quiet {
		return "", func(string) {}
	}
	id := fmt.Sprintf("stage-%d", r.blockSeq.Add(1))
	r.emit(PipelineEvent{Kind: "block", ID: id, Type: string(stage.Kind), Title: title})
	return id, func(body string) {
		if body != "" {
			r.emit(PipelineEvent{Kind: "chunk", ID: id, Text: body})
		}
		r.emit(PipelineEvent{Kind: "block_done", ID: id})
	}
}

// transcriptBody renders what a stage produced for a HUMAN reading the run.
//
// A stage that declares output fields returns JSON — that is the point, it is
// what {stage:NAME.field}, fan_over, and until all read. But the transcript is
// not a data path, and dumping the envelope there showed the reader a wall of
// braces and quoted strings where the answer should be: the first card of a
// research run displayed its four sub-questions as a JSON object, which reads
// as a bug in the app even though the pipeline is working perfectly.
//
// So the JSON is rendered, not replaced. `raw` still goes to every consumer
// unchanged; only the card body differs, and only for declared-output stages.
// Free-text stages are passed through untouched.
//
// Field ORDER follows the declaration, never the map: two runs of the same
// pipeline should read identically, and Go's map iteration guarantees they
// wouldn't.
func transcriptBody(stage PipelineStage, raw string, fields map[string]any) string {
	if len(fields) == 0 || len(stage.Output) == 0 {
		return raw
	}
	// One field carries the whole stage — a synthesized answer, a verdict. Its
	// name is the stage's job, already on the card as the title, so a label
	// above it says nothing twice.
	single := len(stage.Output) == 1
	var b strings.Builder
	for _, f := range stage.Output {
		v, ok := fields[f.Name]
		if !ok {
			continue
		}
		body := readableFieldValue(v)
		if body == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		if !single {
			b.WriteString(displayFromSnake(f.Name))
			b.WriteString("\n")
		}
		b.WriteString(body)
	}
	if b.Len() == 0 {
		return raw // nothing rendered — better the envelope than an empty card
	}
	return b.String()
}

// readableFieldValue turns one declared field's value into lines a reader can
// scan. A list becomes bullets (the shape it almost always is: sub-questions,
// findings, sources); a scalar becomes its own text; anything structured falls
// back to indented JSON, which is still better than a single-line blob.
func readableFieldValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case bool:
		if t {
			return "yes"
		}
		return "no"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []any:
		var lines []string
		for _, item := range t {
			s := readableFieldValue(item)
			if s == "" {
				continue
			}
			// A nested structure inside a list keeps its own shape rather than
			// being flattened onto a bullet that would run off the line.
			if strings.Contains(s, "\n") {
				lines = append(lines, "- "+strings.ReplaceAll(s, "\n", "\n  "))
				continue
			}
			lines = append(lines, "- "+s)
		}
		return strings.Join(lines, "\n")
	default:
		if b, err := json.MarshalIndent(v, "", "  "); err == nil {
			return string(b)
		}
		return fmt.Sprint(v)
	}
}

// displayFromSnake turns a declared field name into a label: "sub_questions" →
// "Sub questions". The inverse of SnakeFromDisplay, for the one direction that
// needed it.
func displayFromSnake(name string) string {
	s := strings.TrimSpace(strings.ReplaceAll(name, "_", " "))
	if s == "" {
		return name
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// reservedTemplateVars are the tokens the interpreter owns. A submit field
// named "input" or "iteration" must not be able to redefine the template
// language out from under a pipeline that was authored against it — the form is
// the app author's, the vocabulary is the framework's.
var reservedTemplateVars = map[string]bool{
	"input": true, "prev": true, "item": true,
	"iteration": true, "iterations": true, "stage": true,
}

// applyRunVars substitutes the run's form values. Applied LAST, so a built-in
// resolves first and a collision can only ever fail to substitute, never
// silently take over.
func (r *pipelineRun) applyRunVars(s string) string {
	for k, v := range r.vars {
		s = strings.ReplaceAll(s, k, v)
	}
	return s
}

// runList executes a stage list in order and returns the last stage's
// output. label prefixes the progress lines ("Stage" at the top level,
// "  Stage" inside a loop pass) so the activity pane reads as a tree.
// extra carries per-pass template values such as {iteration}.
// A branch is control flow, so it lives HERE rather than in runStage:
// only the walk can skip ahead or end the run. runStage stays "execute
// one stage", which is what lets a loop body reuse it unchanged.
func (r *pipelineRun) runList(ctx context.Context, stages []PipelineStage, prev, label string, extra ...map[string]string) (string, bool, error) {
	var vars map[string]string
	if len(extra) > 0 {
		vars = extra[0]
	}
	for i := 0; i < len(stages); i++ {
		if ctx.Err() != nil {
			return "", false, ctx.Err()
		}
		stage := stages[i]
		if stage.Kind == StageBranch {
			taken := r.branchTaken(stage)
			if !taken {
				continue
			}
			if strings.TrimSpace(stage.SkipTo) == "" {
				r.status(fmt.Sprintf("%s: %s is true — ending the pipeline here", stage.Name, stage.When))
				return prev, true, nil
			}
			target := indexOfStage(stages, stage.SkipTo)
			if target < 0 {
				// Validate proved this exists; defensive only.
				return "", false, Error("stage " + stage.Name + ": skip_to target " + stage.SkipTo + " not found")
			}
			r.status(fmt.Sprintf("%s: %s is true — skipping ahead to %s", stage.Name, stage.When, stage.SkipTo))
			i = target - 1 // the loop's own increment lands on the target
			continue
		}
		out, err := r.runStage(ctx, stage, prev, fmt.Sprintf("%s %d/%d: %s", label, i+1, len(stages), stage.Name), vars)
		if err != nil {
			return "", false, err
		}
		prev = out
	}
	return prev, false, nil
}

// branchTaken evaluates a branch's condition. A missing or non-bool
// value reads as FALSE — fall through and run the stages. That is the
// safe direction: the alternative is skipping real work because a field
// didn't parse, and Validate already proved the reference is a declared
// bool, so this only covers a stage that never ran (skipped by an
// earlier branch).
func (r *pipelineRun) branchTaken(stage PipelineStage) bool {
	name, field := SplitStageRef(stage.When)
	src, ok := r.outputs[name]
	if !ok {
		return false
	}
	v, _ := src.Fields[field].(bool)
	return v
}

// indexOfStage returns the position of a stage by name, or -1.
func indexOfStage(stages []PipelineStage, name string) int {
	for i, s := range stages {
		if s.Name == name {
			return i
		}
	}
	return -1
}

// runStage executes ONE stage: resolve its tools and prompt, run it,
// record its output under its name, and report progress. Extracted from
// the old inline loop so a loop body runs through exactly the same path
// the top level does — the alternative was a second copy of the kind
// switch, which would have drifted.
func (r *pipelineRun) runStage(ctx context.Context, stage PipelineStage, prev, stageLabel string, vars map[string]string) (string, error) {
	T, dispatch, status, outputs := r.app, r.dispatch, r.status, r.outputs
	input := r.input
	inheritedTools := r.inherited
	{
		// Emit start event with stage kind so the activity pane reader
		// can tell at a glance whether a slow stage is a worker LLM
		// call (cheap) or an agent dispatch (full sub-agent run). When
		// the worker stage has tools (inherited or explicit), tag the
		// kind so the operator can see at a glance which stages do
		// real I/O vs pure transforms.
		kindLabel := string(stage.Kind)
		if kindLabel == "" {
			kindLabel = "worker"
		}
		if stage.Kind == StageAgent {
			kindLabel = "agent → " + stage.Agent
		}
		// Resolve which tools (if any) this stage's worker sees. Stage's
		// own Tools field wins over inherited; both empty = tool-less
		// worker. Filter inheritedTools by name when stage.Tools is set
		// so a stage can be permissive (no Tools = inherit everything)
		// OR restrictive (specific names from the inherited pool).
		var stageTools []AgentToolDef
		if stage.Kind == StageWorker || stage.Kind == StageSynthesize || stage.Kind == StageTool ||
			(stage.Kind == StageFanout && strings.TrimSpace(stage.Agent) == "") {
			stageTools = StageTools(stage, inheritedTools)
			if len(stageTools) > 0 {
				names := make([]string, 0, len(stageTools))
				for _, td := range stageTools {
					names = append(names, td.Tool.Name)
				}
				kindLabel += " tools=[" + strings.Join(names, ",") + "]"
			}
		}
		if status != nil && stage.Kind != StageTool {
			// A tool stage reports its own start once the tool name is
			// resolved into the label.
			status(stageLabel + " [" + kindLabel + "] starting")
		}
		// Open this stage's transcript block. A surface renders one card per
		// stage from here; the closer below carries what the stage produced.
		// Opened before the prompt resolves so a slow stage shows as working
		// rather than as nothing.
		_, closeBlock := r.openBlock(stage, stage.Name)
		prompt := resolveStageTemplate(stage.Prompt, input, prev, outputs)
		for k, v := range vars {
			prompt = strings.ReplaceAll(prompt, k, v)
		}
		prompt = r.applyRunVars(prompt)

		stageStart := time.Now()
		var out string
		var fields map[string]any
		var err error
		// call runs the stage's underlying LLM work once against a
		// resolved prompt. Declared-output stages need to run it more
		// than once (the repair retry), so the two kinds that can carry
		// an Output contract hand it over as a closure instead of
		// executing inline. Fanout has no single call to wrap.
		var call func(string) (string, error)
		switch stage.Kind {
		case StageWorker, StageSynthesize:
			// Resolve per-stage think: nil = default off, &true = on,
			// &false = explicitly off (same as default but
			// self-documenting).
			think := StageThinks(stage)
			// JSON mode only helps the tool-less path — see runWorkerStage.
			jsonMode := len(stage.ModelOutput()) > 0
			tier := stageTier(stage)
			if tier == LEAD {
				kindLabel += " model=lead"
			}
			call = func(p string) (string, error) {
				return T.runWorkerStage(ctx, p, stageTools, think, jsonMode, tier)
			}
		case StageAgent:
			if dispatch == nil {
				return "", Error("stage " + stage.Name + ": agent stage but no dispatch hook provided")
			}
			// The set does NOT travel: this stage hands the work to an agent
			// with rules of its own, and judging its actions by the caller's
			// is not what either owner authored.
			agentCtx := WithoutStageGuardrails(ctx)
			call = func(p string) (string, error) { return dispatch(agentCtx, stage.Agent, p) }
		case StageFanout:
			// Run the stage's inner work across each element of the
			// FanOver stage's JSON-array output, in parallel, then collect
			// into one labeled block for the next stage to consume. With a
			// body, each branch is a small pipeline in its own scope and
			// the fanout also carries the branches' declared shapes.
			out, fields, err = r.runFanoutStage(ctx, stage, prev, stageTools, status)
		case StagePanel:
			// Several voices on the same question, in parallel, over as many
			// rounds as the stage asks for — each round reading the last.
			out, fields, err = r.runPanelStage(ctx, stage, prev, stageTools, status)
		case StageLoop:
			// Repeat the body, threading each pass's result into the next.
			out, err = r.runLoopStage(ctx, stage, prev)
		case StageMachine:
			// A whole run as a stage. Computed here rather than handed over
			// as a `call` closure on purpose: runDeclaredStage repairs a
			// shape mismatch by calling again, and calling again here would
			// re-run the entire machine.
			out, fields, err = r.runMachineStage(ctx, stage, prompt)
		case StageTool:
			// Call the named tool with the author's arguments. No model
			// runs, so there is nothing to prompt and nothing to repair.
			kindLabel = "tool → " + stage.Tool
			if status != nil {
				status(stageLabel + " [" + kindLabel + "] starting")
			}
			out, err = r.runToolStage(ctx, stage, prev, stageTools, vars)
			if err == nil && len(stage.Output) > 0 {
				// A tool that returns JSON can declare its shape like any
				// other stage — decode it, but with no repair retry: there
				// is no model to ask again, so a mismatch is the tool's
				// contract being wrong, not a bad generation.
				fields, err = decodeStageOutput(out, stage.ModelOutput())
				if err != nil {
					err = Error("tool returned a result that does not match the declared output: " + err.Error())
				}
			}
		default:
			return "", Error("stage " + stage.Name + ": unknown kind " + string(stage.Kind))
		}
		if call != nil {
			if len(stage.ModelOutput()) > 0 {
				out, fields, err = T.runDeclaredStage(ctx, stage, prompt, call, status)
			} else {
				out, err = call(prompt)
			}
		}
		elapsed := time.Since(stageStart).Round(time.Millisecond * 100)
		if err != nil {
			if status != nil {
				status(stageLabel + " failed after " + elapsed.String() + ": " + err.Error())
			}
			// Close the block on the way out: a surface that leaves a card
			// spinning forever after a failed stage reads as a hung run.
			closeBlock("failed after " + elapsed.String() + ": " + err.Error())
			return "", fmt.Errorf("stage %q: %w", stage.Name, err)
		}
		out = strings.TrimSpace(out)
		// Fields the stage takes from a variable rather than asking for.
		// Filled AFTER the call and merged in, so they land in outputs
		// exactly like answered ones and every {stage:NAME.field} reads
		// them the same way. A value the pipeline already holds is not
		// worth a model's attention, and asking for it invites a
		// paraphrase of something that was already right.
		if static := stage.StaticFields(); len(static) > 0 {
			if fields == nil {
				fields = map[string]any{}
			}
			for _, f := range static {
				filled := resolveStageTemplate(f.From, input, prev, outputs)
				for k, v := range vars {
					filled = strings.ReplaceAll(filled, k, v)
				}
				fields[f.Name] = strings.TrimSpace(r.applyRunVars(filled))
			}
		}
		outputs[stage.Name] = stageOutput{Text: out, Fields: fields}
		// Which stage the run's returned text belongs to. A caller that
		// wants the FIELDS behind that text (a machine phase taking a
		// pipeline's declared shape as its own) cannot work it out from
		// def.Stages: a branch may have skipped the tail, so the last
		// stage in the list is not always the last one to run.
		r.lastStage = stage.Name
		// The transcript gets a READABLE rendering; `out` — the JSON a declared
		// stage produces — stays exactly as it is for everything downstream.
		closeBlock(transcriptBody(stage, out, fields))
		if status != nil {
			// Tail preview lets the user see WHAT the stage produced
			// without having to wait for the whole pipeline to finish.
			// 120 chars is enough to recognize the shape (decompose
			// produced a question list, investigate produced bullet
			// points, etc.) without spamming the activity pane.
			preview := strings.Join(strings.Fields(out), " ")
			if len(preview) > 120 {
				preview = preview[:120] + "…"
			}
			if preview == "" {
				preview = "(empty)"
			}
			status(fmt.Sprintf("%s done in %s (%d chars): %s", stageLabel, elapsed, len(out), preview))
		}
		return out, nil
	}
}

// stageOutput is one completed stage's result. Text is always populated
// and is what {stage:NAME} and {prev} render — for a declared-output
// stage that's the raw JSON reply. Fields is populated only when the
// stage declared an Output contract, and is what {stage:NAME.field}
// reads. Keeping both means adding structure took nothing away: a
// downstream stage can still consume the whole reply as text.
type stageOutput struct {
	Text   string
	Fields map[string]any
}

// runDeclaredStage runs a stage that declared an Output contract:
// append the contract to the prompt, run, decode, and on a shape
// mismatch retry ONCE with the failure fed back before giving up.
//
// This is the one place in the pipeline layer that fails closed rather
// than degrading. A stage that promised a shape and didn't deliver it
// breaks every downstream {stage:X.field} anyway, and the fail-open
// alternative is a prompt carrying the literal text "{stage:plan.queries}"
// into an LLM call three stages later. Failing at the source is the
// debuggable behavior — and the breadcrumb rule still holds: both the
// repair attempt and the final failure land on the status line.
func (T *AppCore) runDeclaredStage(ctx context.Context, stage PipelineStage, prompt string, call func(string) (string, error), status func(string)) (string, map[string]any, error) {
	return T.runDeclaredOutput(ctx, "stage "+stage.Name, stage.ModelOutput(), prompt, call, status)
}

// runDeclaredOutput is runDeclaredStage's body, with the stage removed.
// A machine phase declares its handoff with the same PipelineField
// vocabulary and wants the same contract → decode → one repair → fail
// behavior (see AdvanceMachine), and the alternative to sharing this was
// a second copy of the repair loop that would drift from this one.
//
// label identifies the caller in status text and reads as a noun phrase
// ("stage plan", "phase route") because it lands mid-sentence.
func (T *AppCore) runDeclaredOutput(ctx context.Context, label string, decl []PipelineField, prompt string, call func(string) (string, error), status func(string)) (string, map[string]any, error) {
	contract := renderOutputContract(decl)
	out, err := call(prompt + contract)
	if err != nil {
		return "", nil, err
	}
	fields, derr := decodeStageOutput(out, decl)
	if derr == nil {
		return out, fields, nil
	}
	if status != nil {
		status(label + ": reply did not match the declared shape (" + derr.Error() + ") — retrying once")
	}
	if ctx.Err() != nil {
		return "", nil, ctx.Err()
	}
	repair := prompt + contract +
		"\n\nYour previous reply could not be used: " + derr.Error() +
		"\nIt was:\n" + previewForRepair(out) +
		"\nReply again with ONLY the JSON object described above."
	out, err = call(repair)
	if err != nil {
		return "", nil, err
	}
	if fields, derr = decodeStageOutput(out, decl); derr != nil {
		return "", nil, Error("reply did not match the declared shape after one repair attempt: " + derr.Error())
	}
	return out, fields, nil
}

// previewForRepair bounds how much of a failed reply is quoted back in
// the repair prompt. Enough to show the model what it did wrong,
// not so much that a runaway reply doubles the stage's token cost.
func previewForRepair(s string) string {
	const max = 600
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…(truncated)"
}

// renderOutputContract turns a stage's declared fields into the prompt
// instruction that asks for them. Fields render in DECLARED order, not
// map order, so the same def produces a byte-identical prompt on every
// run — a prompt that reshuffles itself never hits the cache.
func renderOutputContract(fields []PipelineField) string {
	var b strings.Builder
	b.WriteString("\n\nReply with a single JSON object and nothing else, using these keys:\n")
	writeContractFields(&b, fields, "")
	b.WriteString("\nNo prose, no markdown, no code fences — the JSON object only.")
	return b.String()
}

func writeContractFields(b *strings.Builder, fields []PipelineField, indent string) {
	for _, f := range fields {
		b.WriteString(indent + "- \"" + f.Name + "\" (" + string(f.resolved()))
		if f.Required {
			b.WriteString(", required")
		} else {
			b.WriteString(", optional")
		}
		b.WriteString(")")
		// A one-line description reads after the colon. A multi-line one
		// is an INSTRUCTION for this field, and crushing it onto the same
		// line is how a directive stops looking like one — to a reader
		// and to the model.
		if d := strings.TrimSpace(f.Desc); d != "" {
			if strings.Contains(d, "\n") {
				b.WriteString("\n")
				for _, line := range strings.Split(d, "\n") {
					if line = strings.TrimSpace(line); line != "" {
						b.WriteString(indent + "  " + line + "\n")
					}
				}
				b.WriteString(strings.TrimSuffix(indent, " "))
			} else {
				b.WriteString(": " + d)
			}
		}
		// The allowed values, from the declaration rather than from prose
		// somebody kept in step by hand. Stated after the description so
		// the description can say what the field MEANS while this says
		// what may go in it.
		if len(f.Enum) > 0 {
			b.WriteString(" — exactly one of: " + strings.Join(f.Enum, ", "))
		}
		b.WriteString("\n")
		if len(f.Fields) > 0 {
			if f.resolved() == FieldList {
				b.WriteString(indent + "  each element is an object with:\n")
			} else {
				b.WriteString(indent + "  an object with:\n")
			}
			writeContractFields(b, f.Fields, indent+"    ")
		}
	}
}

// decodeStageOutput parses a stage reply against its declared fields.
// DecodeJSON does the tolerant extraction (code fences, surrounding
// prose, trailing commas, stray comments — the three things local
// models actually get wrong); this layer handles presence and type.
//
// Coercion is deliberately lenient in the obvious directions — a number
// returned as "42", a single string where a list was asked for. Same
// posture as tool_def's parameter coercion: reshape what the model
// plainly meant instead of bouncing the whole stage over a quoting
// choice. What it will NOT do is invent a missing required field.
func decodeStageOutput(text string, decl []PipelineField) (map[string]any, error) {
	var raw map[string]any
	if err := DecodeJSON(text, &raw); err != nil {
		return nil, Error("reply was not a JSON object")
	}
	out := make(map[string]any, len(decl))
	for _, f := range decl {
		v, present := raw[f.Name]
		if !present || v == nil {
			if f.Required {
				return nil, Error("missing required field \"" + f.Name + "\"")
			}
			out[f.Name] = zeroForFieldType(f.resolved())
			continue
		}
		cv, err := coerceField(f.resolved(), v)
		if err != nil {
			return nil, Error("field \"" + f.Name + "\": " + err.Error())
		}
		// A declared set is checked HERE, where a mismatch is still
		// repairable — runDeclaredOutput retries once with the error. A
		// routing field carrying a phase that does not exist would
		// otherwise fall back silently and route somewhere nobody chose.
		if len(f.Enum) > 0 {
			if str, ok := cv.(string); ok && !containsFold(f.Enum, str) {
				return nil, Error("field \"" + f.Name + "\": " + strconv.Quote(str) +
					" is not one of: " + strings.Join(f.Enum, ", "))
			}
		}
		out[f.Name] = cv
	}
	return out, nil
}

// coerceField reshapes one decoded value to its declared type.
func coerceField(t PipelineFieldType, v any) (any, error) {
	switch t {
	case FieldString:
		if s, ok := v.(string); ok {
			return s, nil
		}
		// A structure where a string was asked for still carries the
		// information — hand it over as JSON rather than losing it.
		return renderFieldValue(v), nil
	case FieldNumber:
		switch n := v.(type) {
		case float64:
			return n, nil
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
			if err != nil {
				return nil, Error("expected a number, got " + strconv.Quote(n))
			}
			return f, nil
		}
		return nil, Error("expected a number")
	case FieldBool:
		switch b := v.(type) {
		case bool:
			return b, nil
		case float64:
			return b != 0, nil
		case string:
			switch strings.ToLower(strings.TrimSpace(b)) {
			case "true", "yes", "y", "1":
				return true, nil
			case "false", "no", "n", "0":
				return false, nil
			}
			return nil, Error("expected true or false, got " + strconv.Quote(b))
		}
		return nil, Error("expected true or false")
	case FieldList:
		switch l := v.(type) {
		case []any:
			return l, nil
		case string:
			// Models hand back a list as a JSON string more often than
			// they should; and a bare string is a one-element list.
			var inner []any
			if DecodeJSON(l, &inner) == nil {
				return inner, nil
			}
			return []any{l}, nil
		}
		return nil, Error("expected a list")
	case FieldObject:
		switch o := v.(type) {
		case map[string]any:
			return o, nil
		case string:
			var inner map[string]any
			if DecodeJSON(o, &inner) == nil {
				return inner, nil
			}
		}
		return nil, Error("expected an object")
	}
	return v, nil
}

// zeroForFieldType is what an absent optional field resolves to, so a
// downstream {stage:X.field} renders as empty rather than as the
// literal placeholder text.
func zeroForFieldType(t PipelineFieldType) any {
	switch t {
	case FieldNumber:
		return float64(0)
	case FieldBool:
		return false
	case FieldList:
		return []any{}
	case FieldObject:
		return map[string]any{}
	}
	return ""
}

// renderFieldValue turns a decoded field back into prompt text.
// Scalars render bare; lists and objects render as compact JSON, which
// keeps them re-parseable by DecodeJSONList downstream and avoids
// inventing a second list encoding nobody else reads.
func renderFieldValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		// JSON has one number type, so a count comes back as 3.0 —
		// render it as "3" so a templated prompt doesn't read oddly.
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// runWorkerStage runs a single worker-tier LLM call with the resolved
// prompt. When tools is empty, this is the original prompt-in-text-out
// cheap path — one LLM completion, no dispatch loop, no persona. When
// tools is non-empty (typically inherited from the calling agent's
// catalog OR set explicitly on the stage), runs a focused agent loop
// with those tools so the worker can actually search / fetch / call
// APIs as part of its stage's job. think controls whether the LLM
// gets a deliberation budget — default false (cheap), set true on
// synthesis / verification / decomposition stages that benefit from
// reasoning.
//
// jsonMode asks the backend to constrain the reply to a JSON object
// (declared-output stages). It applies ONLY on the tool-less path:
// response_format alongside tool definitions is inconsistently
// supported across backends, and a tool-equipped structured stage has
// the prompt contract, DecodeJSON's tolerance, and the repair retry to
// fall back on — three softer mechanisms beat one that may 400.
func (T *AppCore) runWorkerStage(ctx context.Context, prompt string, tools []AgentToolDef, think, jsonMode bool, tier LLMTier) (string, error) {
	return T.runWorkerStageConfirm(ctx, prompt, tools, think, jsonMode, tier, nil)
}

// runWorkerStageConfirm is runWorkerStage with the caller's approval
// hook. nil keeps the unattended default (every call allowed), which is
// what a pipeline stage wants: it runs with nobody watching, so a
// prompt for approval would hang forever.
//
// A caller with somebody watching MUST pass its own. A machine's step
// runs inside a live turn, and a step reaching a tool the turn itself
// would have stopped to ask about is a hole under the approval card,
// not a shortcut.
func (T *AppCore) runWorkerStageConfirm(ctx context.Context, prompt string, tools []AgentToolDef, think, jsonMode bool, tier LLMTier, confirm func(name, args string) bool) (string, error) {
	if len(tools) == 0 {
		// Cheap path — pure LLM transform.
		opts := []ChatOption{WithThink(think)}
		if jsonMode {
			opts = append(opts, WithJSONMode())
		}
		chat := T.WorkerChat
		if tier == LEAD {
			// LeadChat already degrades to worker when no separate lead
			// is configured, so this needs no availability check.
			chat = T.LeadChat
		}
		resp, err := chat(ctx, []Message{{Role: "user", Content: prompt}}, opts...)
		if err != nil {
			return "", err
		}
		if resp == nil {
			return "", Error("worker returned no response")
		}
		return resp.Content, nil
	}
	// Tool-equipped path — short focused agent loop. No persona
	// (just the stage's prompt as the user turn), small round budget
	// since stages are scoped tasks, and the caller's approval hook —
	// nil for an unattended pipeline, the turn's own card for a machine
	// step somebody is waiting on.
	if confirm == nil {
		confirm = func(name, args string) bool { return true }
	}
	// The caller's guardrails, when it handed any over (see StageGuardrails).
	// Narrowed to pre_action by stageCheck: this stage can ACT, and an action
	// gate is the only guard that prevents rather than redacts.
	guards := stageGuardrails(ctx)
	resp, _, err := T.RunAgentLoop(ctx, []Message{{Role: "user", Content: prompt}}, AgentLoopConfig{
		Tools:             tools,
		Tier:              tier,
		MaxRounds:         pipelineStageMaxRounds,
		Confirm:           confirm,
		GuardrailCheck:    guards.stageCheck(),
		GuardrailHalted:   guards.Halted,
		GuardrailReject:   guards.Reject,
		GuardrailDeclines: guards.Declines,
		ChatOptions: []ChatOption{
			WithRouteKey("app.orchestrate.worker"),
			WithThink(think),
		},
	})
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", Error("worker returned no response")
	}
	return resp.Content, nil
}

// runToolStage calls the stage's named tool with the author's rendered
// arguments and returns its result. No LLM runs.
//
// Argument values go through the same templating a prompt does, so a
// tool stage composes with everything upstream: {stage:plan.expression}
// feeds a calculator, {prev} feeds a formatter. They render to strings —
// a tool wanting a number coerces exactly as it does for a
// model-supplied call, so this needs no separate type system.
//
// A missing tool is a clear run-time error rather than a save-time one:
// tool availability is per-user and per-agent, so the pipeline that
// validates for one caller may legitimately not resolve for another.
func (r *pipelineRun) runToolStage(ctx context.Context, stage PipelineStage, prev string, tools []AgentToolDef, vars map[string]string) (string, error) {
	name := strings.TrimSpace(stage.Tool)
	var handler ToolHandlerFunc
	var needsConfirm bool // consequential — the set the pre_action gate covers
	for _, td := range tools {
		if td.Tool.Name == name {
			handler, needsConfirm = td.Handler, td.NeedsConfirm
			break
		}
	}
	if handler == nil {
		available := make([]string, 0, len(tools))
		for _, td := range tools {
			available = append(available, td.Tool.Name)
		}
		sort.Strings(available)
		if len(available) == 0 {
			return "", Error("tool " + strconv.Quote(name) + " is not available to this pipeline — the caller supplied no tool catalog. A tool stage can only call tools the invoking agent has.")
		}
		return "", Error("tool " + strconv.Quote(name) + " is not available to this pipeline; the caller has: " + strings.Join(available, ", "))
	}
	args := make(map[string]any, len(stage.Args))
	for k, tmpl := range stage.Args {
		v := resolveStageTemplate(tmpl, r.input, prev, r.outputs)
		for from, to := range vars {
			v = strings.ReplaceAll(v, from, to)
		}
		args[k] = r.applyRunVars(v)
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	// Same pre_action gate a worker stage's own tool calls get, for the same
	// reason and on the same set of tools — the consequential ones. A tool
	// stage's NAME is the author's, but its ARGUMENTS are templated from
	// upstream stage output, which is model-written and can carry whatever the
	// pipeline read along the way. "The owner wrote the recipe" establishes
	// which tool runs; it does not establish what it is about to be told to do.
	if guards := stageGuardrails(ctx); guards.Check != nil && needsConfirm {
		if dec := guards.Check(GuardHookPreAction, name+" "+formatArgsForGuardrail(args)); dec.Blocked {
			Log("[pipeline] stage %q: guardrail blocked tool %q", stage.Name, name)
			// The stage FAILS rather than returning the block message as its
			// output: a stage's return value flows into the next stage as
			// {prev}, so handing back a refusal would feed a downstream prompt
			// text that reads like content. A pipeline is not a conversation —
			// there is nobody here to read a message and choose differently.
			return "", Error("stage " + stage.Name + ": tool " + strconv.Quote(name) + " was not called — a constraint on the agent running this pipeline covers it")
		}
	}
	out, err := handler(args)
	if err != nil {
		return "", err
	}
	return out, nil
}

// stageTier resolves a stage's declared model tier. Empty or "worker"
// is WORKER; only an explicit "lead" escalates. Validate already
// rejected anything else and every kind this can't apply to.
func stageTier(stage PipelineStage) LLMTier {
	if strings.EqualFold(strings.TrimSpace(stage.Model), "lead") {
		return LEAD
	}
	return WORKER
}

// pipelineStageMaxRounds caps how many rounds a tool-equipped worker
// stage can take. Smaller than a full agent loop — pipeline stages are
// supposed to be focused tasks (one lookup, one synthesis), not
// open-ended investigations. If a stage needs more rounds, that's a
// signal the work should be split into multiple stages or moved to a
// full agent stage.
const pipelineStageMaxRounds = 10

// StageToolRounds is pipelineStageMaxRounds for callers that have to
// TELL somebody what a tool-equipped step can cost. The editor's cost
// line quotes it, and a number quoted from anywhere but the constant
// itself is a number that goes stale.
const StageToolRounds = pipelineStageMaxRounds

// Fanout caps. Items bounds how many branches a single fanout stage
// runs (a runaway decompose stage shouldn't spawn hundreds of LLM
// calls); Parallel bounds how many run concurrently so a fanout doesn't
// monopolize the worker. Truncation past the item cap is logged, never
// silent.
const (
	fanoutMaxItems = 12
	fanoutParallel = 6
)

// FanoutMaxItems is the item cap, exported so a surface quoting the
// price of a fanout takes the number FROM the cap rather than writing
// its own copy that goes stale the day this changes.
const FanoutMaxItems = fanoutMaxItems

// runFanoutStage runs the stage's inner work once per element of the
// FanOver stage's list output, bounded-parallel, and combines the
// results into one labeled block ("## Item N: <item>\n<result>") that
// the next stage reads via {prev} / {stage:NAME}. Each branch is a
// worker (with the stage's resolved tools) unless the stage names an
// agent, in which case it dispatches. The current element substitutes
// into the stage prompt as {item}. Per-branch errors are non-fatal —
// the branch records an error marker and the rest proceed — so one bad
// search doesn't sink the whole fan.
func (r *pipelineRun) runFanoutStage(ctx context.Context, stage PipelineStage, prev string, stageTools []AgentToolDef, status func(string)) (string, map[string]any, error) {
	T, outputs, dispatch, input := r.app, r.outputs, r.dispatch, r.input
	// Parse uncapped first so we can report honest truncation.
	items, err := fanoutItems(stage.FanOver, outputs)
	if err != nil {
		return "", nil, Error("fanout stage " + stage.Name + ": " + err.Error())
	}
	if len(items) == 0 {
		return "", nil, Error("fanout stage " + stage.Name + ": fan_over " + stage.FanOver + " produced no list items")
	}
	if len(items) > fanoutMaxItems {
		if status != nil {
			status(fmt.Sprintf("fanout %s: %d items, capping at %d", stage.Name, len(items), fanoutMaxItems))
		}
		items = items[:fanoutMaxItems]
	}
	if status != nil {
		if body := len(stage.Body); body > 0 {
			// Say the multiplication BEFORE paying it. A body turns N
			// branches into N x K model calls, and somebody watching a
			// bill should not have to work that out from a stage count.
			status(fmt.Sprintf("fanout %s: %d branches x %d stages = %d model calls (parallel %d)",
				stage.Name, len(items), body, len(items)*body, fanoutParallel))
		} else {
			status(fmt.Sprintf("fanout %s: %d branches (parallel %d)", stage.Name, len(items), fanoutParallel))
		}
	}

	think := StageThinks(stage)
	tier := stageTier(stage)
	agent := strings.TrimSpace(stage.Agent)

	results := make([]string, len(items))
	collected := make([]map[string]any, len(items))
	lg := NewLimitGroup(fanoutParallel)
	for i, item := range items {
		if ctx.Err() != nil {
			break
		}
		lg.Add(1) // blocks until a slot frees, bounding concurrency
		go func(idx int, it string) {
			defer lg.Done()
			// A BODY branch is a small pipeline of its own, in its own
			// scope. Runs first because it replaces the single call
			// entirely: the stage's prompt belongs to the body stages.
			if len(stage.Body) > 0 {
				out, fields, err := r.runBranchBody(ctx, stage, it, idx, len(items), prev)
				if err != nil {
					results[idx] = fmt.Sprintf("(branch error: %v)", err)
					if status != nil {
						status(fmt.Sprintf("fanout %s: branch %d/%d failed: %v", stage.Name, idx+1, len(items), err))
					}
					return
				}
				results[idx] = strings.TrimSpace(out)
				collected[idx] = fields
				return
			}
			// {item} is the per-branch element; the rest of the templating
			// vocabulary ({input}/{prev}/{stage:NAME}) resolves as usual.
			p := resolveStageTemplate(strings.ReplaceAll(stage.Prompt, "{item}", it), input, prev, outputs)
			var out string
			var err error
			if agent != "" {
				if dispatch == nil {
					err = Error("agent fanout but no dispatch hook provided")
				} else {
					out, err = dispatch(WithoutStageGuardrails(ctx), agent, p) // the branch agent carries its own rules
				}
			} else {
				// No jsonMode: a fanout branch produces free text that
				// gets joined into the labeled block, not a declared
				// shape (Validate rejects Output on kind=fanout).
				out, err = T.runWorkerStage(ctx, p, stageTools, think, false, tier)
			}
			if err != nil {
				results[idx] = fmt.Sprintf("(branch error: %v)", err)
				if status != nil {
					status(fmt.Sprintf("fanout %s: branch %d/%d failed: %v", stage.Name, idx+1, len(items), err))
				}
				return
			}
			results[idx] = strings.TrimSpace(out)
		}(i, item)
	}
	lg.Wait()
	if ctx.Err() != nil {
		return "", nil, ctx.Err()
	}

	var b strings.Builder
	for i, item := range items {
		fmt.Fprintf(&b, "## Item %d: %s\n%s\n\n", i+1, strings.TrimSpace(item), results[i])
	}
	return strings.TrimSpace(b.String()), fanoutFields(items, collected), nil
}

// ErrNoSuchAgent is how a host tells the interpreter that a name it was asked
// to dispatch to is not one of its agents.
//
// It exists for the panel, where a voice is EITHER an agent or a role and the
// interpreter has no way to tell them apart — it does not know what an agent
// is, which is the property that keeps the interpreter portable. A host that
// never returns it simply has no roles: every voice must name something real,
// and one that does not is reported as a gap rather than answered by a worker
// wearing its name.
var ErrNoSuchAgent = Error("no such agent")

// panelParallel bounds how many voices speak at once. Same shape as the
// fanout cap and for the same reason: a stage that opens N connections to a
// worker LLM is a stage that can take the deployment down with one save.
const panelParallel = 6

// runPanelStage puts every voice on the SAME question, in parallel, for as
// many rounds as the stage asks — each round reading what the last one said.
//
// The two properties that make it a panel rather than a fan:
//
//   - Every voice gets the same context. A fanout's branches each get their
//     own item and never meet; here the item IS the question, and what
//     differs is who is answering.
//   - Rounds are sequential and cumulative. Round one is a poll — nobody has
//     replied to anybody. Round two is where a voice can say "that is wrong
//     because", which is the whole reason to run a panel instead of asking
//     one worker for three opinions in one call. Within a round the voices
//     are parallel and blind to each other, so nobody answers first and sets
//     the frame.
//
// The product is the labeled transcript of EVERY round, not a verdict and not
// the last round alone: what a synthesizer needs is who said what and where
// they moved. Judging is the next stage's job.
//
// A voice that fails is recorded and the round continues, the same
// non-fatal rule a fanout branch follows: a panel that collapses because one
// agent timed out is worse than a panel with a gap in it, and the gap is
// visible in the transcript.
func (r *pipelineRun) runPanelStage(ctx context.Context, stage PipelineStage, prev string, stageTools []AgentToolDef, status func(string)) (string, map[string]any, error) {
	T, outputs, dispatch, input := r.app, r.outputs, r.dispatch, r.input
	voices := make([]string, 0, len(stage.Panel))
	for _, v := range stage.Panel {
		if v = strings.TrimSpace(v); v != "" {
			voices = append(voices, v)
		}
	}
	if len(voices) < 2 {
		return "", nil, Error("panel stage " + stage.Name + ": needs at least two voices")
	}
	if len(voices) > panelMaxVoices {
		if status != nil {
			status(fmt.Sprintf("panel %s: %d voices, capping at %d", stage.Name, len(voices), panelMaxVoices))
		}
		voices = voices[:panelMaxVoices]
	}
	rounds := stage.Count
	if rounds < 1 {
		rounds = 1
	}
	if rounds > panelMaxRounds {
		rounds = panelMaxRounds
	}
	if status != nil {
		// The multiplication, before it is paid. Voices times rounds is the
		// number nobody works out from a stage count, and it is the number
		// that shows up on a bill.
		status(fmt.Sprintf("panel %s: %d voices x %d round(s) = %d model calls (parallel %d)",
			stage.Name, len(voices), rounds, len(voices)*rounds, panelParallel))
	}

	think := StageThinks(stage)
	tier := stageTier(stage)

	var transcript []panelSaid
	var b strings.Builder
	for round := 1; round <= rounds; round++ {
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		// What the previous rounds said, handed to every voice as {panel}.
		// Empty on round one, which is what makes round one a poll — and the
		// prompt can say so, because the author can see the same emptiness.
		sofar := renderPanel(transcript)
		results := make([]string, len(voices))
		lg := NewLimitGroup(panelParallel)
		for i, v := range voices {
			if ctx.Err() != nil {
				break
			}
			lg.Add(1)
			go func(idx int, voice string) {
				defer lg.Done()
				p := resolveStageTemplate(panelPrompt(stage.Prompt, voice, round, rounds, sofar), input, prev, outputs)
				var out string
				var err error
				// A voice that names one of your agents IS that agent: its
				// persona, its memory, its tools. One that does not is a
				// ROLE, and the worker answers as it.
				//
				// The host decides which, by returning ErrNoSuchAgent — and
				// ONLY that. Falling back on any error would let an agent
				// that timed out be silently impersonated by a worker
				// wearing its name, which is worse than the gap: the
				// transcript would read as the agent's own words.
				out, err = "", ErrNoSuchAgent
				if dispatch != nil {
					out, err = dispatch(WithoutStageGuardrails(ctx), voice, p)
				}
				if errors.Is(err, ErrNoSuchAgent) {
					out, err = T.runWorkerStage(ctx, p, stageTools, think, false, tier)
				}
				if err != nil {
					results[idx] = fmt.Sprintf("(no answer: %v)", err)
					if status != nil {
						status(fmt.Sprintf("panel %s: %s did not answer in round %d: %v", stage.Name, voice, round, err))
					}
					return
				}
				results[idx] = strings.TrimSpace(out)
			}(i, v)
		}
		lg.Wait()
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		for i, v := range voices {
			transcript = append(transcript, panelSaid{Voice: v, Text: results[i]})
			if rounds > 1 {
				fmt.Fprintf(&b, "## Round %d — %s\n%s\n\n", round, v, results[i])
				continue
			}
			fmt.Fprintf(&b, "## %s\n%s\n\n", v, results[i])
		}
	}
	// Declared shape for the stage that reads this: who spoke, and how many
	// times they were asked. A synthesizer fanning over the voices is the
	// obvious next move and should not have to parse the headings back out.
	names := make([]any, 0, len(voices))
	for _, v := range voices {
		names = append(names, v)
	}
	return strings.TrimSpace(b.String()), map[string]any{"voices": names, "rounds": rounds}, nil
}

// panelPrompt layers the panel's own vocabulary over the stage prompt:
// {voice} is who is answering, {panel} is what has been said so far, and
// {iteration}/{iterations} are the round and the total — the same two names a
// loop uses, because they mean the same thing and a second vocabulary for it
// would be a second thing to learn.
func panelPrompt(prompt, voice string, round, rounds int, sofar string) string {
	p := strings.ReplaceAll(prompt, "{voice}", voice)
	p = strings.ReplaceAll(p, "{iteration}", strconv.Itoa(round))
	p = strings.ReplaceAll(p, "{iterations}", strconv.Itoa(rounds))
	if strings.Contains(p, "{panel}") {
		return strings.ReplaceAll(p, "{panel}", sofar)
	}
	// The author did not place it, so the framework does — at the end, and
	// only when there is something to say. A voice that cannot see the round
	// before it is not on a panel, and losing that to a forgotten placeholder
	// would make the difference between a panel and a poll a typo.
	if strings.TrimSpace(sofar) == "" {
		return p
	}
	return p + "\n\n## Already said, by the others\n" + sofar +
		"\nAnswer as yourself. Where you agree, say so briefly and add what is missing; where you disagree, say which claim is wrong and why."
}

// panelSaid is one contribution: who, and what.
type panelSaid struct{ Voice, Text string }

// renderPanel is the transcript as a voice reads it — plainer than the block
// the next STAGE gets, because a voice is being asked to reply to people
// rather than to parse a document.
func renderPanel(said []panelSaid) string {
	var b strings.Builder
	for _, s := range said {
		fmt.Fprintf(&b, "%s: %s\n\n", s.Voice, strings.TrimSpace(s.Text))
	}
	return strings.TrimSpace(b.String())
}

// runBranchBody runs one branch's body in its own scope and returns what
// it produced: the last body stage's text, and that stage's declared
// fields when it declared any.
//
// The fields are what makes a fan MERGEABLE. Without them a following
// stage can only read the joined prose and score it by eye, which is the
// thing a declared shape exists to stop.
func (r *pipelineRun) runBranchBody(ctx context.Context, stage PipelineStage, item string, idx, total int, prev string) (string, map[string]any, error) {
	br := r.forBranch()
	vars := map[string]string{
		"{item}":     item,
		"{branch}":   strconv.Itoa(idx + 1),
		"{branches}": strconv.Itoa(total),
	}
	label := fmt.Sprintf("  %s branch %d/%d", stage.Name, idx+1, total)
	out, _, err := br.runList(ctx, stage.Body, prev, label, vars)
	if err != nil {
		return "", nil, err
	}
	// The stage whose text we are returning is the one whose shape we
	// return with it (runList records it), because a branch that ended on
	// a skip did not end on the last stage in the list.
	return out, br.outputs[br.lastStage].Fields, nil
}

// fanoutFields is the structured half of a fanout's result: one object per
// branch that produced a declared shape, each carrying its branch number
// and the item it ran on.
//
// Exposed as {stage:NAME.items} so a following stage can rank, filter, or
// fan over the survivors. Nil unless EVERY surviving branch declared a
// shape: a half-populated list would silently drop the branches whose body
// ended in prose, and a reader counting entries would get the wrong total.
func fanoutFields(items []string, collected []map[string]any) map[string]any {
	out := make([]any, 0, len(collected))
	for i, f := range collected {
		if len(f) == 0 {
			continue
		}
		entry := make(map[string]any, len(f)+2)
		for k, v := range f {
			entry[k] = v
		}
		entry["branch"] = i + 1
		if i < len(items) {
			entry["item"] = items[i]
		}
		out = append(out, entry)
	}
	if len(out) == 0 {
		return nil
	}
	return map[string]any{"items": out, "count": len(out)}
}

// runLoopStage repeats the stage's Body, threading each pass's last
// output into the next pass's {prev}. That carry is what separates loop
// from fanout: fanout runs N INDEPENDENT branches in parallel, a loop
// runs passes that each see what the last one produced.
//
// Termination is guaranteed by Count (validated 1..loopMaxIterations at
// save time); Until is an early exit checked after each full pass, never
// a substitute for the ceiling. A pipeline runs unattended, so "the
// model decides when to stop" is not on its own an acceptable bound.
//
// Body stage outputs land in the shared outputs map, so {stage:NAME}
// inside the body reads THIS pass's value and is overwritten by the
// next. Nothing after the loop can reference them — Validate rejects
// that, since such a reference would silently mean "whatever the last
// pass happened to leave."
func (r *pipelineRun) runLoopStage(ctx context.Context, stage PipelineStage, prev string) (string, error) {
	collectAll := strings.TrimSpace(stage.Collect) == "all"
	untilStage, untilField := SplitStageRef(stage.Until)

	var passes []string
	carry := prev
	for i := 1; i <= stage.Count; i++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		r.status(fmt.Sprintf("%s: pass %d/%d", stage.Name, i, stage.Count))
		vars := map[string]string{
			"{iteration}":  strconv.Itoa(i),
			"{iterations}": strconv.Itoa(stage.Count),
		}
		// A body branch can only skip WITHIN the pass (Validate rejects
		// a pipeline-ending branch inside a loop), so the stop flag is
		// never set here.
		out, _, err := r.runList(ctx, stage.Body, carry, "  "+stage.Name+" pass "+strconv.Itoa(i), vars)
		if err != nil {
			return "", err
		}
		carry = out
		passes = append(passes, out)

		if untilField == "" {
			continue
		}
		src, ok := r.outputs[untilStage]
		if !ok {
			// Body validated, so the stage exists — it just hasn't run
			// this pass (a body whose earlier stage errored can't reach
			// here, so this is defensive).
			continue
		}
		done, _ := src.Fields[untilField].(bool)
		if done {
			r.status(fmt.Sprintf("%s: %s went true after pass %d — stopping early", stage.Name, stage.Until, i))
			break
		}
	}

	if !collectAll {
		return carry, nil
	}
	var b strings.Builder
	for i, p := range passes {
		fmt.Fprintf(&b, "## Pass %d\n%s\n\n", i+1, p)
	}
	return strings.TrimSpace(b.String()), nil
}

// fanoutItems resolves a fan_over reference to the list of elements the
// fanout runs over.
//
//	"NAME"        the whole stage output, parsed leniently as a list
//	              (the historic form — a stage prompted to "return a
//	              JSON array" whose text IS that array)
//	"NAME.field"  a declared list field of a structured stage
//
// The field form isn't sugar: once a stage declares an Output contract
// its text is a JSON *object*, so the whole-output form would fail to
// parse and fall through to scraping prose out of JSON.
func fanoutItems(ref string, outputs map[string]stageOutput) ([]string, error) {
	name, field := SplitStageRef(ref)
	src, ok := outputs[name]
	if !ok {
		return nil, Error("fan_over stage " + name + " has no output")
	}
	if field == "" {
		return DecodeJSONList(src.Text, 0), nil
	}
	v, ok := src.Fields[field]
	if !ok {
		return nil, Error("stage " + name + " declares no output field " + field)
	}
	list, ok := v.([]any)
	if !ok {
		return nil, Error("fan_over field " + ref + " is not a list")
	}
	items := make([]string, 0, len(list))
	for _, it := range list {
		items = append(items, renderFieldValue(it))
	}
	return items, nil
}

// NoToolsMarker is the reserved name a tool list holds to say "none at
// all", as distinct from an empty list, which everywhere in the framework
// means "inherit whatever the caller has".
//
// A list needs both ends sayable. Reading emptiness as "nothing" is the
// tempting shortcut and it costs the far more common case: a step or stage
// left alone is one nobody has narrowed, not one somebody silenced. So the
// deliberate "nothing" gets a name of its own and survives a save, a
// reload, and an export — the same reason an agent's own tool list has
// carried this marker since long before a phase could.
const NoToolsMarker = "__none__"

// resolveStageTools picks the tool set a worker stage gets. Stage's
// own Tools field is the override: when set, returns the intersection
// of those names with the inherited pool (so the stage can name a
// subset). When stage.Tools is empty, returns the inherited pool
// verbatim — the natural "inherit caller's catalog" default. When
// both are empty, returns nil so the cheap tool-less path fires.
//
// NoToolsMarker anywhere in the list wins over everything else in it: a
// list holding the marker AND a name is a control somebody left in two
// states, and the reading that grants less is the safe one.
// StageTools narrows an inherited catalog to what a stage may reach: the
// reach first (a capability class, which is true for every caller), then
// the stage's own names on top of what is left (exact strings, which
// describe one caller's catalog at one moment).
//
// The counterpart of PhaseTools, deliberately identical in behaviour: a
// pipeline stage and a machine step are the two places an author writes
// this, and an author who learned one has learned the other.
func StageTools(stage PipelineStage, inherited []AgentToolDef) []AgentToolDef {
	switch StageReach(stage) {
	case ReachNone:
		return nil
	case ReachRead:
		inherited = FilterToolsByCaps(inherited, ReachAllowsCaps(ReachRead))
	}
	return resolveStageTools(stage.Tools, inherited)
}

// StageReach is the stage's reach, reading the legacy marker as the
// setting it always meant — the same back-compat PhaseReach gives a step
// saved before the field existed.
func StageReach(stage PipelineStage) string {
	if r := strings.TrimSpace(stage.Reach); r != "" {
		return r
	}
	for _, n := range stage.Tools {
		if strings.TrimSpace(n) == NoToolsMarker {
			return ReachNone
		}
	}
	return ReachAll
}

func resolveStageTools(stageTools []string, inheritedTools []AgentToolDef) []AgentToolDef {
	if len(stageTools) == 0 {
		return inheritedTools
	}
	for _, n := range stageTools {
		if strings.TrimSpace(n) == NoToolsMarker {
			return nil
		}
	}
	if len(inheritedTools) == 0 {
		return nil // stage named tools but caller didn't supply a catalog to pick from
	}
	want := make(map[string]bool, len(stageTools))
	for _, n := range stageTools {
		want[strings.TrimSpace(n)] = true
	}
	out := make([]AgentToolDef, 0, len(stageTools))
	for _, td := range inheritedTools {
		if want[td.Tool.Name] {
			out = append(out, td)
		}
	}
	return out
}

// resolveStageTemplate substitutes the pipeline templating vocabulary
// into a stage prompt:
//
//	{input}             — the pipeline's top-level input
//	{prev}              — the immediately-preceding stage's output
//	{stage:NAME}        — a named prior stage's output
//	{stage:NAME.field}  — one declared field of a structured stage
//
// Plain literal replacement is enough even with the field form:
// {stage:plan} can't match inside {stage:plan.queries} because the
// closing brace is part of the literal, so there's no ordering hazard
// and no scanner to get wrong.
//
// Unknown placeholders (a typo'd stage name, a literal brace) are left
// untouched rather than blanked, so a mistake degrades to a visible
// prompt artifact instead of silently dropping context. Validate now
// catches the typo at save time, so this is a second line of defense
// rather than the only one.
func resolveStageTemplate(tmpl, input, prev string, outputs map[string]stageOutput) string {
	s := strings.ReplaceAll(tmpl, "{input}", input)
	s = strings.ReplaceAll(s, "{prev}", prev)
	for name, out := range outputs {
		s = strings.ReplaceAll(s, "{stage:"+name+"}", out.Text)
		for field, v := range out.Fields {
			s = strings.ReplaceAll(s, "{stage:"+name+"."+field+"}", renderFieldValue(v))
		}
	}
	return s
}

// runMachineStage runs a stored machine as one stage.
//
// The counterpart of the machine's pipeline phase, and the pair is what
// lets the two primitives compose in both directions: a machine step can
// be a pipeline, and a pipeline stage can be a machine. The shape that
// wants this is a fanout body whose branch is a whole child run, which is
// how N gaps get filled at once rather than one after another.
//
// The host supplies the runner (PipelineHooks.Machine): resolving the
// reference, deciding what the run may reach, and enforcing nesting are
// all its business, not the interpreter's.
func (r *pipelineRun) runMachineStage(ctx context.Context, stage PipelineStage, prompt string) (string, map[string]any, error) {
	if r.machines == nil {
		return "", nil, Error("stage " + stage.Name + " runs machine " + strconv.Quote(stage.Machine) +
			", but this pipeline was started without a machine runner. Run it from a surface that supplies one (RunPipelineDefHooks).")
	}
	out, fields, err := r.machines(ctx, stage.Machine, prompt)
	if err != nil {
		return "", nil, Error("stage " + stage.Name + ": machine " + strconv.Quote(stage.Machine) + ": " + err.Error())
	}
	out = strings.TrimSpace(out)
	if len(stage.ModelOutput()) == 0 {
		return out, nil, nil
	}
	// A declared stage takes the child run's own shape when the names line
	// up, which costs nothing: the machine's last step already produced
	// validated fields.
	if kept, ok := declaredSubset(stage.ModelOutput(), fields); ok {
		return out, kept, nil
	}
	// They did not line up. Decoding the text is the one remaining honest
	// try — a machine whose last step returns JSON prose still satisfies a
	// contract — and failing that, say which side to fix. There is no
	// model in this stage to ask again, and re-running the machine to
	// reshape its answer would be an expensive way to hide a mismatch.
	decoded, derr := decodeStageOutput(out, stage.ModelOutput())
	if derr != nil {
		return "", nil, Error("stage " + stage.Name + ": machine " + strconv.Quote(stage.Machine) +
			" finished, but its result does not carry the fields this stage declares (" + fieldNameList(stage.ModelOutput()) +
			"). Declare those fields on the machine's LAST step, or drop the output contract from this stage and read its text.")
	}
	return out, decoded, nil
}

// declaredSubset picks exactly the declared fields out of what a child run
// produced, and reports whether every one of them was there.
//
// Every one, not some: a missing field would decode as empty and read as
// "the run had nothing to say about it", when the truth is that nobody
// asked it.
func declaredSubset(declared []PipelineField, fields map[string]any) (map[string]any, bool) {
	if len(fields) == 0 {
		return nil, false
	}
	out := make(map[string]any, len(declared))
	for _, f := range declared {
		v, ok := fields[f.Name]
		if !ok {
			return nil, false
		}
		out[f.Name] = v
	}
	return out, true
}

// fieldNameList names a contract's fields for a message about it.
func fieldNameList(fields []PipelineField) string {
	names := make([]string, 0, len(fields))
	for _, f := range fields {
		names = append(names, f.Name)
	}
	return strings.Join(names, ", ")
}
