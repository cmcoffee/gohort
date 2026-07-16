// `agents` — single grouped tool consolidating list, get, and run
// (dispatch) operations against the user's agent fleet. Replaces the
// three separate tools (list_agents, get_agent, dispatch_to_agent)
// with one catalog entry and an action discriminator.
//
// Why grouped: list/get/run share a coherent subject (agents) with
// simple aligned schemas. Same pattern as tool_def for tool authoring.
// Trims three catalog entries down to one for every agent that has
// it — meaningful surface reduction at scale.
//
// chatTurn-bound (like dispatch_to_agent was) so the run action can
// track dispatch depth + apply the Builder-exclusivity gate.
//
// Backward compat: list_agents, get_agent, and dispatch_to_agent
// stay registered as separate tools — existing user agent records
// that explicitly name them in AllowedTools keep working. New
// agents reach for `agents` going forward.

package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/temptool"
)

// agentsGroupedToolDef builds the per-turn `agents` AgentToolDef. The
// handler dispatches on the `action` arg. When allowRun is false, the
// schema and handler are stripped of the `run` action — the tool is
// then read-only (list / get / help). Use the read-only variant for
// agents that author or compose but should not dispatch into the
// general fleet: Builder is the canonical case, where allowing run
// re-introduces the Builder → Chat → Builder cycle (Chat's
// authoring-intent routing sends control right back here). Builder
// delegates execution via plan_set workers instead.
func (t *chatTurn) agentsGroupedToolDef(allowRun bool) AgentToolDef {
	desc := "Manage and call other agents in the fleet. Three actions: list (see what agents exist), get (read one agent's full record + set authoring focus), run (delegate work to a named agent and get its synthesis back). Single entry point for agent operations — pick the action that matches the intent."
	if !allowRun {
		desc = "Inspect other agents in the fleet. Two actions: list (see what agents exist), get (read one agent's full record + set authoring focus). This catalog variant is READ-ONLY — dispatch (run) is intentionally disabled for this agent because its job is authoring/composition, not delegation. If you need to delegate execution work, use plan_set with worker steps; if you need a specialist's domain knowledge during authoring, dispatch a plan_set worker with web_search / fetch_url."
	}
	params := map[string]ToolParam{
		"action": {
			Type:        "string",
			Description: "One of: list | get | help.",
		},
		"id": {
			Type:        "string",
			Description: "(get) Agent id (from action=\"list\").",
		},
		"full": {
			Type:        "boolean",
			Description: "(get) When true, return the COMPLETE record — full orchestrator_prompt / plan_guidance / rules text and full tool definitions. Default false returns a compact view (prose previewed, tools by name) to save context. Use full=true only when you need to READ prose you didn't write this session — e.g. to edit an inherited prompt after clone_agent, or modify an agent from an earlier session. For agents you're authoring fresh, the compact view is enough.",
		},
	}
	caps := []Capability{CapRead}
	if allowRun {
		params["action"] = ToolParam{
			Type:        "string",
			Description: "One of: list | get | run | help.",
		}
		params["agent"] = ToolParam{
			Type:        "string",
			Description: "(run) Name or id of the agent to dispatch to.",
		}
		params["message"] = ToolParam{
			Type:        "string",
			Description: "(run) The question or task to send to the target agent. Phrase it as the user would phrase it directly; the sub-agent has its own persona and will frame the response. The sub-agent keeps its persona, saved facts, and knowledge base, and it re-threads your prior dispatches to it this session (ephemeral continuity), so a follow-up can be brief without repeating earlier context.",
		}
		// CapNetwork is tagged here even though the bare tool itself
		// doesn't make HTTP calls: the `run` action dispatches into a
		// sub-agent whose tools may. Without this cap, Private mode
		// would strip web_search / fetch_url from the calling agent
		// but leave `agents` available — the model could then dispatch
		// to a Research agent that runs web_search and leak the turn.
		// Tagging it CapNetwork closes that gap via the existing
		// Private-mode filter. Only relevant when run is permitted.
		caps = append(caps, CapNetwork)
	}
	// run_tool — a Builder-only allowance. Lets Builder execute ONE of a
	// target agent's attached tools directly, with explicit args, WITHOUT
	// dispatching a natural-language message and hoping the sub-agent's LLM
	// picks the right tool and formats the args. That indirect path costs a
	// full sub-agent turn per check, burns the dispatch cap, and conflates
	// "does the TOOL work" with "did the AGENT choose correctly" — exactly
	// the friction seen verifying the moltbook toolbox. Builder is the
	// authoring/verification agent, so it gets the direct seam; ordinary
	// fleet agents do not (they'd be reaching into another agent's kit).
	allowRunTool := isBuilderAgent(t.agent.ID)
	if allowRunTool {
		desc += " You (Builder) also have action=\"run_tool\": execute one of a target agent's attached tools directly with explicit args (tool + tool_args) to verify it works, without an LLM dispatch — the fast path for checking an authored agent's tools one by one."
		if _, ok := params["agent"]; !ok {
			params["agent"] = ToolParam{
				Type:        "string",
				Description: "(run/run_tool) Name or id of the target agent.",
			}
		}
		params["tool"] = ToolParam{
			Type:        "string",
			Description: "(run_tool) Name of the tool on the target agent to execute directly. For a toolbox, this is the toolbox name (e.g. \"moltbook\") and you pass the sub-action inside tool_args as {\"action\":\"<sub>\", ...}.",
		}
		params["tool_args"] = ToolParam{
			Type:        "object",
			Description: "(run_tool) Arguments to pass to the tool, as a JSON object keyed by the tool's param names. For a toolbox include \"action\". Runs the tool exactly as the target agent would, against its real credential/endpoint — a mutating action (POST, etc.) has real side effects, so verify with a read action first when unsure.",
		}
		// run_tool dispatches into the tool's own execution path (secure-API
		// / sandbox), so it may make network calls — tag it like run does so
		// Private mode strips it consistently.
		hasNet := false
		for _, c := range caps {
			if c == CapNetwork {
				hasNet = true
				break
			}
		}
		if !hasNet {
			caps = append(caps, CapNetwork)
		}
	}
	// Set the action enum description once, from whatever is actually
	// enabled for this agent, so the schema never advertises an action the
	// handler will refuse.
	{
		acts := []string{"list", "get"}
		if allowRun {
			acts = append(acts, "run")
		}
		if allowRunTool {
			acts = append(acts, "run_tool")
		}
		acts = append(acts, "help")
		params["action"] = ToolParam{
			Type:        "string",
			Description: "One of: " + strings.Join(acts, " | ") + ".",
		}
	}
	return AgentToolDef{
		Tool: Tool{
			Name:        "agents",
			Description: desc,
			Parameters:  params,
			Required:    []string{"action"},
			Caps:        caps,
		},
		Handler: func(args map[string]any) (string, error) {
			action := strings.TrimSpace(stringArg(args, "action"))
			switch action {
			case "", "help":
				return agentsToolHelp(allowRun, allowRunTool), nil
			case "list":
				return t.agentsListAction()
			case "get":
				return t.agentsGetAction(args)
			case "run":
				if !allowRun {
					return "", fmt.Errorf("agents(run) is not available to this agent — your job is authoring/composition, not delegation. To execute work, call plan_set with worker steps; to consult a specialist during authoring, dispatch a plan_set worker with web_search / fetch_url instead of dispatching to another agent")
				}
				return t.agentsRunAction(args)
			case "run_tool":
				if !allowRunTool {
					return "", fmt.Errorf("agents(run_tool) is not available to this agent — it's a Builder-only allowance for verifying an agent's tools directly")
				}
				return t.agentsRunToolAction(args)
			default:
				acts := []string{"list", "get"}
				if allowRun {
					acts = append(acts, "run")
				}
				if allowRunTool {
					acts = append(acts, "run_tool")
				}
				acts = append(acts, "help")
				return "", fmt.Errorf("unknown action %q for agents tool. valid: %s", action, strings.Join(acts, ", "))
			}
		},
	}
}

