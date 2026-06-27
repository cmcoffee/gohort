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
			Description: "Author and run multi-stage pipelines — reusable workflows that chain stages (decompose → investigate → synthesize, etc.), where each stage is a worker LLM step or a dispatch to one of your agents, and outputs thread forward. Actions: create (author a new pipeline), update (revise one), list (see yours), get (read one's stages), run (execute on an input and get the result), delete. Pick the action that matches the intent.\n\nUse a pipeline when the work is a repeatable multi-step shape worth saving — not for a one-off question (answer that directly) and not for a single specialist task (dispatch to an agent). A pipeline pays off when the same staged flow runs more than once.\n\n**When building a pipeline FOR a specific agent, pass `attach_to_agents` in the same call** — that's the one-shot wire-up. Forgetting to attach is the classic failure mode: the pipeline exists in storage but the agent can't see it in the next session.",
			Parameters: map[string]ToolParam{
				"action": {Type: "string", Description: "One of: create | update | list | get | run | delete | help."},
				"name":   {Type: "string", Description: "Pipeline name. Required for create/update/get/run/delete (get/run/delete also accept the id)."},
				"id":     {Type: "string", Description: "(update/get/run/delete) Pipeline id, if you have it instead of the name."},
				"full":   {Type: "boolean", Description: "(get) When true, return the COMPLETE definition with every stage's full prompt. Default false returns a compact view (stage prompts previewed) to save context. Use full=true only to read stage prompts you didn't write this session (e.g. editing an existing pipeline)."},
				"description": {Type: "string", Description: "(create/update) One-line summary of what the pipeline does / when to use it."},
				"input": {Type: "string", Description: "(run) The input fed to the pipeline's first stage and available as {input} in every stage prompt."},
				"stages": {
					Type: "array",
					Description: "(create/update) Ordered stages. Each is an object: {\"name\": unique label, \"kind\": \"worker\"|\"agent\"|\"fanout\", \"prompt\": instruction, \"agent\": agent name/id (for kind=agent, or optionally kind=fanout), \"tools\": optional array of tool names, \"think\": optional boolean, \"fan_over\": earlier stage name (for kind=fanout)}. Prompt templating: {input} = pipeline input, {prev} = previous stage's output, {stage:NAME} = a named earlier stage's output, {item} = the current element (fanout only). Stages run in order; each output is captured under its name.\n\nWorker stages INHERIT the calling agent's tool catalog by default — if a pipeline is invoked from an agent with web_search and fetch_url, its worker stages have those automatically. Set a stage's \"tools\" array to RESTRICT to a subset of the inherited pool (e.g. a synthesis stage that should not be tempted to fetch sets tools=[]). Agent stages dispatch to a named agent (full persona + that agent's catalog) — use when you want a sub-agent's complete behavior, not just a focused tool-equipped step.\n\nFANOUT does breadth: it runs its prompt once PER ELEMENT of an earlier stage's list output, in parallel, then collects the results into one labeled block for the next stage. Point \"fan_over\" at the earlier stage (whose prompt should emit a JSON array, e.g. 'Return a JSON array of sub-questions'), and use {item} in the fanout prompt for the current element. A fanout branch runs as a worker over the stage's resolved tools by default (so it can search/fetch per item); name an \"agent\" to dispatch each branch to a full agent instead. Branches are capped (12 items, 6 concurrent) and per-branch errors are non-fatal. Canonical shape: decompose (worker, emits JSON list) → fanout (worker[web_search,fetch_url], prompt 'Research: {item}') → synthesize (worker, tools=[], think).\n\nThe \"think\" flag (default false) enables thinking for synthesis / verification / decomposition stages that benefit from deliberation. Leave it off (default) for cheap transforms, format conversions, and tool-equipped fetches where reasoning is procedural. Setting think=true on every stage burns tokens for marginal value; turn it on selectively where the work genuinely needs reasoning.",
					Items:       &ToolParam{Type: "object"},
				},
				"attach_to_agents": {
					Type:        "array",
					Description: "(create/update) Optional list of agent names or IDs. After the pipeline saves, it's added to each named agent's attached_pipelines so the agent can call it as `run_<pipeline>` from its next session onward. Idempotent — already-attached pipelines aren't double-added. Unknown agent names get reported back in the result; the pipeline still saves. Use this whenever the pipeline is being built as part of an agent's surface so you don't have to remember a separate update_agent call.",
					Items:       &ToolParam{Type: "string"},
				},
				"replaces": {
					Type:        "string",
					Description: "(create) Optional name or id of an old pipeline this one supersedes. When set, every agent currently attaching the old pipeline gets it swapped out for the new one, and the old pipeline is then deleted. Atomic retire-and-replace — prevents the failure mode of writing a v2 pipeline and leaving v1 attached as dead weight. Use ONLY when the new pipeline has a different name/design than the old; for in-place edits use action=update instead (same ID, new stages, attachments stay automatically).",
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
- create  {name, description, stages:[{name, kind, prompt, agent?}], attach_to_agents?:[names], replaces?:name|id} — author a pipeline.
- update  {name|id, ..., attach_to_agents?:[names]} — revise a pipeline's fields/stages in place (same id, attachments stay).
- list    — your pipelines: [{id, name, description, stages}].
- get     {name|id} — one pipeline's full definition.
- run     {name|id, input} — execute it, returns the final stage's output.
- delete  {name|id}.

When building a pipeline FOR an agent, pass attach_to_agents in the same call — that wires it to the agent so future sessions see it as run_<pipeline>.

In-place edit vs. retire-and-replace: use action=update when iterating on the SAME pipeline (same name/id, new stages — attachments stay automatically). Use action=create with replaces=<old-name|id> when the new pipeline has a different name/design and the old one should be retired (swaps every agent's attachment from old to new, deletes the old). Don't create a v2 without replaces — that leaves v1 attached as dead weight on every agent that had it.

Stage prompt templating: {input}, {prev}, {stage:NAME}, {item} (fanout only).
Stage kinds: worker (plain LLM step) | agent (dispatch to one of your agents) | fanout (run the prompt per element of an earlier stage's JSON list, in parallel; set fan_over=<stage>, use {item}; worker by default, or name an agent). Canonical breadth shape: decompose → fanout → synthesize.`

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

	// Optional replaces pass — when Builder writes a v2 pipeline meant
	// to retire v1 (different name/design), this swaps every agent
	// attaching the old pipeline to the new one and deletes the old.
	// Only meaningful on create — on update the same record is being
	// overwritten in place, there's nothing to retire. Failures are
	// reported but never block the save (the new pipeline persists
	// regardless).
	replaced := ""
	replacedSwaps := []string{}
	if !isUpdate {
		oldKey := strings.TrimSpace(stringArg(args, "replaces"))
		if oldKey != "" && oldKey != saved.Name && oldKey != saved.ID {
			// Try id first, then case-insensitive name match — mirrors
			// findPipeline's lookup order so the same key Builder
			// passes for get/delete works here.
			oldDef, foundOld := LoadPipelineDef(t.udb, t.user, oldKey)
			if !foundOld {
				for _, d := range ListPipelineDefs(t.udb, t.user) {
					if strings.EqualFold(d.Name, oldKey) {
						oldDef = d
						foundOld = true
						break
					}
				}
			}
			if foundOld && oldDef.ID != saved.ID {
				// Walk every agent and swap attachments.
				for _, ag := range listAgents(t.udb, t.user) {
					if ag.Owner != t.user {
						continue
					}
					hadOld := false
					newAttached := ag.AttachedPipelines[:0:0]
					for _, pid := range ag.AttachedPipelines {
						if pid == oldDef.ID {
							hadOld = true
							continue
						}
						newAttached = append(newAttached, pid)
					}
					if !hadOld {
						continue
					}
					// Add new pipeline (idempotent — caller may have
					// also passed attach_to_agents covering this agent).
					alreadyHasNew := false
					for _, pid := range newAttached {
						if pid == saved.ID {
							alreadyHasNew = true
							break
						}
					}
					if !alreadyHasNew {
						newAttached = append(newAttached, saved.ID)
					}
					ag.AttachedPipelines = newAttached
					if _, err := saveAgent(t.udb, ag); err == nil {
						replacedSwaps = append(replacedSwaps, ag.Name)
					}
				}
				DeletePipelineDef(t.udb, oldDef.ID)
				replaced = oldDef.Name
			}
		}
	}

	// Optional attach pass — Builder's classic failure mode is to
	// author a pipeline FOR an agent and forget the separate
	// update_agent call to wire it up. By accepting attach_to_agents
	// here, the create+attach is one atomic operation that can't be
	// half-completed. Idempotent: pipelines already attached aren't
	// duplicated. Unknown agent names report back without failing the
	// save — the pipeline itself is persisted regardless of attachment
	// outcomes.
	targets := stringSliceFromArgs(args, "attach_to_agents")
	attached := []string{}
	missing := []string{}
	for _, key := range targets {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		agent, ok := findAgentByNameOrID(t.udb, t.user, key)
		if !ok {
			missing = append(missing, key)
			continue
		}
		already := false
		for _, pid := range agent.AttachedPipelines {
			if pid == saved.ID {
				already = true
				break
			}
		}
		if already {
			attached = append(attached, agent.Name+" (already attached)")
			continue
		}
		agent.AttachedPipelines = append(agent.AttachedPipelines, saved.ID)
		if _, err := saveAgent(t.udb, agent); err != nil {
			missing = append(missing, agent.Name+" (save failed: "+err.Error()+")")
			continue
		}
		attached = append(attached, agent.Name)
	}

	msg := fmt.Sprintf("%s pipeline %q (%d stage%s, id %s). Run it with pipeline(action=\"run\", name=%q, input=…).",
		verb, saved.Name, len(saved.Stages), plural(len(saved.Stages)), saved.ID, saved.Name)
	if replaced != "" {
		if len(replacedSwaps) > 0 {
			msg += fmt.Sprintf(" Retired %q and swapped attachments on: %s.", replaced, strings.Join(replacedSwaps, ", "))
		} else {
			msg += fmt.Sprintf(" Retired %q (no agents had it attached).", replaced)
		}
	}
	if len(attached) > 0 {
		msg += fmt.Sprintf(" Attached this call to: %s.", strings.Join(attached, ", "))
	}
	if len(missing) > 0 {
		msg += fmt.Sprintf(" Could not attach (unknown name or save error): %s.", strings.Join(missing, ", "))
	}

	// Always surface the CURRENT attachment state — independent of
	// whether this call passed attach_to_agents. Catches the failure
	// mode where Builder saves a pipeline meant for an agent and just
	// forgets to wire it; without this nudge, the orphaned pipeline
	// silently exists until the user notices in a future session.
	currentAttachments := []string{}
	for _, ag := range listAgents(t.udb, t.user) {
		if ag.Owner != t.user {
			continue
		}
		for _, pid := range ag.AttachedPipelines {
			if pid == saved.ID {
				currentAttachments = append(currentAttachments, ag.Name)
				break
			}
		}
	}
	if len(currentAttachments) == 0 {
		msg += " WARNING: this pipeline is not attached to ANY agent — if it's meant for one, call pipeline(action=\"update\", name=" + saved.Name + ", attach_to_agents=[\"<agent_name>\"]) to wire it up."
	} else {
		msg += fmt.Sprintf(" Currently attached to: %s.", strings.Join(currentAttachments, ", "))
	}
	return msg, nil
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
	// Default compact, full on demand — same posture as agents(get): a
	// full def echoes every stage's prompt, and the author→get→update→get
	// re-evaluate loop would accumulate them in context. full=true returns
	// the verbatim stage prompts (for editing an existing/inherited
	// pipeline you didn't write this session). See
	// project_long_context_management.
	if b, ok := args["full"].(bool); ok && b {
		full, _ := json.Marshal(def)
		return string(full), nil
	}
	return string(slimPipelineJSON(def)), nil
}

// slimPipelineJSON renders a PipelineDef for the pipeline(get) tool
// result: identity + per-stage name/kind/agent/fan_over intact, with each
// stage's prompt PREVIEWED (length-marked) rather than echoed in full.
func slimPipelineJSON(def PipelineDef) []byte {
	preview := func(s string, n int) string {
		s = strings.TrimSpace(s)
		if len(s) <= n {
			return s
		}
		return s[:n] + fmt.Sprintf("…[%d chars total — previewed; re-send the full stage prompt to change it, or get full=true to read it]", len(s))
	}
	type stageSummary struct {
		Name    string            `json:"name"`
		Kind    PipelineStageKind `json:"kind"`
		Prompt  string            `json:"prompt"`
		Agent   string            `json:"agent,omitempty"`
		FanOver string            `json:"fan_over,omitempty"`
	}
	stages := make([]stageSummary, 0, len(def.Stages))
	for _, s := range def.Stages {
		stages = append(stages, stageSummary{
			Name: s.Name, Kind: s.Kind, Prompt: preview(s.Prompt, 300),
			Agent: s.Agent, FanOver: s.FanOver,
		})
	}
	slim := map[string]any{
		"id":          def.ID,
		"name":        def.Name,
		"description": def.Description,
		"stages":      stages,
		"_note":       "Compact view: stage prompts are previewed to save context. To change a stage, re-send its full prompt via pipeline(action=\"update\"). To read a full stage prompt you didn't write this session, call pipeline(action=\"get\", full=true).",
	}
	b, _ := json.Marshal(slim)
	return b
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
	return t.runPipelineDefInline(def, input)
}

// runPipelineDefInline executes a pipeline def synchronously inside the
// current turn and returns the final stage's output. Shared by the
// grouped `pipeline` tool's run action and the per-agent attached-
// pipeline tools — both want the result inline to continue the turn,
// with per-stage progress narrated into the activity pane.
//
// Agent stages dispatch through RunAgentSync (scoped to this user). Sync
// (not RunPipelineDefAsync) because the LLM called this inline and needs
// the answer to keep going; orchestrate has no auto-promotion path to
// deliver an async result back into the turn, so a fire-and-forget run
// would strand the output. The per-stage status emits keep the user
// oriented during the (potentially slow) multi-stage run.
func (t *chatTurn) runPipelineDefInline(def PipelineDef, input string) (string, error) {
	dispatch := func(ctx context.Context, agentID, stageInput string) (string, error) {
		// Agent stages run as the same user; RunAgentSync resolves the
		// agent from the user's store and dispatches with isolated
		// sub-session state.
		return t.app.RunAgentSync(ctx, t.user, t.user, agentID, stageInput)
	}
	// No pipeline-level timeout wrap — operator-configurable timeouts
	// already govern execution at the right layer: each LLM call uses
	// the LLMProviderConfig.RequestTimeout (set via --setup / admin
	// UI), each stream has the StreamIdleTimeout watchdog, and the
	// parent chatTurn context is bound to the user's HTTP/SSE
	// connection. A hardcoded pipeline-wide deadline on top would just
	// race those without adding value, and would override what the
	// operator picked for the underlying LLM tier.
	ctx := t.ctx
	// Status callback fans out to BOTH the activity pane (SSE chip)
	// AND the diag log. Without the Log fan-out, pipeline stage events
	// vanished from gohort.log — making "did the pipeline actually run
	// these stages?" un-greppable. Same string in both places so the
	// SSE chip and log line correlate by content.
	status := func(s string) {
		t.emitStatus("[" + def.Name + "] " + s)
		Log("[orchestrate.pipeline %q] %s", def.Name, s)
	}
	// Inheritance — pipeline worker stages get the calling chat's
	// resolved worker-tool catalog by default. This is the "tools
	// flow down" behavior we wanted: if the agent that invoked the
	// pipeline has web_search and fetch_url, the pipeline's worker
	// stages have them too without per-stage configuration. A stage
	// CAN restrict to a subset by setting its own Tools field, which
	// gets intersected with the inherited pool.
	//
	// Wrapping the inherited tools with wrapToolsForActivity hooks
	// them into the parent chatTurn's infrastructure: toolCache
	// (so a search done in stage 1 of the pipeline short-circuits
	// when stage 2 makes the same call), dispatchCounts (so the
	// per-(name,args) cap applies across orchestrator + pipeline),
	// AND activity SSE (so the user sees the pipeline's tool calls
	// chip-by-chip in the activity pane, with a "↳ [pipeline]"
	// nesting prefix). Same wiring agents(run) sub-dispatches use.
	sess := t.newToolSession()
	defer t.captureActiveWorkspace(sess)
	inheritedTools, _, _ := t.resolveWorkerTools(sess, false)
	wrappedTools := t.wrapToolsForActivity(sess, inheritedTools, "↳ ["+def.Name+"] ")
	out, err := t.app.RunPipelineDefSyncWithTools(ctx, def, input, dispatch, status, wrappedTools)
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
