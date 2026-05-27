// `pipeline` — grouped tool for declarative multi-stage pipelines
// (core.PipelineDef). Single entry point with an action discriminator,
// same shape as the `agents` tool:
//
//   create / update — author a pipeline (name, description, stages[]).
//   list            — see the user's pipelines.
//   get             — read one pipeline's full definition.
//   run             — execute a pipeline on an input, return the result.
//   delete          — remove a pipeline.
//
// This is the DECLARATIVE multi-stage workflow surface (decompose →
// stages → synthesize), distinct from the legacy create_pipeline_tool
// (a sub-agent-as-one-tool macro, now folded into add_tool). The two
// don't collide: create_pipeline_tool isn't surfaced in the catalog.
//
// Worker stages run as plain WorkerChat calls; agent stages dispatch
// through RunAgentSync via the PipelineDispatch hook wired below.

package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func (t *chatTurn) pipelineGroupedToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "pipeline",
			Description: "Author and run multi-stage pipelines — reusable workflows that chain stages (decompose → investigate → synthesize, etc.), where each stage is a worker LLM step or a dispatch to one of your agents, and outputs thread forward. Actions: create (author a new pipeline), update (revise one), list (see yours), get (read one's stages), run (execute on an input and get the result), delete. Pick the action that matches the intent.\n\nUse a pipeline when the work is a repeatable multi-step shape worth saving — not for a one-off question (answer that directly) and not for a single specialist task (dispatch to an agent). A pipeline pays off when the same staged flow runs more than once.",
			Parameters: map[string]ToolParam{
				"action": {Type: "string", Description: "One of: create | update | list | get | run | delete | help."},
				"name":   {Type: "string", Description: "Pipeline name. Required for create/update/get/run/delete (get/run/delete also accept the id)."},
				"id":     {Type: "string", Description: "(update/get/run/delete) Pipeline id, if you have it instead of the name."},
				"description": {Type: "string", Description: "(create/update) One-line summary of what the pipeline does / when to use it."},
				"input": {Type: "string", Description: "(run) The input fed to the pipeline's first stage and available as {input} in every stage prompt."},
				"stages": {
					Type: "array",
					Description: "(create/update) Ordered stages. Each is an object: {\"name\": unique label, \"kind\": \"worker\"|\"agent\", \"prompt\": instruction, \"agent\": agent name/id (for kind=agent)}. Prompt templating: {input} = pipeline input, {prev} = previous stage's output, {stage:NAME} = a named earlier stage's output. Stages run in order; each output is captured under its name. Worker stages are a plain LLM call (cheap, portable); agent stages dispatch to one of your agents (its persona + tools).",
					Items:       &ToolParam{Type: "object"},
				},
			},
			Required: []string{"action"},
			// CapNetwork: a run can dispatch agent stages whose tools
			// reach the network. Tag so private mode filters it.
			Caps: []Capability{CapNetwork},
		},
		Handler: func(args map[string]any) (string, error) {
			action := strings.ToLower(strings.TrimSpace(stringArg(args, "action")))
			switch action {
			case "create", "update":
				return t.pipelineCreateOrUpdate(args, action == "update")
			case "list":
				return t.pipelineList()
			case "get":
				return t.pipelineGet(args)
			case "run":
				return t.pipelineRun(args)
			case "delete":
				return t.pipelineDelete(args)
			case "help", "":
				return pipelineHelpText, nil
			default:
				return "", fmt.Errorf("unknown action %q — use create | update | list | get | run | delete | help", action)
			}
		},
	}
}

const pipelineHelpText = `pipeline actions:
- create  {name, description, stages:[{name, kind, prompt, agent?}]} — author a pipeline.
- update  {name|id, ...} — revise a pipeline's fields/stages.
- list    — your pipelines: [{id, name, description, stages}].
- get     {name|id} — one pipeline's full definition.
- run     {name|id, input} — execute it, returns the final stage's output.
- delete  {name|id}.

Stage prompt templating: {input}, {prev}, {stage:NAME}.
Stage kinds: worker (plain LLM step) | agent (dispatch to one of your agents).`

// pipelineCreateOrUpdate parses the stages array and saves a PipelineDef.
// On update, loads the existing def (by id or name) and overwrites the
// provided fields; on create, mints a fresh def owned by the user.
func (t *chatTurn) pipelineCreateOrUpdate(args map[string]any, isUpdate bool) (string, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	if name == "" && !isUpdate {
		return "", errors.New("name is required to create a pipeline")
	}
	stages, err := parsePipelineStages(args["stages"])
	if err != nil {
		return "", err
	}

	var def PipelineDef
	if isUpdate {
		existing, ok := t.findPipeline(args)
		if !ok {
			return "", errors.New("no matching pipeline to update — check the name/id (pipeline action=list)")
		}
		def = existing
		if name != "" {
			def.Name = name
		}
	} else {
		def = PipelineDef{Name: name, Owner: t.user}
	}
	if d := strings.TrimSpace(stringArg(args, "description")); d != "" {
		def.Description = d
	}
	if len(stages) > 0 {
		def.Stages = stages
	}
	if err := def.Validate(); err != nil {
		return "", fmt.Errorf("pipeline is not runnable: %w", err)
	}
	def.Owner = t.user
	saved := SavePipelineDef(t.udb, def)
	verb := "Created"
	if isUpdate {
		verb = "Updated"
	}
	return fmt.Sprintf("%s pipeline %q (%d stage%s, id %s). Run it with pipeline(action=\"run\", name=%q, input=…).",
		verb, saved.Name, len(saved.Stages), plural(len(saved.Stages)), saved.ID, saved.Name), nil
}

