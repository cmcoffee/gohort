// Pipeline-mode temp tools — admin-authored or LLM-authored
// multi-step flows exposed as a single LLM-callable tool. The
// sub-agent runs with its OWN system prompt + tool allowlist;
// returns its final text as the tool's result. See
// core.TempToolModePipeline for the data model.
//
// Recursion guard: runPipelineSubAgent caps pipelineDepth at
// maxPipelineDepth so a pipeline tool that itself calls another
// pipeline tool can't infinite-loop. Each call increments on entry
// and decrements on exit (deferred).

package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// maxPipelineDepth caps recursive pipeline calls. Most pipelines
// only need 1-2 levels; deeper than 3 almost always means a tool
// is mis-authored (recursive call to itself by accident).
const maxPipelineDepth = 3

// runPipelineSubAgent wires ToolSession.SubAgentRunner to a worker-
// tier RunAgentLoop call. allowedToolNames filters the parent
// session's tool catalog to just what the pipeline declared; the
// sub-agent then has the same Confirm + auto-context plumbing the
// regular worker steps use, minus the plan/intent surface.
func (t *chatTurn) runPipelineSubAgent(ctx context.Context, sysPrompt, userMsg string, allowedToolNames []string, maxRounds int) (string, error) {
	if t.pipelineDepth >= maxPipelineDepth {
		return "", fmt.Errorf("pipeline recursion depth exceeded (limit %d) — check that a pipeline tool isn't calling itself directly or transitively", maxPipelineDepth)
	}
	t.pipelineDepth++
	defer func() { t.pipelineDepth-- }()

	if maxRounds <= 0 {
		maxRounds = 6
	}

	// Build the sub-agent's tool catalog from the names the pipeline
	// declared. Unknown names log + skip — the pipeline-tool authoring
	// step warns the user at create time so the LLM doesn't write
	// pipelines referencing non-existent tools.
	cleanNames := make([]string, 0, len(allowedToolNames))
	for _, n := range allowedToolNames {
		n = strings.TrimSpace(n)
		if n != "" {
			cleanNames = append(cleanNames, n)
		}
	}
	tools, err := GetAgentTools(cleanNames...)
	if err != nil {
		// One bad name shouldn't kill the whole pipeline. Fall back
		// to filtering individually so the sub-agent gets whatever
		// was valid.
		tools = nil
		for _, n := range cleanNames {
			if td, terr := GetAgentTools(n); terr == nil && len(td) > 0 {
				tools = append(tools, td[0])
			}
		}
	}

	f := false
	resp, _, err2 := t.app.RunAgentLoop(ctx, []Message{{Role: "user", Content: userMsg}}, AgentLoopConfig{
		SystemPrompt: sysPrompt,
		Tools:        tools,
		MaxRounds:    maxRounds,
		Confirm:      func(name, args string) bool { return true },
		ChatOptions: []ChatOption{
			WithRouteKey("app.orchestrate.worker"),
			WithThink(f),
		},
	})
	if err2 != nil {
		return "", err2
	}
	if resp == nil {
		return "", errors.New("pipeline sub-agent returned no response")
	}
	return strings.TrimSpace(resp.Content), nil
}

