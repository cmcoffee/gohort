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
	if err := def.Validate(); err != nil {
		return "", err
	}
	r := &pipelineRun{
		app:       T,
		input:     input,
		dispatch:  dispatch,
		sink:      sink,
		inherited: inheritedTools,
		vars:      vars,
		outputs:   make(map[string]stageOutput, len(def.Stages)),
	}
	out, _, err := r.runList(ctx, def.Stages, input, "Stage")
	return out, err
}

// pipelineRun is one execution of a pipeline definition — the state every
// stage needs, gathered so the stage runner isn't an eight-argument
// function. Created by executePipelineDef and threaded through loop
// bodies unchanged, which is what lets a body stage read outer stages.
type pipelineRun struct {
	app       *AppCore
	input     string
	dispatch  PipelineDispatch
	sink      PipelineSink
	inherited []AgentToolDef
	// vars are RUN-scoped template values — the submit form's fields, keyed
	// "{name}". Kept on the run rather than threaded through runList because a
	// loop body builds its own vars ({iteration}) and passes those down: a
	// threaded value would be visible everywhere EXCEPT inside a loop, which is
	// where a refinement pipeline does its work.
	vars     map[string]string
	outputs  map[string]stageOutput
	blockSeq atomic.Int64 // block ids, unique across parallel fanout branches
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
	if r == nil || r.sink == nil {
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
			stageTools = resolveStageTools(stage.Tools, inheritedTools)
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
			think := false
			if stage.Think != nil {
				think = *stage.Think
			}
			// JSON mode only helps the tool-less path — see runWorkerStage.
			jsonMode := len(stage.Output) > 0
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
			call = func(p string) (string, error) { return dispatch(ctx, stage.Agent, p) }
		case StageFanout:
			// Run the stage's inner work across each element of the
			// FanOver stage's JSON-array output, in parallel, then collect
			// into one labeled block for the next stage to consume.
			out, err = T.runFanoutStage(ctx, stage, input, prev, outputs, stageTools, dispatch, status)
		case StageLoop:
			// Repeat the body, threading each pass's result into the next.
			out, err = r.runLoopStage(ctx, stage, prev)
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
				fields, err = decodeStageOutput(out, stage.Output)
				if err != nil {
					err = Error("tool returned a result that does not match the declared output: " + err.Error())
				}
			}
		default:
			return "", Error("stage " + stage.Name + ": unknown kind " + string(stage.Kind))
		}
		if call != nil {
			if len(stage.Output) > 0 {
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
		outputs[stage.Name] = stageOutput{Text: out, Fields: fields}
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
	contract := renderOutputContract(stage.Output)
	out, err := call(prompt + contract)
	if err != nil {
		return "", nil, err
	}
	fields, derr := decodeStageOutput(out, stage.Output)
	if derr == nil {
		return out, fields, nil
	}
	if status != nil {
		status("stage " + stage.Name + ": reply did not match the declared shape (" + derr.Error() + ") — retrying once")
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
	if fields, derr = decodeStageOutput(out, stage.Output); derr != nil {
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
		if f.Desc != "" {
			b.WriteString(": " + f.Desc)
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
	// since pipeline stages are scoped tasks, no confirm prompt for
	// tool calls since pipelines run un-attended.
	resp, _, err := T.RunAgentLoop(ctx, []Message{{Role: "user", Content: prompt}}, AgentLoopConfig{
		Tools:     tools,
		Tier:      tier,
		MaxRounds: pipelineStageMaxRounds,
		Confirm:   func(name, args string) bool { return true },
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
	for _, td := range tools {
		if td.Tool.Name == name {
			handler = td.Handler
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

// Fanout caps. Items bounds how many branches a single fanout stage
// runs (a runaway decompose stage shouldn't spawn hundreds of LLM
// calls); Parallel bounds how many run concurrently so a fanout doesn't
// monopolize the worker. Truncation past the item cap is logged, never
// silent.
const (
	fanoutMaxItems = 12
	fanoutParallel = 6
)

// runFanoutStage runs the stage's inner work once per element of the
// FanOver stage's list output, bounded-parallel, and combines the
// results into one labeled block ("## Item N: <item>\n<result>") that
// the next stage reads via {prev} / {stage:NAME}. Each branch is a
// worker (with the stage's resolved tools) unless the stage names an
// agent, in which case it dispatches. The current element substitutes
// into the stage prompt as {item}. Per-branch errors are non-fatal —
// the branch records an error marker and the rest proceed — so one bad
// search doesn't sink the whole fan.
func (T *AppCore) runFanoutStage(ctx context.Context, stage PipelineStage, input, prev string, outputs map[string]stageOutput, stageTools []AgentToolDef, dispatch PipelineDispatch, status func(string)) (string, error) {
	// Parse uncapped first so we can report honest truncation.
	items, err := fanoutItems(stage.FanOver, outputs)
	if err != nil {
		return "", Error("fanout stage " + stage.Name + ": " + err.Error())
	}
	if len(items) == 0 {
		return "", Error("fanout stage " + stage.Name + ": fan_over " + stage.FanOver + " produced no list items")
	}
	if len(items) > fanoutMaxItems {
		if status != nil {
			status(fmt.Sprintf("fanout %s: %d items, capping at %d", stage.Name, len(items), fanoutMaxItems))
		}
		items = items[:fanoutMaxItems]
	}
	if status != nil {
		status(fmt.Sprintf("fanout %s: %d branches (parallel %d)", stage.Name, len(items), fanoutParallel))
	}

	think := false
	if stage.Think != nil {
		think = *stage.Think
	}
	tier := stageTier(stage)
	agent := strings.TrimSpace(stage.Agent)

	results := make([]string, len(items))
	lg := NewLimitGroup(fanoutParallel)
	for i, item := range items {
		if ctx.Err() != nil {
			break
		}
		lg.Add(1) // blocks until a slot frees, bounding concurrency
		go func(idx int, it string) {
			defer lg.Done()
			// {item} is the per-branch element; the rest of the templating
			// vocabulary ({input}/{prev}/{stage:NAME}) resolves as usual.
			p := resolveStageTemplate(strings.ReplaceAll(stage.Prompt, "{item}", it), input, prev, outputs)
			var out string
			var err error
			if agent != "" {
				if dispatch == nil {
					err = Error("agent fanout but no dispatch hook provided")
				} else {
					out, err = dispatch(ctx, agent, p)
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
		return "", ctx.Err()
	}

	var b strings.Builder
	for i, item := range items {
		fmt.Fprintf(&b, "## Item %d: %s\n%s\n\n", i+1, strings.TrimSpace(item), results[i])
	}
	return strings.TrimSpace(b.String()), nil
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

// resolveStageTools picks the tool set a worker stage gets. Stage's
// own Tools field is the override: when set, returns the intersection
// of those names with the inherited pool (so the stage can name a
// subset). When stage.Tools is empty, returns the inherited pool
// verbatim — the natural "inherit caller's catalog" default. When
// both are empty, returns nil so the cheap tool-less path fires.
func resolveStageTools(stageTools []string, inheritedTools []AgentToolDef) []AgentToolDef {
	if len(stageTools) == 0 {
		return inheritedTools
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