func agentsToolHelp(allowRun, allowRunTool bool) string {
	base := `agents — usage:

  action="list"   — return the user's orchestrate agents as a JSON
                    array of {id, name, description, owned}. No
                    other params. Call before get when you don't
                    know what agents exist.

  action="get"    — fetch one agent's full record by id AND set it
                    as authoring focus for this session. Required:
                    id (from list).
`
	if allowRun {
		base += `
  action="run"    — dispatch work to an agent by name (or id), get
                    its synthesis back as the tool result. The sub-
                    agent runs with its own persona, memory, facts,
                    and tools. Required: agent, message.
`
	} else {
		base += `
  (action="run" is intentionally disabled for this agent — use
   plan_set with worker steps to execute, or with web_search /
   fetch_url to consult specialist knowledge during authoring.)
`
	}
	if allowRunTool {
		base += `
  action="run_tool" — (Builder only) execute ONE of a target
                    agent's attached tools directly, with explicit
                    args, and get its raw output. Skips the sub-
                    agent LLM turn that action="run" costs — use it
                    to verify a tool works without relying on the
                    agent to pick and call it. Required: agent,
                    tool, plus tool_args={...} (for a toolbox,
                    tool is the toolbox name and tool_args carries
                    {"action":"<sub>", ...}). Runs against the real
                    credential/endpoint — a write action has real
                    effects, so exercise read actions first.
`
	}
	base += `
  action="help"   — show this spec.`
	return base
}