func (t *chatTurn) pipelineList() (string, error) {
	defs := ListPipelineDefs(t.udb, t.user)
	if len(defs) == 0 {
		return "No pipelines yet. Author one with pipeline(action=\"create\", name=…, stages=[…]).", nil
	}
	type row struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Stages      int    `json:"stages"`
	}
	out := make([]row, len(defs))
	for i, d := range defs {
		out[i] = row{ID: d.ID, Name: d.Name, Description: d.Description, Stages: len(d.Stages)}
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func (t *chatTurn) pipelineGet(args map[string]any) (string, error) {
	def, ok := t.findPipeline(args)
	if !ok {
		return "", errors.New("no matching pipeline — check the name/id (pipeline action=list)")
	}
	b, _ := json.Marshal(def)
	return string(b), nil
}

func (t *chatTurn) pipelineDelete(args map[string]any) (string, error) {
	def, ok := t.findPipeline(args)
	if !ok {
		return "", errors.New("no matching pipeline to delete")
	}
	DeletePipelineDef(t.udb, def.ID)
	return fmt.Sprintf("Deleted pipeline %q.", def.Name), nil
}

// pipelineRun executes a pipeline synchronously and returns the final
// stage's output. Agent stages dispatch through RunAgentSync (scoped
// to this user). Sync because the LLM called it inline and wants the
// result to continue its turn; the async path is for the
// attach-to-agent / UI surfaces.
func (t *chatTurn) pipelineRun(args map[string]any) (string, error) {
	def, ok := t.findPipeline(args)
	if !ok {
		return "", errors.New("no matching pipeline to run — check the name/id (pipeline action=list)")
	}
	input := strings.TrimSpace(stringArg(args, "input"))
	if input == "" {
		return "", errors.New("input is required to run a pipeline")
	}
	dispatch := func(ctx context.Context, agentID, stageInput string) (string, error) {
		// Agent stages run as the same user; RunAgentSync resolves the
		// agent from the user's store and dispatches with isolated
		// sub-session state.
		return t.app.RunAgentSync(ctx, t.user, t.user, agentID, stageInput)
	}
	ctx, cancel := context.WithTimeout(t.ctx, knowledgeIngestTimeout*8)
	defer cancel()
	status := func(s string) { t.emitStatus("[" + def.Name + "] " + s) }
	out, err := t.app.RunPipelineDefSync(ctx, def, input, dispatch, status)
	if err != nil {
		return "", fmt.Errorf("pipeline %q failed: %w", def.Name, err)
	}
	return out, nil
}

// findPipeline resolves a pipeline from the args by id first, then by
// case-insensitive name match.
func (t *chatTurn) findPipeline(args map[string]any) (PipelineDef, bool) {
	if id := strings.TrimSpace(stringArg(args, "id")); id != "" {
		if d, ok := LoadPipelineDef(t.udb, t.user, id); ok {
			return d, true
		}
	}
	name := strings.TrimSpace(stringArg(args, "name"))
	if name == "" {
		return PipelineDef{}, false
	}
	for _, d := range ListPipelineDefs(t.udb, t.user) {
		if strings.EqualFold(d.Name, name) {
			return d, true
		}
	}
	return PipelineDef{}, false
}

// parsePipelineStages converts the LLM-supplied stages array (a
// []any of objects) into []PipelineStage. Tolerant of missing kind
// (defaults to worker) so a minimal stage spec still runs.
func parsePipelineStages(raw any) ([]PipelineStage, error) {
	if raw == nil {
		return nil, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, errors.New("stages must be an array of stage objects")
	}
	out := make([]PipelineStage, 0, len(arr))
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("stage %d must be an object {name, kind, prompt, agent?}", i+1)
		}
		kind := PipelineStageKind(strings.ToLower(strings.TrimSpace(fmt.Sprint(mapStr(m, "kind")))))
		if kind == "" {
			kind = StageWorker
		}
		out = append(out, PipelineStage{
			Name:    strings.TrimSpace(mapStr(m, "name")),
			Kind:    kind,
			Prompt:  mapStr(m, "prompt"),
			Agent:   strings.TrimSpace(mapStr(m, "agent")),
			FanOver: strings.TrimSpace(mapStr(m, "fan_over")),
		})
	}
	return out, nil
}

// mapStr pulls a string field from a decoded JSON object, coercing
// non-string scalars to their string form. Empty for missing/nil.
func mapStr(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