// createPipelineToolToolDef is the LLM-facing tool that lets the
// orchestrator author a new pipeline tool mid-conversation. Mirrors
// the create_temp_tool / create_api_tool shape but writes a TempTool
// with Mode = "pipeline" instead of shell/api.
func (t *chatTurn) createPipelineToolToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "create_pipeline_tool",
			Description: "Author a multi-step sub-agent flow as a single callable tool, attached to ONE specific agent. Pipelines authored here are ALWAYS agent-scoped — saved into the target agent's tools[] and callable on the next turn against that agent, no admin approval required.\n\nThe `for_agent` parameter (or the session's authoring focus, set automatically by your most recent get_agent / create_agent call) determines which agent the pipeline attaches to. If neither is set, the call refuses with a directive error — agent-scoped pipelines need a target.\n\nUser-wide cross-cutting pipeline tools (available to every agent the user runs) are not authored via this tool — the admin creates those via the admin UI. Don't try to make user-wide pipelines from a chat conversation.",
			Parameters: map[string]ToolParam{
				"name": {
					Type:        "string",
					Description: "Snake_case tool name the LLM will see in its catalog. Must be unique within the chosen scope (per-agent for scoped tools; user-wide pool for global tools). Example: \"research_company\".",
				},
				"description": {
					Type:        "string",
					Description: "One-line summary of what the tool does. The calling agent reads this in its catalog to decide whether to call it. Example: \"Look up a company's recent news, financials, and key people. Returns a structured summary.\"",
				},
				"params": {
					Type:        "object",
					Description: "JSON object mapping parameter names to {type, description, required?}. Parameters are surfaced to callers; their values get substituted into pipeline_prompt / step args as `{name}` placeholders. Example: {\"company\": {\"type\": \"string\", \"description\": \"Company name to research\", \"required\": true}}.",
				},
				"pipeline_prompt": {
					Type:        "string",
					Description: "ADAPTIVE pipeline. System prompt for an LLM sub-agent that runs the inner steps with reasoning between them. Use when steps depend on prior results in ways that need judgment (\"if the top result is paywalled, try a different angle\"). Mutually exclusive with pipeline_steps; if both are set, pipeline_steps wins. `{param_name}` placeholders are replaced with dispatch arg values.",
				},
				"pipeline_steps": {
					Type:        "array",
					Description: "DETERMINISTIC pipeline. Ordered list of step objects, each {tool, args, name?}, executed in order with no inner LLM. Args are substituted before each call:\n  - {param_name}    → caller's arg value\n  - $N              → entire output of step N (1-indexed)\n  - $N.field.path   → JSON field path into step N's output (empty if not JSON / path misses)\n  - $name / $name.field → same as $N forms but referencing step's optional Name\nRight for linear chains (\"search → fetch → summarize\") where no reasoning between steps. Cheaper + faster + predictable vs pipeline_prompt. Mutually exclusive with pipeline_prompt.\nExample: [{\"tool\": \"web_search\", \"args\": {\"query\": \"{topic}\"}, \"name\": \"results\"}, {\"tool\": \"fetch_url\", \"args\": {\"url\": \"$results.top_url\"}}, {\"tool\": \"summarize\", \"args\": {\"text\": \"$2\"}}]",
					Items:       &ToolParam{Type: "object"},
				},
				"allowed_tools": {
					Type:        "array",
					Description: "Names of tools the sub-agent may call during the pipeline. Subset of the standard worker catalog. Example: [\"web_search\", \"fetch_url\", \"browse_page\"].",
					Items:       &ToolParam{Type: "string"},
				},
				"max_rounds": {
					Type:        "integer",
					Description: "Optional cap on the sub-agent's LLM rounds. Default 6 — enough for a 3-4 step flow with retries. Raise to 10-12 only for genuinely complex flows.",
				},
				"for_agent": {
					Type:        "string",
					Description: "Optional override for the target agent id or exact name. When omitted, the framework uses the session's authoring focus (the agent your most recent get_agent / create_agent call selected). Pass this explicitly only when you need to attach the pipeline to a DIFFERENT agent than the one currently in focus. If neither this nor authoring focus is set, the call errors out — pipelines must have a target.",
				},
			},
			Required: []string{"name", "description", "allowed_tools"},
			Caps:     []Capability{CapWrite},
		},
		Handler: func(args map[string]any) (string, error) {
			if pipelineAuthoringDisabled {
				return "", errors.New("create_pipeline_tool is currently disabled for diagnostic testing — pipeline-mode tool authoring is off across the platform right now. Author the same logic using create_temp_tool (shell) or create_api_tool (api) instead, or hand the design to the user and let them create the pipeline tool manually via the admin UI later")
			}
			name := strings.TrimSpace(stringArg(args, "name"))
			if name == "" {
				return "", errors.New("name is required")
			}
			desc := strings.TrimSpace(stringArg(args, "description"))
			prompt := strings.TrimSpace(stringArg(args, "pipeline_prompt"))
			steps := pipelineStepsFromArgs(args, "pipeline_steps")
			if prompt == "" && len(steps) == 0 {
				return "", errors.New("either pipeline_prompt (adaptive LLM-driven) or pipeline_steps (deterministic) is required — pick one based on whether the chain needs reasoning between steps")
			}
			toolNames := stringSliceFromArgs(args, "allowed_tools")
			if len(toolNames) == 0 {
				return "", errors.New("allowed_tools must include at least one tool name")
			}
			// Validate step tool names against the allowlist for early
			// feedback — the runtime catches this too, but failing here
			// during authoring gives the LLM a chance to fix immediately.
			if len(steps) > 0 {
				allowed := map[string]bool{}
				for _, n := range toolNames {
					allowed[n] = true
				}
				for i, s := range steps {
					if !allowed[s.Tool] {
						return "", fmt.Errorf("pipeline_steps[%d].tool %q is not in allowed_tools %v — add it to allowed_tools or pick a different tool", i, s.Tool, toolNames)
					}
				}
			}
			maxRounds := intFromArgs(args, "max_rounds")
			params := paramsFromArgs(args, "params")
			required := requiredFromParams(params)
			tt := TempTool{
				Name:              name,
				Description:       desc,
				Params:            params,
				Required:          required,
				Mode:              TempToolModePipeline,
				PipelinePrompt:    prompt,
				PipelineSteps:     steps,
				PipelineTools:     toolNames,
				PipelineMaxRounds: maxRounds,
			}

			// Resolve target agent — agent-scoped is the ONLY persistence
			// path for pipelines authored from a chat conversation:
			//   1. Explicit for_agent in this call.
			//   2. The session's authoring-in-progress slot — set when
			//      create_agent (or get_agent) fired earlier this session.
			//   3. Otherwise refuse — no user-wide-with-admin-approval
			//      branch from the LLM surface. That path stranded the
			//      tool name on the agent's allowed_tools (it didn't
			//      resolve until admin approval), and the LLM looped
			//      trying to recover.
			forAgent := strings.TrimSpace(stringArg(args, "for_agent"))
			autoDefaulted := false
			if forAgent == "" && t.session != nil && t.session.AuthoringAgentID != "" {
				forAgent = t.session.AuthoringAgentID
				autoDefaulted = true
			}
			if forAgent == "" {
				return "", errors.New("create_pipeline_tool needs an agent to attach to — call get_agent (to modify an existing agent) or create_agent (to make a new one) first, which sets the authoring focus automatically for this session. Or pass for_agent=\"<id-or-name>\" explicitly. Pipelines must be agent-scoped; user-wide pipeline tools are admin-authored via the admin UI, not via this chat tool")
			}

			// Resolve + authorize the target.
			target, ok := findAgentByNameOrID(t.udb, t.user, forAgent)
			if !ok {
				return "", fmt.Errorf("for_agent=%q not found in your agents — call list_agents to see what's available, or create the agent first", forAgent)
			}
			if target.Owner != t.user {
				return "", fmt.Errorf("for_agent=%q is a read-only seed agent — clone it first via clone_agent, then attach the tool to the clone", forAgent)
			}

			// Install as a session-scoped draft so the LLM can dispatch
			// the tool BY NAME on the next round to verify before
			// declaring success. The canonical save lives on the agent
			// record (below); the session draft is purely a verification
			// handle and gets shadowed by the committed copy.
			if t.session != nil {
				if err := SaveSessionTempTool(t.udb, t.session.ID, tt); err != nil {
					Log("[orchestrate.pipeline] session draft save failed for %q: %v", tt.Name, err)
				}
			}

			// Attach to the agent's tools[]. Idempotent replace by name.
			replaced := false
			for i, existing := range target.Tools {
				if existing.Name == tt.Name {
					target.Tools[i] = tt
					replaced = true
					break
				}
			}
			if !replaced {
				target.Tools = append(target.Tools, tt)
			}
			if _, err := saveAgent(t.udb, target); err != nil {
				return "", fmt.Errorf("save agent: %v", err)
			}
			verb := "attached"
			if replaced {
				verb = "replaced"
			}
			prefix := ""
			if autoDefaulted {
				prefix = fmt.Sprintf("(auto-targeted %q — the agent you're currently authoring in this session) ", target.Name)
			}
			return fmt.Sprintf("%sPipeline tool %q %s on agent %q AND installed as a draft in this session — you can call %q with sample args on the next round to verify it works. Re-call create_pipeline_tool with the same name to iterate. When you're satisfied, END THE TURN with a one-line summary; the agent's saved copy is already the canonical version.", prefix, tt.Name, verb, target.Name, tt.Name), nil
		},
	}
}

