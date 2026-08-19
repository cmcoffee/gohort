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
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func (t *chatTurn) pipelineGroupedToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "pipeline",
			Description: "Author and run multi-stage pipelines — reusable workflows that chain stages (decompose → investigate → synthesize, etc.), where each stage is a worker LLM step or a dispatch to one of your agents, and outputs thread forward. Actions: create (author a new pipeline), update (revise one), list (see yours), get (read one's stages), run (execute on an input and get the result), delete. Pick the action that matches the intent.\n\nUse a pipeline when the work is a repeatable multi-step shape worth saving — not for a one-off question (answer that directly) and not for a single specialist task (dispatch to an agent). A pipeline pays off when the same staged flow runs more than once.\n\n**When building a pipeline FOR a specific agent, pass `attach_to_agents` in the same call** — that's the one-shot wire-up. Forgetting to attach is the classic failure mode: the pipeline exists in storage but the agent can't see it in the next session.",
			Parameters: map[string]ToolParam{
				"action":      {Type: "string", Description: "One of: create | update | list | get | run | delete | help."},
				"name":        {Type: "string", Description: "Pipeline name. Required for create/update/get/run/delete (get/run/delete also accept the id)."},
				"id":          {Type: "string", Description: "(update/get/run/delete) Pipeline id, if you have it instead of the name."},
				"full":        {Type: "boolean", Description: "(get) When true, return the COMPLETE definition with every stage's full prompt. Default false returns a compact view (stage prompts previewed) to save context. Use full=true only to read stage prompts you didn't write this session (e.g. editing an existing pipeline)."},
				"description": {Type: "string", Description: "(create/update) One-line summary of what the pipeline does / when to use it."},
				"input":       {Type: "string", Description: "(run) The input fed to the pipeline's first stage and available as {input} in every stage prompt."},
				"stages": {
					Type:        "array",
					Description: "(create/update) Ordered stages, each an object. Common shape: {\"name\": unique label, \"kind\": \"worker\"|\"agent\"|\"fanout\"|\"loop\"|\"branch\"|\"tool\", \"prompt\": instruction}. Kinds: worker = a plain LLM step (the default); agent = dispatch to one of your agents (set \"agent\"); fanout = run the prompt once per element of an earlier list, in parallel (set \"fan_over\", use {item}); loop = repeat a nested \"body\" of stages, each pass seeing the last (set \"count\" as the ceiling, \"until\" to stop early); a fanout may ALSO take a \"body\", run once per item, when each item needs several steps rather than one prompt. WHEN THE NUMBER OF REPETITIONS IS NOT KNOWN AS YOU WRITE THE PIPELINE — \"keep going until the critic is satisfied\", \"until they agree\", \"up to five rounds\" — that is kind=loop, NOT five hand-written copies of the same two stages. The copies cannot stop early, cannot say which pass they are, and silently become a fixed-length pipeline the user was not promised; machine = run a stored machine as this stage (set \"machine\"), for work that carries state between its own steps; branch = no LLM call, read a bool and stop or skip (set \"when\"); tool = call one of your tools directly with \"args\" you write (no LLM, no tokens). Templating: {input}, {prev}, {stage:NAME}, {stage:NAME.field}, {item}, {iteration} — plus {field_name} for every field of the submit form when this pipeline backs an app (that is how a run takes parameters, not just a question). Any stage may declare \"output\": [{name,type,desc,required}] to return validated JSON whose fields later stages read as {stage:NAME.field} — that is what makes fan_over-a-field, loop \"until\", and branch \"when\" possible. Worker stages inherit the calling agent's tools; set \"tools\" to restrict, \"think\" for deliberation, \"model\":\"lead\" for the precision tier on the stages that earn it. **Call action=\"help\" for the full spec** — every field, the caps, and the canonical shapes.",
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
- create  {name, description, stages:[...], attach_to_agents?:[names], replaces?:name|id} — author a pipeline.
- update  {name|id, ..., attach_to_agents?:[names]} — revise in place (same id, attachments stay).
- list    — your pipelines: [{id, name, description, stages}].
- get     {name|id, full?:true} — one pipeline's definition.
- run     {name|id, input} — execute it, returns the final stage's output.
- delete  {name|id}.

When building a pipeline FOR an agent, pass attach_to_agents in the same call — that wires it so future sessions see it as run_<pipeline>.

In-place edit vs retire-and-replace: use action=update when iterating on the SAME pipeline (attachments stay). Use action=create with replaces=<old-name|id> when the new pipeline has a different name/design and the old should be retired. Don't create a v2 without replaces — that leaves v1 attached as dead weight.

=== STAGE FIELDS ===
name       unique label; also the key later stages read as {stage:NAME}. No dots.
kind       worker (default) | agent | fanout | loop | branch | tool
prompt     the instruction (not used by branch or tool)
agent      agent name/id — for kind=agent, optionally for kind=fanout
tools      restrict a worker stage to a subset of the caller's catalog; [] = none
think      true on stages that genuinely reason (synthesis, verification, decomposition)
model      "worker" (default) | "lead" — the precision tier
output     [{name, type, desc, required, enum?, from?}] — declare a validated JSON result.
           "enum": [values] constrains a string field to a fixed set, checked where the decoder can
           still repair it. "from": "{input}" | "{prev}" | "{stage:NAME.field}" FILLS the field from
           a value the pipeline already holds instead of asking the model for it — it is left out of
           the contract entirely and merged into the result, so later stages read it the same way.
           A value you already have is not worth a model's attention, and asking invites a paraphrase
fan_over   (fanout) an earlier stage, or one of its list fields: "plan.queries"
body       (loop) nested stage list, repeated
count      (loop) required, 1-25 — the hard ceiling
until      (loop) a body stage's bool field; stops early when true
collect    (loop) "last" (default) | "all" (passes joined as ## Pass N)
when       (branch) required bool field on an EARLIER stage
skip_to    (branch) a LATER stage name; omit to end the pipeline
tool       (tool) the tool to call
args       (tool) {param: template}

=== TEMPLATING ===
{input} pipeline input · {prev} previous stage's output · {stage:NAME} a named stage's output
{stage:NAME.field} one declared field · {item} current element (fanout) · {iteration}/{iterations} (loop body)
Every reference is checked when the pipeline is SAVED, so a typo is an authoring error, not a mid-run surprise.

=== STRUCTURED OUTPUT ===
Give a stage "output": [{"name": lowercase_key, "type": "string"|"number"|"bool"|"list"|"object", "desc": what goes in it, "required": bool}] and it is asked for JSON with those keys, validated, and each field becomes {stage:NAME.field} downstream. Use it when a later stage needs ONE PIECE of an earlier result — a list to fan over, a count, a verdict, a title. This is what makes fan_over-a-field, loop until, and branch when possible. A stage that declares output renders its own {stage:NAME} as JSON, so point fan_over at the field ("plan.queries"). Nested fields go one level deep. Not valid on fanout, loop, or branch. Skip it for prose stages (a draft, a summary) — wrapping prose in a JSON envelope buys nothing. NEVER ask for JSON in the prompt as well: declaring the fields IS the mechanism, so a prompt that also specifies a format is two sets of formatting rules, and the usual result is a JSON string nested inside a JSON field. Say what to FIND; the framework handles the shape.

=== FANOUT (breadth) ===
Runs its prompt once PER ELEMENT of an earlier list, in parallel, then collects into one labeled block. Point fan_over at the stage (whose prompt emits a JSON array) or at a declared list field, and use {item}. A branch runs as a worker over the stage's tools by default; name an agent to dispatch each one instead. Capped at 12 items / 6 concurrent; per-branch errors are non-fatal. Canonical: decompose (emits JSON list) -> fanout (worker[web_search,fetch_url], "Research: {item}") -> synthesize (tools=[], think).
A fanout may instead carry a "body" of stages, run ONCE PER ITEM in its own scope — for when each item needs several steps rather than one prompt ("search it, read it, then judge it"). Body stages see {item}, {branch}, {branches}, everything established BEFORE the fan, and each other WITHIN their own branch; two branches running a stage of the same name never see each other's value, and nothing after the fanout may reference a body stage by name. Body OR agent, not both. Bodies do not nest (no loop or fanout inside one). When the LAST body stage declares output, the fanout also carries the per-branch results as {stage:NAME.items} — one object per branch plus "branch" and "item" — which is how you rank what came back, or fan over the survivors with fan_over:"NAME.items". Mind the multiplication: 12 items x a 4-stage body is 48 model calls.

=== LOOP (depth) ===
Repeats "body", each pass seeing the last via {prev}. count is required and is the hard ceiling (1-25) — a pipeline runs unattended, so the model does not get to decide when to stop. until is an early exit reading a body stage's bool. collect="all" joins every pass, which is what you want when building a transcript. Loops do not nest. Body stage names are per-pass and CANNOT be referenced after the loop — read the loop's own name.

=== BRANCH (control flow, no LLM call) ===
when = a bool field an EARLIER stage declared. True takes the branch: skip_to jumps to a LATER stage, or omit skip_to to end the pipeline (returning the last stage's output, so a screening stage's rejection IS the answer). Jumps are forward-only; repeating work is what loop is for. Inside a loop body a branch may only skip within the pass — use the loop's until to stop early.

=== MACHINE (a whole run as a stage) ===
machine = the name of a stored MACHINE, which runs with its own steps and its own working set,
and its last step's result becomes this stage's. The machine must be marked "this RUNS instead of
converses" — a stage has nobody waiting in it. Depth is capped: a machine already running this
pipeline cannot start another. Use it when the work is a smaller version of the same shape, and
put it in a FANOUT BODY when there is one per item — that is how N gaps get filled at once rather
than one after another. When the machine's last step declares the fields this stage declares, they
come straight across with nothing re-read out of prose.

=== TOOL (no LLM call, no tokens) ===
tool = one of the invoking agent's tools; args = {param: template}. For computation rather than judgment: arithmetic, dedup, normalization, a formatting pass, one specific API call. Asking a worker stage to do arithmetic is the classic waste — call the calculator. May declare output to decode a JSON-returning tool, with no repair retry (there is no model to ask again). A missing tool is a run-time error listing what the caller does have.

=== TIERS + TOOLS ===
Worker stages INHERIT the calling agent's catalog — a pipeline invoked from an agent with web_search/fetch_url has them automatically. Set "tools" to restrict (a synthesis stage that shouldn't fetch sets tools=[]). model="lead" puts a stage on the precision tier; use it on decompose / synthesize / judge and leave transforms on worker, because lead everywhere is how a cheap pipeline stops being cheap. think defaults false — turn it on selectively, not everywhere.`

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
	createdViaUpdate := false
	if isUpdate {
		existing, ok := t.findPipeline(args)
		switch {
		case ok:
			def = existing
			if name != "" {
				def.Name = name
			}
		case name != "" && len(stages) > 0:
			// Upsert. After a REFUSED create the definition was never stored,
			// and the reflex is to "fix it" with update — which used to spend a
			// round discovering there was nothing to fix. Saying so in the
			// error did not stop it (one observed build made the same call
			// twice in six rounds), and the intent is not ambiguous: a name
			// plus a full stage list is a complete definition. Store it and
			// name what happened, so the transcript still shows a create.
			createdViaUpdate = true
			isUpdate = false
			def = PipelineDef{Name: name, Owner: t.user}
		default:
			// A partial patch with nothing to patch is still an error: there is
			// no definition here to save, only edits to one that doesn't exist.
			return "", errors.New("no matching pipeline to update — nothing is stored under that name/id, and this call carries no stages to store as a new one. pipeline(action=\"list\") shows what you actually have")
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
	if createdViaUpdate {
		verb = "Created (nothing was stored under that name yet, so this was saved as a new pipeline rather than an edit)"
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
	// A pipeline whose whole body is one tool call has composed nothing: it is a
	// wrapper around whatever that tool already did, and it inherits that tool's
	// behavior including the parts that do not exist. Observed exactly once and
	// shipped: a "polish this through five rounds" pipeline that was a single
	// tool stage over a stub whose body printed {"status":"started"} — the
	// stage list said one thing, the tool's DESCRIPTION promised five rounds,
	// and the summary repeated the description.
	if len(saved.Stages) == 1 && saved.Stages[0].Kind == StageTool {
		msg += " WARNING: this pipeline is ONE tool stage — it composes nothing and does exactly what " +
			strconv.Quote(saved.Stages[0].Tool) + " already does, no more. A tool stage is a STEP (arithmetic, a lookup, one API call), not a whole pipeline. If the user asked for rounds, passes or multi-step work, that work belongs in stages here — read the tool's script body, not its description, before trusting it to do it for you."
	}
	if len(currentAttachments) == 0 {
		msg += " WARNING: this pipeline is not attached to ANY agent — if it's meant for one, call pipeline(action=\"update\", name=" + saved.Name + ", attach_to_agents=[\"<agent_name>\"]) to wire it up."
	} else {
		msg += fmt.Sprintf(" Currently attached to: %s.", strings.Join(currentAttachments, ", "))
	}
	// The soft half. Validate has already refused anything unrunnable,
	// so what is left is what the pipeline looks like it might not have
	// meant — and on this path the author is a MODEL, which makes the
	// one rule here (a prompt hand-rolling the JSON its declared fields
	// already produce) a mistake this surface is the most likely to
	// produce. The machine tool says the same thing about the same
	// mistake, from the same function.
	msg += pipelineAdviceNote(saved)
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
		// So "which of my pipelines needs a look" is answerable without
		// getting every one of them. Omitted when there is nothing to
		// say, which is the common case.
		WorthALook int `json:"worth_a_look,omitempty"`
	}
	out := make([]row, len(defs))
	for i, d := range defs {
		out[i] = row{ID: d.ID, Name: d.Name, Description: d.Description,
			Stages: len(d.Stages), WorthALook: len(d.Advice())}
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
	// The advice travels with the definition, because a pipeline written
	// before the rule existed will never see a create reply again — get
	// is the only place its findings can reach anybody.
	if b, ok := args["full"].(bool); ok && b {
		full, _ := json.Marshal(def)
		return string(full) + pipelineAdviceNote(def), nil
	}
	return string(slimPipelineJSON(def)) + pipelineAdviceNote(def), nil
}

// pipelineAdviceNote is the soft half, in the words both tools use.
func pipelineAdviceNote(def PipelineDef) string {
	adv := def.Advice()
	if len(adv) == 0 {
		return ""
	}
	return "\n\nWorth a look — none of this stopped the save, and none of it is certain:\n- " +
		strings.Join(adv, "\n- ")
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
	// The same boundary the dispatch path is judged at (pipeline_guardrail.go).
	// That an attached pipeline was arranged in advance says who set it up, not
	// that it is outside the rules — otherwise "attach it instead of
	// dispatching it" would be the way around every guardrail an agent has.
	if err := t.guardPipelineInput(t.ctx, def, input); err != nil {
		return "", err
	}
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
	// The caller's rules ride down into the stages (pre_action), and the
	// synthesis is judged on the way back out. This path inherits the caller's
	// whole tool catalog, so it is the one where an ungoverned stage could act
	// with the most reach.
	ctx := t.guardedPipelineContext(t.ctx)
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
	return t.guardPipelineOutput(t.ctx, def, out)
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
		fields, err := parsePipelineFields(i+1, m["output"])
		if err != nil {
			return nil, err
		}
		// A loop's body is a nested stage list, parsed by the same
		// function — one level, which Validate enforces.
		var body []PipelineStage
		if raw, ok := m["body"]; ok && raw != nil {
			if body, err = parsePipelineStages(raw); err != nil {
				return nil, fmt.Errorf("stage %d (%s) body: %w", i+1, mapStr(m, "name"), err)
			}
		}
		out = append(out, PipelineStage{
			Name:    strings.TrimSpace(mapStr(m, "name")),
			Kind:    kind,
			Prompt:  mapStr(m, "prompt"),
			Agent:   strings.TrimSpace(mapStr(m, "agent")),
			FanOver: strings.TrimSpace(mapStr(m, "fan_over")),
			Tools:   mapStrList(m, "tools"),
			Think:   mapBoolPtr(m, "think"),
			Output:  fields,
			Body:    body,
			Count:   mapInt(m, "count"),
			Until:   strings.TrimSpace(mapStr(m, "until")),
			Collect: strings.ToLower(strings.TrimSpace(mapStr(m, "collect"))),
			When:    strings.TrimSpace(mapStr(m, "when")),
			SkipTo:  strings.TrimSpace(mapStr(m, "skip_to")),
			Model:   strings.ToLower(strings.TrimSpace(mapStr(m, "model"))),
			Tool:    strings.TrimSpace(mapStr(m, "tool")),
			Machine: strings.TrimSpace(mapStr(m, "machine")),
			Args:    mapStrMap(m, "args"),
		})
	}
	return out, nil
}

// parsePipelineFields decodes a stage's "output" declaration — the list
// of fields the stage promises to return. Accepts the full object form
// ({name, type, desc, required, fields}) and the shorthand a model
// reaches for first: a bare string, which becomes an optional string
// field of that name.
func parsePipelineFields(stageNum int, raw any) ([]PipelineField, error) {
	if raw == nil {
		return nil, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("stage %d: output must be an array of field objects", stageNum)
	}
	out := make([]PipelineField, 0, len(arr))
	for _, item := range arr {
		switch f := item.(type) {
		case string:
			out = append(out, PipelineField{Name: strings.TrimSpace(f), Type: FieldString})
		case map[string]any:
			nested, err := parsePipelineFields(stageNum, f["fields"])
			if err != nil {
				return nil, err
			}
			out = append(out, PipelineField{
				Name:     strings.TrimSpace(mapStr(f, "name")),
				Type:     PipelineFieldType(strings.ToLower(strings.TrimSpace(mapStr(f, "type")))),
				Desc:     mapStr(f, "desc"),
				Fields:   nested,
				Required: mapBool(f, "required"),
				// Both of these were dropped, and both are load-bearing.
				// enum is how a routing field DECLARES where it may send
				// a turn — without it the picture fans out to every step
				// and a bad target stops being a save-time error. from
				// fills a field from a variable instead of asking the
				// model for it, which is the whole point of the feature
				// the spec describes two paragraphs above.
				Enum: mapStrList(f, "enum"),
				From: strings.TrimSpace(mapStr(f, "from")),
			})
		default:
			return nil, fmt.Errorf("stage %d: each output entry must be a field object {name, type, desc?, required?} or a bare field name", stageNum)
		}
	}
	return out, nil
}

// mapInt reads an integer, tolerating the float form JSON decoding
// produces and the string form models sometimes send.
func mapInt(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
			return n
		}
	}
	return 0
}

// mapStrList pulls a string array from a decoded JSON object, tolerating
// the single-string form a model sometimes sends instead of a one-element
// array.
func mapStrList(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s := strings.TrimSpace(fmt.Sprint(e)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if s := strings.TrimSpace(t); s != "" {
			return []string{s}
		}
	}
	return nil
}

// mapBool reads a boolean, accepting the string forms models produce.
func mapBool(m map[string]any, key string) bool {
	b := mapBoolPtr(m, key)
	return b != nil && *b
}

// mapBoolPtr is mapBool for tri-state fields (nil = unset, which is
// distinct from false for PipelineStage.Think).
func mapBoolPtr(m map[string]any, key string) *bool {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	var b bool
	switch t := v.(type) {
	case bool:
		b = t
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "yes", "1":
			b = true
		case "false", "no", "0":
			b = false
		default:
			return nil
		}
	case float64:
		b = t != 0
	default:
		return nil
	}
	return &b
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

// mapStrMap pulls a {key: string} object from a decoded JSON object,
// coercing non-string values to their string form. Used for a tool
// stage's args, where every value renders as a template string.
func mapStrMap(m map[string]any, key string) map[string]string {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	raw, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, val := range raw {
		if val == nil {
			continue
		}
		if s, ok := val.(string); ok {
			out[k] = s
			continue
		}
		out[k] = fmt.Sprint(val)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
