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
	"fmt"
	"strings"
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
// stage for live progress.
func (T *AppCore) RunPipelineDefSync(ctx context.Context, def PipelineDef, input string, dispatch PipelineDispatch, status func(string)) (string, error) {
	return T.executePipelineDef(ctx, def, input, dispatch, status)
}

// RunPipelineDefAsync compiles a pipeline definition into a
// PipelineWork and runs it through RunPipeline (queue, slot, session
// lifecycle, notification, cleanup). onResult, if set, receives the
// final output when the pipeline completes — the caller persists it /
// delivers it. Returns the pipeline ID immediately.
func (T *AppCore) RunPipelineDefAsync(cfg PipelineConfig, def PipelineDef, input string, dispatch PipelineDispatch, onResult func(string)) string {
	return T.RunPipeline(cfg, func(ctx context.Context, pc *PipelineCtx) error {
		out, err := T.executePipelineDef(ctx, def, input, dispatch, pc.Status)
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
func (T *AppCore) executePipelineDef(ctx context.Context, def PipelineDef, input string, dispatch PipelineDispatch, status func(string)) (string, error) {
	if err := def.Validate(); err != nil {
		return "", err
	}
	outputs := make(map[string]string, len(def.Stages))
	prev := input
	for i, stage := range def.Stages {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if status != nil {
			status(fmt.Sprintf("Stage %d/%d: %s", i+1, len(def.Stages), stage.Name))
		}
		prompt := resolveStageTemplate(stage.Prompt, input, prev, outputs)

		var out string
		var err error
		switch stage.Kind {
		case StageWorker, StageSynthesize:
			out, err = T.runWorkerStage(ctx, prompt)
		case StageAgent:
			if dispatch == nil {
				return "", Error("stage " + stage.Name + ": agent stage but no dispatch hook provided")
			}
			out, err = dispatch(ctx, stage.Agent, prompt)
		case StageFanout:
			// Phase 2: run the inner work across each element of the
			// FanOver stage's JSON-array output, in parallel, then
			// collect. Not yet implemented — fail loudly rather than
			// silently treating it as a single worker call.
			return "", Error("stage " + stage.Name + ": fanout stages are not yet supported (phase 2)")
		default:
			return "", Error("stage " + stage.Name + ": unknown kind " + string(stage.Kind))
		}
		if err != nil {
			return "", fmt.Errorf("stage %q: %w", stage.Name, err)
		}
		out = strings.TrimSpace(out)
		outputs[stage.Name] = out
		prev = out
	}
	return prev, nil
}

// runWorkerStage runs a single worker-tier LLM call with the resolved
// prompt. No tools, no persona — prompt in, text out. Thinking is off:
// pipeline stages are transforms/extractions/synthesis where a fast
// deterministic pass beats a deliberation budget. (A per-stage think
// flag can be added to PipelineStage later if a stage needs it.)
func (T *AppCore) runWorkerStage(ctx context.Context, prompt string) (string, error) {
	resp, err := T.WorkerChat(ctx, []Message{{Role: "user", Content: prompt}}, WithThink(false))
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", Error("worker returned no response")
	}
	return resp.Content, nil
}

// resolveStageTemplate substitutes the pipeline templating vocabulary
// into a stage prompt:
//
//	{input}       — the pipeline's top-level input
//	{prev}        — the immediately-preceding stage's output
//	{stage:NAME}  — a named prior stage's output
//
// Unknown placeholders (a typo'd stage name, a literal brace) are left
// untouched rather than blanked, so a mistake degrades to a visible
// prompt artifact instead of silently dropping context.
func resolveStageTemplate(tmpl, input, prev string, outputs map[string]string) string {
	s := strings.ReplaceAll(tmpl, "{input}", input)
	s = strings.ReplaceAll(s, "{prev}", prev)
	for name, out := range outputs {
		s = strings.ReplaceAll(s, "{stage:"+name+"}", out)
	}
	return s
}