// pipelineStepsFromArgs coerces the LLM-supplied pipeline_steps
// array into []PipelineStep. Round-trips through JSON to normalize
// loose types. Entries without a tool name are silently dropped.
func pipelineStepsFromArgs(args map[string]any, key string) []PipelineStep {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		Log("[orchestrate.pipeline] pipeline_steps marshal failed: %v", err)
		return nil
	}
	var steps []PipelineStep
	if err := json.Unmarshal(data, &steps); err != nil {
		Log("[orchestrate.pipeline] pipeline_steps unmarshal failed: %v", err)
		return nil
	}
	out := make([]PipelineStep, 0, len(steps))
	for _, s := range steps {
		s.Tool = strings.TrimSpace(s.Tool)
		if s.Tool == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// paramsFromArgs coerces the LLM-supplied `params` object into the
// map[string]ToolParam shape TempTool expects. Forgiving — entries
// without an explicit type default to "string"; unknown keys on the
// value object are ignored.
func paramsFromArgs(args map[string]any, key string) map[string]ToolParam {
	raw, ok := args[key].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make(map[string]ToolParam, len(raw))
	for k, v := range raw {
		obj, ok := v.(map[string]any)
		if !ok {
			continue
		}
		p := ToolParam{Type: "string"}
		if t, ok := obj["type"].(string); ok && t != "" {
			p.Type = t
		}
		if d, ok := obj["description"].(string); ok {
			p.Description = d
		}
		out[k] = p
	}
	return out
}

// requiredFromParams extracts the names whose param objects have
// `"required": true`. Walks args["params"] from scratch because the
// converted map[string]ToolParam doesn't carry the required flag.
func requiredFromParams(params map[string]ToolParam) []string {
	// The required flag lives on the source map; without it we can't
	// recover the list. Callers that need explicit required passing
	// should re-state it; for the MVP we treat all params as optional
	// unless the LLM passes a separate "required" arg.
	_ = params
	return nil
}
