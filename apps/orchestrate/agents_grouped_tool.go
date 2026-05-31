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
			Description: "(run) The question or task to send to the target agent. Phrase it as the user would phrase it directly — the sub-agent has its own persona and will frame the response.",
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
				return agentsToolHelp(allowRun), nil
			case "list":
				return t.agentsListAction()
			case "get":
				return t.agentsGetAction(args)
			case "run":
				if !allowRun {
					return "", fmt.Errorf("agents(run) is not available to this agent — your job is authoring/composition, not delegation. To execute work, call plan_set with worker steps; to consult a specialist during authoring, dispatch a plan_set worker with web_search / fetch_url instead of dispatching to another agent")
				}
				return t.agentsRunAction(args)
			default:
				validActions := "list, get, help"
				if allowRun {
					validActions = "list, get, run, help"
				}
				return "", fmt.Errorf("unknown action %q for agents tool. valid: %s", action, validActions)
			}
		},
	}
}

func agentsToolHelp(allowRun bool) string {
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

// agentsRunAction dispatches to the target agent. Two behaviors
// distinguish it from a one-shot call:
//
//   - **Live activity** (V1): the sub-agent's tool calls + step
//     progress emit into the parent turn's SSE so the user sees
//     "[<target name>] web_search(...)" appear in the activity
//     pane as the sub-agent works. Without this the sub-agent
//     was invisible until its final synthesis returned.
//   - **Multi-turn persistence** (V2): the sub-session's prior
//     Messages are loaded before the run and the new
//     (user → assistant) pair appended back after. A caller that
//     dispatches to the same target multiple times within the
//     same parent session gets continuity — the sub-agent
//     remembers what was discussed.
//
// The sub-session ID is deterministic (dispatch:<parent>:<target>)
// so the storage slot survives across calls. Different parents
// talking to the same target each get their own thread; the same
// parent gets one persistent thread per target.
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
	if target.ID == t.agent.ID {
		return "", fmt.Errorf("agents(run, agent=%q) is impossible — you ARE %s, or you ARE a worker spawned by %s. Calling yourself is infinite recursion. STOP trying to dispatch back to yourself; do the work directly with the tools you already have. Retrying this call will keep failing — pick a different agent or just execute the work yourself", key, t.agent.Name, t.agent.Name)
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
	} else if isBuilderAgent(t.agent.ID) && target.OwnedBy != "" {
		// Builder override — allow dispatch to any sub-agent for
		// post-authoring verification. Logged for audit visibility.
		Log("[orchestrate.agents.run] Builder override — dispatching to sub-agent %q (owned_by=%q)", target.Name, target.OwnedBy)
	} else if len(t.agent.AllowedDispatchTargets) > 0 {
		linked := false
		for _, id := range t.agent.AllowedDispatchTargets {
			if id == target.ID {
				linked = true
				break
			}
		}
		if !linked {
			return "", fmt.Errorf("agents(run): agent %q is not on this agent's allowed_dispatch_targets list; ask the user to add it or clear the allowlist", target.Name)
		}
	} else if target.Hidden {
		return "", fmt.Errorf("agents(run): agent %q is hidden from the fleet; ask the user to toggle Hidden off on %q, or add it to this agent's allowed_dispatch_targets", target.Name, target.Name)
	}
	t.dispatchDepth++
	defer func() { t.dispatchDepth-- }()

	parentSessID := ""
	if t.session != nil {
		parentSessID = t.session.ID
	}
	// Deterministic per-(parent, target) sub-session ID so repeat
	// dispatches to the same target re-thread prior messages (V2
	// continuity below). Orchestrate does NOT register this in the
	// SubSession lifecycle index — that index drives async promotion,
	// which orchestrate doesn't do (dispatch is sync). Registering
	// here would just leak idle records nobody reads or retires.
	subSessID := "dispatch:" + parentSessID + ":" + target.ID
	subSess := &ToolSession{
		LLM:            t.app.LLM,
		LeadLLM:        t.app.LeadLLM,
		Username:       t.user,
		DB:             t.udb,
		ChatSessionID:  subSessID,
		SubAgentRunner: t.runPipelineSubAgent,
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
		tools = append(tools, builderAuthoringTools(subSess)...)
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
	// Knowledge layer — always-on read tool over the target's
	// corpus + its AttachedCollections + auto-injected deployment
	// collections (the empty-list default-attach rule).
	tools = append(tools, subTurn.searchKnowledgeToolDef())
	// Memory layers — only if the target has them enabled. The
	// helpers gate internally on agent.DisableInferred /
	// DisableExplicit, so just appending unconditionally is safe.
	if !subTurn.inferredOff() {
		tools = append(tools, subTurn.saveMemoryToolDef(), subTurn.searchMemoryToolDef(), subTurn.forgetMemoryToolDef())
	}
	if !subTurn.explicitOff() {
		tools = append(tools, subTurn.storeFactToolDef(), subTurn.forgetFactToolDef())
	}
	// Agents grouped tool — sub-agents (OwnedBy set) are LEAVES and
	// don't get the dispatch surface (eliminates depth cascades and
	// forces hierarchical composition: peers under one parent, not
	// chained sub-agents). Top-level targets keep the full grouped
	// surface; Builder targets stay read-only on dispatch (same
	// cycle-prevention as before).
	if target.OwnedBy == "" {
		tools = append(tools, subTurn.agentsGroupedToolDef(!isBuilderAgent(target.ID)))
	}
	if !target.DisableSkills {
		tools = append(tools, subTurn.activateSkillToolDef())
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
		target, subFacts,
	)

	// V2 — load prior sub-session and prepend its Messages so a
	// repeated dispatch to the same target reads as a continuation
	// rather than a fresh ask. New session loaded as empty when no
	// prior exists; ID + AgentID set so saveChatSession routes the
	// write to the target's bucket under our deterministic ID.
	subSession, subExists := loadChatSession(t.udb, target.ID, subSessID)
	if !subExists {
		subSession = ChatSession{
			ID:      subSessID,
			AgentID: target.ID,
			Title:   "dispatched by " + t.agent.Name,
			Created: time.Now(),
		}
	} else {
		subSession.AgentID = target.ID // loadChatSession stamps this; defensive
	}

	deliveredMsg := markAsDelegated(msg)
	llmMessages := make([]Message, 0, len(subSession.Messages)+1)
	for _, m := range subSession.Messages {
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
		"text": fmt.Sprintf("[%s] dispatched (%d prior turn%s)", target.Name, len(subSession.Messages)/2, plural(len(subSession.Messages)/2)),
	})

	ctx, cancel := context.WithTimeout(t.ctx, knowledgeIngestTimeout*4)
	defer cancel()
	// Resolve thinking the same way the chat surface does so a
	// dispatched agent runs with the SAME default as if it were
	// invoked directly from Agency. Base = route default (true for
	// orchestrator stage). Target's explicit Think="on"/"off" wins
	// over the route default; empty Think falls through to the route
	// default rather than getting a different "dispatch-only" default.
	think := true
	if p := RouteThink("app.orchestrate.orchestrator"); p != nil {
		think = *p
	}
	switch target.Think {
	case "on":
		think = true
	case "off":
		think = false
	}
	resp, _, runErr := t.app.RunAgentLoop(ctx, llmMessages, AgentLoopConfig{
		SystemPrompt: sysPrompt,
		Tools:        tools,
		MaxRounds:    resolveMaxWorkerRounds(target),
		Confirm:      func(name, args string) bool { return true },
		OnStep:       stepNotice,
		ChatOptions: []ChatOption{
			WithRouteKey("app.orchestrate.worker"),
			WithThink(think),
		},
	})
	Log("[orchestrate.agents.run] depth=%d caller=%s → target=%s prior_msgs=%d msg_chars=%d err=%v",
		t.dispatchDepth, t.agent.ID, target.ID, len(subSession.Messages), len(msg), runErr)
	if runErr != nil {
		return "", runErr
	}
	if resp == nil {
		return "", errors.New("agents(run): target returned no response")
	}

	// V2 — persist this exchange (user msg + assistant reply) onto
	// the sub-session so the next dispatch sees it as prior history.
	now := time.Now()
	cleanReply := strings.TrimSpace(resp.Content)
	subSession.Messages = append(subSession.Messages,
		ChatMessage{Role: "user", Content: deliveredMsg, Created: now},
		ChatMessage{Role: "assistant", Content: cleanReply, Created: now},
	)
	if _, serr := saveChatSession(t.udb, subSession); serr != nil {
		Log("[orchestrate.agents.run] WARN failed to persist sub-session %s: %v", subSessID, serr)
	}

	return fmt.Sprintf("From %s:\n\n%s", target.Name, cleanReply), nil
}
