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
	if err := def.Validate(); err != nil {
		return "", err
	}
	outputs := make(map[string]string, len(def.Stages))
	prev := input
	for i, stage := range def.Stages {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		// Emit start event with stage kind so the activity pane reader
		// can tell at a glance whether a slow stage is a worker LLM
		// call (cheap) or an agent dispatch (full sub-agent run). When
		// the worker stage has tools (inherited or explicit), tag the
		// kind so the operator can see at a glance which stages do
		// real I/O vs pure transforms.
		stageLabel := fmt.Sprintf("Stage %d/%d: %s", i+1, len(def.Stages), stage.Name)
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
		if stage.Kind == StageWorker || stage.Kind == StageSynthesize {
			stageTools = resolveStageTools(stage.Tools, inheritedTools)
			if len(stageTools) > 0 {
				names := make([]string, 0, len(stageTools))
				for _, td := range stageTools {
					names = append(names, td.Tool.Name)
				}
				kindLabel += " tools=[" + strings.Join(names, ",") + "]"
			}
		}
		if status != nil {
			status(stageLabel + " [" + kindLabel + "] starting")
		}
		prompt := resolveStageTemplate(stage.Prompt, input, prev, outputs)

		stageStart := time.Now()
		var out string
		var err error
		switch stage.Kind {
		case StageWorker, StageSynthesize:
			// Resolve per-stage think: nil = default off, &true = on,
			// &false = explicitly off (same as default but
			// self-documenting).
			think := false
			if stage.Think != nil {
				think = *stage.Think
			}
			out, err = T.runWorkerStage(ctx, prompt, stageTools, think)
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
		elapsed := time.Since(stageStart).Round(time.Millisecond * 100)
		if err != nil {
			if status != nil {
				status(stageLabel + " failed after " + elapsed.String() + ": " + err.Error())
			}
			return "", fmt.Errorf("stage %q: %w", stage.Name, err)
		}
		out = strings.TrimSpace(out)
		outputs[stage.Name] = out
		prev = out
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
	}
	return prev, nil
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
func (T *AppCore) runWorkerStage(ctx context.Context, prompt string, tools []AgentToolDef, think bool) (string, error) {
	if len(tools) == 0 {
		// Cheap path — pure LLM transform.
		resp, err := T.WorkerChat(ctx, []Message{{Role: "user", Content: prompt}}, WithThink(think))
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

// pipelineStageMaxRounds caps how many rounds a tool-equipped worker
// stage can take. Smaller than a full agent loop — pipeline stages are
// supposed to be focused tasks (one lookup, one synthesis), not
// open-ended investigations. If a stage needs more rounds, that's a
// signal the work should be split into multiple stages or moved to a
// full agent stage.
const pipelineStageMaxRounds = 10

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