// agentsListAction returns the user's agents as JSON. Same shape the
// legacy list_agents tool produces — kept identical so existing
// consumers don't have to adapt.
func (t *chatTurn) agentsListAction() (string, error) {
	fleetDB, fleetUser := t.fleetView()
	if fleetDB == nil || fleetUser == "" {
		return "", errors.New("agents(list) requires authenticated session")
	}
	type row struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Owned       bool   `json:"owned"`
	}
	all := listAgents(fleetDB, fleetUser)
	out := make([]row, 0, len(all))
	for _, a := range all {
		// Builder is not dispatch-callable (see agentsRunAction);
		// hide it from the listing too so the LLM can't address it
		// by id/name through the tool. Direct chat with Builder via
		// the Agency picker / /chat/seed-builder still works — those
		// are human-facing surfaces, not LLM-facing.
		if isBuilderAgent(a.ID) {
			continue
		}
		// Sub-agents held for approval aren't live — keep them out of the
		// dispatch listing until the owner activates them.
		if a.PendingApproval {
			continue
		}
		out = append(out, row{
			ID:          a.ID,
			Name:        a.Name,
			Description: a.Description,
			Owned:       a.Owner == fleetUser,
		})
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

// agentsGetAction reads one agent record + stamps authoring focus.
// Mirrors get_agent's behavior so downstream tools that read
// AuthoringAgentID work whether the LLM used get_agent or
// agents(action="get").
func (t *chatTurn) agentsGetAction(args map[string]any) (string, error) {
	if t.udb == nil || t.user == "" {
		return "", errors.New("agents(get) requires authenticated session")
	}
	id := strings.TrimSpace(stringArg(args, "id"))
	if id == "" {
		return "", errors.New("id is required for action=get")
	}
	// Builder is hidden from this surface — see agentsRunAction and
	// agentsListAction for the rationale.
	if isBuilderAgent(id) {
		return "", fmt.Errorf("agent %q not found", id)
	}
	fleetDB, fleetUser := t.fleetView()
	a, ok := loadAgent(fleetDB, id)
	if !ok || (a.Owner != fleetUser && a.Owner != seedOwner) {
		return "", fmt.Errorf("agent %q not found", id)
	}
	if t.session != nil && t.session.ID != "" {
		saveAuthoringInProgress(t.udb, t.session.ID, a.ID)
	}
	// Default to a COMPACT view, not the full record. A full agents(get)
	// marshals the ~15KB orchestrator_prompt + every agent-scoped tool's
	// full templates — observed at 64KB per call. Builder re-fetches to
	// re-evaluate after each edit, so those echoes accumulate and blew a
	// long authoring session past the 200K context window. Builder rewrites
	// prose it authored this session wholesale (it already has that text),
	// so it needs STRUCTURE + previews here.
	//
	// full=true returns the complete record — the escape hatch for when
	// Builder needs to READ prose it did NOT write this session: editing an
	// inherited prompt after clone_agent, or an agent from a prior session.
	// See project_long_context_management.
	if b, ok := args["full"].(bool); ok && b {
		full, _ := json.Marshal(a)
		return string(full), nil
	}
	return string(slimAgentJSON(a)), nil
}

// agentsRunToolAction executes ONE of a target agent's attached tools
// directly, with caller-supplied args, and returns its raw output. This
// is the Builder-only verification seam: it skips the sub-agent LLM turn
// (and the dispatch cap) that agents(run) incurs, so Builder can drive an
// agent's tools one by one — profile, then post, then feed — and see
// exactly what each returns. The tool runs through the same execution path
// (secure-API allow-list, sandbox, response_pipe) the agent itself would
// use, so credentials stay server-side and a mutating call has real
// effects — Builder is expected to test read actions before writes.
func (t *chatTurn) agentsRunToolAction(args map[string]any) (string, error) {
	if !isBuilderAgent(t.agent.ID) {
		return "", errors.New("agents(run_tool) is Builder-only")
	}
	key := strings.TrimSpace(stringArg(args, "agent"))
	toolName := strings.TrimSpace(stringArg(args, "tool"))
	if key == "" || toolName == "" {
		return "", errors.New("agent and tool are required for action=run_tool")
	}
	fleetDB, fleetUser := t.fleetView()
	target, ok := findAgentByNameOrID(fleetDB, fleetUser, key)
	if !ok {
		return "", fmt.Errorf("agent %q not found in your store — call agents(action=list) to see what's available", key)
	}
	// Locate the named tool in the target agent's attached kit.
	var found *TempTool
	for i := range target.Tools {
		if target.Tools[i].Name == toolName {
			found = &target.Tools[i]
			break
		}
	}
	if found == nil {
		names := make([]string, 0, len(target.Tools))
		for i := range target.Tools {
			names = append(names, target.Tools[i].Name)
		}
		if len(names) == 0 {
			return "", fmt.Errorf("agent %q has no attached tools to run", target.Name)
		}
		return "", fmt.Errorf("agent %q has no attached tool named %q. Its tools: %s", target.Name, toolName, strings.Join(names, ", "))
	}
	toolArgs := testArgsFromArgs(args, "tool_args")
	if toolArgs == nil {
		toolArgs = map[string]any{}
	}
	// Execute directly on a fresh tool session (same DB/user/network/ctx as
	// this turn). No dispatch-cap accounting — this is a single tool call,
	// not an agent dispatch.
	sess := t.newToolSession()
	toolCopy := *found
	out, err := temptool.DispatchTempToolDirect(sess, &toolCopy, toolArgs)
	if err != nil {
		return fmt.Sprintf("Ran %q on agent %q — FAILED: %v. The tool's own definition (params / url_template / body_template / credential) is the thing to fix; edit it with tool_def(action=\"update\", name=%q, ...) for a toolbox, or add_tool for a single shell/api tool, then run_tool again.", toolName, target.Name, err, toolName), nil
	}
	trimmed := strings.TrimSpace(out)
	if len(trimmed) > 2000 {
		trimmed = trimmed[:2000] + "\n... [truncated]"
	}
	return fmt.Sprintf("Ran %q on agent %q — result:\n\n%s", toolName, target.Name, trimmed), nil
}

// slimAgentJSON renders an AgentRecord for the agents(get) tool result:
// all structure/flags intact, the heavy prose fields (orchestrator_prompt,
// plan_guidance, rules) previewed with a length marker, and agent-scoped
// tools reduced to name/mode/description (their full templates dropped).
func slimAgentJSON(a AgentRecord) []byte {
	preview := func(s string, n int) string {
		s = strings.TrimSpace(s)
		if len(s) <= n {
			return s
		}
		return s[:n] + fmt.Sprintf("…[%d chars total — previewed; you have the full text you set, re-send it wholesale to change it]", len(s))
	}
	type toolSummary struct {
		Name        string `json:"name"`
		Mode        string `json:"mode,omitempty"`
		Description string `json:"description,omitempty"`
	}
	tools := make([]toolSummary, 0, len(a.Tools))
	for _, tl := range a.Tools {
		tools = append(tools, toolSummary{Name: tl.Name, Mode: tl.Mode, Description: tl.Description})
	}
	slim := map[string]any{
		"id": a.ID, "name": a.Name, "description": a.Description,
		"orchestrator_prompt":      preview(a.OrchestratorPrompt, 500),
		"plan_guidance":            preview(a.PlanGuidance, 300),
		"rules":                    preview(a.Rules, 500),
		"allowed_tools":            a.AllowedTools,
		"tools":                    tools,
		"attached_collections":     a.AttachedCollections,
		"attached_pipelines":       a.AttachedPipelines,
		"allowed_skills":           a.AllowedSkills,
		"allowed_dispatch_targets": a.AllowedDispatchTargets,
		"max_plan_steps":           a.MaxPlanSteps,
		"max_worker_rounds":        a.MaxWorkerRounds,
		"exposed":                  a.Exposed,
		"public_name":              a.PublicName,
		"hidden":                   a.Hidden,
		"force_private":            a.ForcePrivate,
		"allow_private_mode":       a.AllowPrivateMode,
		"lead_model":               a.LeadModel,
		"disable_explicit":         a.DisableExplicit,
		"disable_inferred":         a.DisableInferred,
		"disable_skills":           a.DisableSkills,
		"memory_mode":              a.MemoryMode,
		"ingest_attachments":       a.IngestAttachments,
		"allow_explorer":           a.AllowExplorer,
		"gap_check":                a.GapCheck,
		"knowledge_model":          a.KnowledgeModel,
		"evals_count":              len(a.Evals),
		"intake_form":              a.IntakeForm,
		"_note":                    "Compact view: orchestrator_prompt / plan_guidance / rules are previewed (full text omitted to save context); tools listed by name+mode. To change a prose field, send the complete new text via update_agent. If you need to READ the full prose you didn't write this session (e.g. to edit an inherited prompt after clone_agent), call agents(action=\"get\", id=…, full=true).",
	}
	b, _ := json.Marshal(slim)
	return b
}

// agentsRunAction dispatches to the target agent. State model: EPHEMERAL
// continuity — a follow-up to the same agent in the same parent session
// re-threads the prior exchange (so "now tell me more" just works), but the
// continuity is bounded to this parent session and lives in the parent's own
// namespace (not a hidden cross-session ledger). The target's long-term
// facts/knowledge/persona load on every dispatch regardless.
//
// Live activity: the sub-agent's tool calls + step progress emit into the
// parent turn's SSE so the user sees "[<target name>] web_search(...)" appear
// in the activity pane as the sub-agent works. Without this the sub-agent
// would be invisible until its final synthesis returned.
//
// Direct chat with a sub-agent via Agency's secondary picker is a SEPARATE
// code path (handleSend) with normal ChatSession persistence — that's the
// testing/iteration surface, not the dispatch surface.
func (t *chatTurn) agentsRunAction(args map[string]any) (string, error) {
	if t.dispatchDepth >= maxDispatchDepth {
		return "", fmt.Errorf("agents(run): depth limit %d exceeded", maxDispatchDepth)
	}
	key := strings.TrimSpace(stringArg(args, "agent"))
	msg := strings.TrimSpace(stringArg(args, "message"))
	if key == "" || msg == "" {
		return "", errors.New("agent and message are required for action=run")
	}
	fleetDB, fleetUser := t.fleetView()
	target, ok := findAgentByNameOrID(fleetDB, fleetUser, key)
	if !ok {
		return "", fmt.Errorf("agent %q not found in your store — call agents(action=list) to see what's available", key)
	}
	// A sub-agent held for approval is not live yet — refuse to dispatch it until
	// the owner activates it from the Authorizations pane.
	if target.PendingApproval {
		return "", fmt.Errorf("agents(run, agent=%q) refused — that agent is awaiting approval and isn't live yet; it becomes dispatchable once the user approves it in the Authorizations pane", key)
	}
	if target.ID == t.agent.ID {
		return "", fmt.Errorf("agents(run, agent=%q) is impossible — you ARE %s, or you ARE a worker spawned by %s. Calling yourself is infinite recursion. STOP trying to dispatch back to yourself; do the work directly with the tools you already have. Retrying this call will keep failing — pick a different agent or just execute the work yourself", key, t.agent.Name, t.agent.Name)
	}
	// Builder is never dispatchable. Builder's authoring rhythm needs
	// a human in the loop — Phase 1 conversational intake, ask_user
	// pauses for design clarifications, the approval gate on every
	// authored tool. The [DELEGATED INVOCATION] marker that strips
	// ask_user under dispatch turns Builder into a guessing game on a
	// thin brief, and any tools it authors get stuck in a sub-session
	// draft pool the dispatching agent can't see. The user clicks
	// Builder in their picker directly when they want authoring; no
	// other agent should be intermediating that conversation.
	// Builder is dispatch-callable ONLY from a channel/Fleet controller (e.g.
	// Chat). That parent runs it as an authoring SUB-agent: Builder inherits the
	// parent's inheritable tools (to inspect the parent's world while drafting),
	// and anything it creates is stamped OwnedBy=<parent> and queued for the
	// parent owner's approval instead of going live. For every NON-Fleet caller
	// Builder stays undispatchable — authoring there still needs the human in
	// the loop (the full intake conversation, ask_user pauses, draft review).
	if isBuilderAgent(target.ID) && !t.agent.Fleet {
		return "", fmt.Errorf("agents(run, agent=%q) refused — Builder is dispatch-callable only from a channel/fleet agent. Point the user at Builder in their agent picker (or the chat URL for Builder) and describe what they want built", key)
	}
	// Cycle guard. The current turn's agent is always considered "in
	// flight" — combined with dispatchChain (inherited from parent
	// turns), this catches A→B→A and any longer cycle like A→B→C→A.
	// Without this, depth resets to 0 on each sub-turn so a cycle
	// could iterate maxDispatchDepth times per "level" before tripping
	// the depth cap — observed: Builder→Chat→Builder, where Chat's
	// "dispatch to Builder on authoring intent" instruction sends the
	// turn right back into Builder.
	for _, prior := range t.dispatchChain {
		if prior == target.ID {
			return "", fmt.Errorf("agents(run): dispatch cycle — %q is already on the call chain for this turn; pick a different target or answer directly", target.Name)
		}
	}
	// Dispatch gate. Two cases mirror the visibility logic in
	// renderAvailableAgentsBlock so a target dropped from the block
	// can't be reached by guessing its name either.
	//
	//   1. Allowlist mode — caller has a non-empty AllowedDispatchTargets:
	//      ONLY listed targets are reachable (Hidden status ignored;
	//      the explicit pick wins both ways).
	//   2. Default mode — caller's allowlist is empty: any non-Hidden
	//      target is reachable.
	// Sub-agent ownership — implicit dispatch authority. If the target
	// is owned by the caller (target.OwnedBy == t.agent.ID), the
	// dispatch is allowed regardless of AllowedDispatchTargets and
	// regardless of Hidden status. Ownership IS the link. This is the
	// sub-agent / specialist pattern: a parent agent owns focused
	// capability sub-agents and can reach them without re-listing each
	// one in its allowlist.
	//
	// Builder override — Builder is the authoring surface that mints
	// sub-agents on behalf of their eventual parent. To test or debug
	// a freshly-authored specialist directly, Builder must be able to
	// reach ANY sub-agent regardless of who owns it (Builder doesn't
	// own them — the configured parent does). Without this carve-out,
	// Builder's "verify the persona by dispatching a probe" step
	// fails on every sub-agent it just authored. Limited to sub-agents
	// only (target.OwnedBy != "") so the override doesn't unlock
	// arbitrary fleet access from Builder; just the specialists.
	if target.OwnedBy == t.agent.ID {
		// Allowed by ownership; skip the standard checks.
	} else if isBuilderAgent(target.ID) && t.agent.Fleet {
		// Builder is dispatch-callable from a Fleet controller (e.g. Chat) for
		// in-session authoring, despite Builder's Hidden=true seed posture. The
		// guard at the top of this function already refused non-Fleet callers, so
		// reaching here means the caller is authorized — let it through past the
		// Hidden / allowlist checks below.
	} else if isBuilderAgent(t.agent.ID) && target.OwnedBy != "" {
		// Builder override — allow dispatch to any sub-agent for
		// post-authoring verification. Logged for audit visibility.
		Log("[orchestrate.agents.run] Builder override — dispatching to sub-agent %q (owned_by=%q)", target.Name, target.OwnedBy)
	} else {
		switch effectiveDispatchMode(t.agent) {
		case dispatchNone:
			return "", fmt.Errorf("agents(run): this agent is set to dispatch to NO other agents (Security & Access → Allow none); ask the user to change its dispatch policy before it can reach %q", target.Name)
		case dispatchOnly:
			if !dispatchListContains(t.agent, target.ID) {
				return "", fmt.Errorf("agents(run): agent %q is not on this agent's dispatch allow list; ask the user to add it (Security & Access) or change the policy to Allow all", target.Name)
			}
		case dispatchExcept:
			if dispatchListContains(t.agent, target.ID) {
				return "", fmt.Errorf("agents(run): agent %q is on this agent's dispatch block list; ask the user to remove it (Security & Access) to reach it", target.Name)
			}
			if target.Hidden {
				return "", fmt.Errorf("agents(run): agent %q is hidden from the fleet; ask the user to toggle Hidden off on %q, or switch this agent to Only-allow and add it", target.Name, target.Name)
			}
		default: // dispatchAll
			if target.Hidden {
				return "", fmt.Errorf("agents(run): agent %q is hidden from the fleet; ask the user to toggle Hidden off on %q, or add it to this agent's dispatch allow list", target.Name, target.Name)
			}
		}
	}
	// Per-turn dispatch caps — the hard stop for a chat agent that re-fires
	// agents(run, X) round after round in ONE turn. dispatchDepth (recursion)
	// and dispatchChain (cycles) both miss it: depth resets as each sub-run
	// returns and there's no cycle. A prompt "don't dispatch again" is a soft
	// guard the worker ignores; this is code-enforced. Counts only dispatches
	// that pass the gates above (a refused one shouldn't burn the budget).
	//
	// Two distinct pathologies, two counters:
	//   (1) LOOP — the SAME call (target + message) fired over and over with
	//       no new input. Keyed on target+message so it trips ONLY on true
	//       repeats. This is the one the Builder kept false-positiving: it
	//       drives one agent with a DIFFERENT message per tool (profile, then
	//       post, then feed…), which is real verification progress, not a loop.
	//   (2) THRASH — an outsized TOTAL volume of (possibly distinct) dispatches
	//       to one target in a turn. Ceiling is generous, and higher still when
	//       the dispatcher is the Builder, whose job is to sweep an agent's
	//       whole toolset.
	if t.agentDispatchCounts == nil {
		t.agentDispatchCounts = map[string]int{}
	}
	if block := dispatchCapDecision(t.agentDispatchCounts, target.ID, target.Name, msg, isBuilderAgent(t.agent.ID)); block != "" {
		Log("[orchestrate.agents.run] per-turn dispatch cap hit: %s → %s — blocking further dispatch", t.agent.ID, target.ID)
		return block, nil
	}
	t.dispatchDepth++
	defer func() { t.dispatchDepth-- }()

	parentSessID := ""
	if t.session != nil {
		parentSessID = t.session.ID
	}
	// Deterministic per-(parent, target) sub-session ID: keys the EPHEMERAL
	// dispatch continuity (the prior exchange is re-threaded on follow-ups,
	// see below) and scopes the sub-agent's workspace + session temp tools.
	// Not registered in the SubSession lifecycle index; that index drives
	// async promotion, which orchestrate doesn't do (dispatch is sync).
	subSessID := "dispatch:" + parentSessID + ":" + target.ID
	subSess := &ToolSession{
		LLM:            t.app.LLM,
		LeadLLM:        t.app.LeadLLM,
		Username:       t.user,
		DB:             t.udb,
		ChatSessionID:  subSessID,
		SubAgentRunner: t.runPipelineSubAgent,
		// Carry the dispatching parent so authoring tools (Builder's
		// create_agent) can stamp creations OwnedBy=<parent> and route them to
		// the parent owner's approval queue.
		DispatchParentAgentID: t.agent.ID,
		// Inherit the parent turn's LIVE connector (same instance).
		// Mid-turn flips on the parent propagate to this child
		// too — sub-agents can never be more permissive than the
		// host, AND a privacy cutoff fired on the parent stops
		// the child's network access mid-flight as well.
		Network: t.network,
	}
	if ws, werr := EnsureWorkspaceDir(t.user); werr == nil {
		subSess.WorkspaceDir = ws
	}
	defer clearAuthoringInProgress(t.udb, subSessID)
	defer DeleteSessionTempTools(t.udb, subSessID)

	toolNames := target.AllowedTools
	if len(toolNames) == 0 {
		for _, td := range RegisteredChatTools() {
			toolNames = append(toolNames, td.Name())
		}
	}
	tools, err := GetAgentToolsWithSession(subSess, toolNames...)
	if err != nil {
		tools = nil
		for _, n := range toolNames {
			if td, terr := GetAgentToolsWithSession(subSess, n); terr == nil && len(td) > 0 {
				tools = append(tools, td[0])
			}
		}
	}
	if isBuilderAgent(target.ID) {
		tools = append(tools, builderAuthoringTools(subSess, nil)...)
		// Dispatched Builder reaches here only from a Fleet parent (guarded at
		// the top of this function). Inherit that parent's non-consequential
		// catalog so Builder can inspect the parent's world while authoring —
		// read_phantom_chat, web_search, etc. — but never the parent's texting /
		// delegation / fleet tools. Deduped so shared names don't double-add.
		tools = mergeToolsDedup(tools, t.inheritableParentTools(t.agent, subSess))
	}

	// chatTurn-bound framework tools (knowledge_search, memory_*,
	// agents, store_fact, etc.) are NOT in the global registry —
	// they're built per-turn so the closure captures the right
	// agent / DB / topic. The dispatched sub-agent needs its own
	// builds against the TARGET's config, otherwise knowledge_search
	// is missing from the sub-agent's catalog entirely and the
	// agent can't see its own AttachedCollections corpus. Construct
	// a minimal chatTurn for the target and invoke the per-turn
	// builders against it. Most chatTurn fields (sse, session,
	// queue) stay nil — the bound tools that need them aren't
	// added to the sub-agent's catalog anyway.
	subTurn := &chatTurn{
		app:     t.app,
		agent:   target,
		user:    t.user,
		udb:     t.udb,
		ctx:     t.ctx, // inherit caller's ctx so NetworkConnector propagates
		topic:   t.resolveTopic(),
		network: t.network,
		// Carry the caller's chain + the caller's own agent ID forward.
		// The cycle guard above runs against this slice on every
		// further agents(run) the sub-turn makes.
		dispatchChain: append(append([]string(nil), t.dispatchChain...), t.agent.ID),
	}
	// Shared sub-agent dispatch catalog — framework conversational tools
	// (knowledge, find_tools, send_status, stay_silent, load_tool, skills, the
	// memory layers, cortex deliverables), the agents grouped
	// tool, attached pipelines, AND the target's custom tools. dispatchExtraTools
	// is the SAME assembly the channel/dispatch path uses, so an inline
	// agents(run) sub-agent sees the identical surface (this is the path that
	// previously had neither the full framework set nor any custom tools) and the
	// two sub-agent surfaces can't drift. Parent + sub-agent share the user/db, so
	// the custom-tool pool owner is the caller's user.
	dispatchExtra, customToolPrompt := subTurn.dispatchExtraTools(subSess, t.user, t.udb)
	tools = append(tools, dispatchExtra...)
	// Parent-tool inheritance on the DISPATCH path (this resolves tools directly,
	// not via resolveWorkerTools, so the runtime block there wouldn't fire here).
	// An owned sub-agent that opted in pulls its parent's non-consequential
	// catalog (read_phantom_chat etc.) so a Builder-authored summarizer can read
	// the chat it summarizes even when reached by dispatch. Guarded to top-level
	// parents; deduped.
	if target.InheritParentTools && target.OwnedBy != "" {
		if parent, ok := loadAgent(t.udb, target.OwnedBy); ok && parent.OwnedBy == "" {
			tools = mergeToolsDedup(tools, subTurn.inheritableParentTools(parent, subSess))
		}
	}
	// Channel-scoped messaging tools (list_chats / read_chat / send_message)
	// for the TARGET's own bound channels. Without this a channel-bound agent
	// (e.g. a WiWee transport agent) dispatched via agents(action="run") has no
	// send_message tool, so "post this to the group" becomes a hallucinated
	// success — the agent claims it sent but nothing reaches the channel. The
	// external dispatch paths (RunAgentSync / RunAgentSyncContinuing) already
	// add these; the in-session path was the lone gap. Self-gates: returns nil
	// when the target has no channels or no transport is registered, and
	// send_message still routes through its own pre-auth / approval check.
	if chTools := channelChatTools(subSess, t.user, target.ID); len(chTools) > 0 {
		tools = append(tools, chTools...)
	}

	// V1 — wrap the sub-agent's tools so their calls emit into the
	// caller's SSE activity pane. Reuses the parent's wiring (cmd
	// rows, inline chips, cache annotations); the user sees the
	// sub-agent's work live instead of waiting in the dark.
	//
	// Pass the target's name as a label prefix so the sub-agent's
	// tool calls render with visual nesting ("↳ [Pickleball Coach]
	// knowledge_search(...)") instead of blending in with the
	// parent's own tool calls — the second knowledge_search row
	// the user reported was the sub-agent's, but they had no way
	// to tell because both rows looked identical.
	t.wrapToolsForActivity(subSess, tools, "↳ ["+target.Name+"] ")

	subFacts := ListMemoryFacts(t.udb, factsNamespace(target.ID))
	sysPrompt := prependAgentContext(
		t.gatedPersona(target.OrchestratorPrompt),
		target, subFacts, agentOperatingNotes(t.udb, target),
	)
	sysPrompt += customToolPrompt // "Your custom tools (load before use)" section

	// Ephemeral dispatch continuity: a follow-up to the SAME agent in the
	// SAME parent session re-threads the prior exchange, so the parent can
	// ask "now tell me more about their B2B presence" without re-briefing.
	// Scoped to dispatch:<parentSessID>:<target.ID> in the parent's OWN db
	// (t.udb) and capped to recent turns, so it's:
	//   - ephemeral: bounded to this parent session, not a permanent ledger;
	//   - visible/controllable: lives in the parent's namespace, not a hidden
	//     phantom:<chatID> store the parent can't see (the contamination the
	//     old stateless design avoided);
	//   - additive: the target's long-term facts/knowledge/persona already
	//     load above; this only adds the running conversation.
	// Direct Agency chat with a sub-agent is a separate path (handleSend).
	prior, _ := loadChatSession(t.udb, target.ID, subSessID)
	// Only Builder acts on the delegated marker; others get the message verbatim.
	deliveredMsg := msg
	if isBuilderAgent(target.ID) {
		deliveredMsg = markAsDelegated(msg)
	}
	llmMessages := make([]Message, 0, len(prior.Messages)+1)
	for _, m := range prior.Messages {
		llmMessages = append(llmMessages, Message{Role: m.Role, Content: m.Content})
	}
	llmMessages = append(llmMessages, Message{Role: "user", Content: deliveredMsg})

	// V1 — per-step status emit. Hooks the orchestrator's per-round
	// progress callback so the user sees "[<target>] round N (X tool
	// calls)" snapshots between rounds. Cheap; one SSE event per
	// round at most.
	stepNotice := func(step StepInfo) {
		text := fmt.Sprintf("[%s] round %d", target.Name, step.Round)
		if n := len(step.ToolCalls); n > 0 {
			text += fmt.Sprintf(" (%d tool call%s)", n, plural(n))
		}
		if step.Done {
			text += " — done"
		}
		t.sse.Send(map[string]any{
			"kind": "activity",
			"type": "status",
			"id":   activityCheapID(),
			"text": text,
		})
	}
	t.sse.Send(map[string]any{
		"kind": "activity",
		"type": "status",
		"id":   activityCheapID(),
		"text": fmt.Sprintf("[%s] dispatched", target.Name),
	})

	// Bound the sub-agent run by its OWN round cap (MaxRounds below) + the
	// per-call LLM budget — same as a top-level turn — NOT an arbitrary
	// wall-clock cap. The previous WithTimeout(knowledgeIngestTimeout*4 =
	// 3m) reused a knowledge-INGEST constant for agent EXECUTION, and 3m is
	// SHORTER than a single LLM call's 5m budget, so any non-trivial or
	// nested sub-agent blew it. The deadline then surfaced as the parent's
	// agents(run) tool result ("context deadline exceeded"), looking like
	// the MAIN agent failed. WithCancel keeps cleanup + client-disconnect
	// cancellation (t.ctx) without the bogus deadline.
	ctx, cancel := context.WithCancel(t.ctx)
	defer cancel()
	// ForcePrivate enforcement — same shape as the external dispatch
	// paths. The parent's network connector already propagates via
	// subSess.Network (set at line ~391), but if THIS sub-agent has
	// ForcePrivate=true while the parent didn't, the connector would
	// stay permissive. This call upgrades to a blocked connector and
	// strips CapNetwork tools from the catalog. No-op when
	// target.ForcePrivate is false.
	ctx, tools = applyForcePrivateToDispatch(ctx, subSess, tools, target)
	think := resolveDispatchThink(target)
	resp, _, runErr := t.app.RunAgentLoop(ctx, llmMessages, AgentLoopConfig{
		SystemPrompt: sysPrompt,
		Tools:        tools,
		MaxRounds:    resolveMaxWorkerRounds(target),
		ThinkBudget:  target.ThinkBudget, // per-agent override; 0 = inherit route/global
		Confirm:      func(name, args string) bool { return true },
		OnStep:       stepNotice,
		// Custom-tool resolution, same as the channel/dispatch + web paths:
		// lazyToolFallback resolves a direct call to a has-args custom tool;
		// dynamicNewTempTools surfaces tools loaded via load_tool this turn.
		ToolFallbackResolver: subTurn.lazyToolFallback,
		DynamicTools:         subTurn.dynamicNewTempTools(subSess),
		ChatOptions: []ChatOption{
			WithRouteKey("app.orchestrate.worker"),
			WithThink(think),
		},
	})
	Log("[orchestrate.agents.run] depth=%d caller=%s → target=%s msg_chars=%d err=%v",
		t.dispatchDepth, t.agent.ID, target.ID, len(msg), runErr)
	if runErr != nil {
		return "", runErr
	}
	if resp == nil {
		return "", errors.New("agents(run): target returned no response")
	}
	cleanReply := strings.TrimSpace(resp.Content)
	// Feed the request into the target's cortex (cortex agents only — a no-op
	// otherwise) so a dispatched cortex/channel agent is AWARE another agent
	// asked it to do something. The dispatch ran in the throwaway
	// dispatch:<…> session, disconnected from the agent's standing thread, so
	// without this a channel agent (WiWee) posts to its group on request and
	// then can't field follow-ups about what it just "said". from = the
	// dispatching parent; the request text itself is the observation.
	if target.Cortex {
		appendCortexObs(t.udb, target.ID, t.agent.Name, cortexKindRequest, msg)
	}
	// Persist the exchange for the next follow-up. Store the RAW brief (not
	// the delegated wrapper) so re-threaded history reads cleanly, and cap to
	// the most recent turns to keep continuity cheap and ephemeral.
	if prior.ID == "" {
		prior.ID = subSessID
		prior.AgentID = target.ID
		prior.Created = time.Now()
	}
	tnow := time.Now()
	prior.Messages = append(prior.Messages,
		ChatMessage{Role: "user", Content: msg, Created: tnow},
		ChatMessage{Role: "assistant", Content: cleanReply, Created: tnow},
	)
	const maxDispatchTurns = 24 // ~12 exchanges of ephemeral continuity
	if len(prior.Messages) > maxDispatchTurns {
		prior.Messages = prior.Messages[len(prior.Messages)-maxDispatchTurns:]
	}
	if _, err := saveChatSession(t.udb, prior); err != nil {
		Log("[orchestrate.agents.run] WARN persist dispatch sub-session %s: %v", subSessID, err)
	}
	return fmt.Sprintf("From %s:\n\n%s", target.Name, cleanReply), nil
}
