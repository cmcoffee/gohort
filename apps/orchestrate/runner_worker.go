package orchestrate

import (
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/temptool"

	"github.com/cmcoffee/gohort/core/prompts"
)

// runWorkerStep dispatches one plan step to the worker (no-think) LLM.
// Prior step outputs are included as context so later steps can build
// on earlier ones. The worker's tool surface is the default pool (every
// non-blocked chat tool with Read or Network capability) intersected
// with agent.AllowedTools when the agent set an explicit allowlist;
// empty AllowedTools means "default pool".
//
// Each tool call the worker makes surfaces as an activity row in the
// AgentLoopPanel pane so the user can see what the agent is doing
// live, without waiting for the step to finish.
func (t *chatTurn) runWorkerStep(prior []PlanStep, cur PlanStep, userMsg string, notes []injectionNote) (stepOut string, stepErr error) {
	// Per-step telemetry — same instrumentation as the orchestrator's
	// runPlan. Each worker step gets its own round budget and its own
	// telem; the summary log fires when the step exits.
	telem := newTurnTelemetry()
	defer func() {
		softCap := resolveMaxWorkerRounds(t.agent)
		hardCap := softCap
		if t.agent.AllowExplorer {
			if ec := resolveExplorerHardCap(t.agent); ec > hardCap {
				hardCap = ec
			}
		}
		exitReason := classifyWorkerExit(stepErr, telem.rounds, softCap, len(stepOut))
		label := fmt.Sprintf("orchestrate.worker step=%d", cur.ID)
		Log("%s", telem.summary(label, softCap, hardCap, exitReason)+" agent="+t.agent.ID)
		if line := telem.toolCallSummary(label); line != "" {
			Log("%s", line)
		}
		// Churn is a user-visible outcome, not just a log line — route it
		// to the ⚠ trail so the person who saw the turn go wrong can see
		// WHY without reading server logs.
		if kind, detail, ok := telem.churnDiag(); ok {
			t.turnDiag(kind, detail)
		}
	}()

	// Reset the active-bubble id at each worker-step boundary so the
	// first tool call of THIS step materializes its own card under
	// the just-emitted intent block, instead of attaching to the
	// previous step's bubble. (currentMsgID lingers from the
	// orchestrator's last round / prior step otherwise.)
	t.setCurrentMsgID("")

	// Build the worker's per-call session — same helper the
	// orchestrator uses, so persistent temp tools land on both paths.
	sess := t.newToolSession()
	// Persist any managed-workspace switch this step performs so the
	// next step's fresh session shares the same workspace (a script
	// written here is then runnable from the following step).
	defer t.captureActiveWorkspace(sess)

	// forOrchestrator=false — this is the per-step worker running one
	// plan_set step. The Builder branch returns builderWorkerResearchTools
	// (raw call_<credential> for endpoint probing + the full authoring
	// catalog: tool_def / create_agent / add_tool / skill_def) on top of
	// the standard AllowedTools pool. Workers can either author directly
	// (call tool_def themselves and return confirmation) or return
	// components for Builder to assemble. The framework doesn't dictate;
	// Builder's brief decides which shape the worker takes.
	tools, toolNames, err := t.resolveWorkerTools(sess, false)
	if err != nil {
		return "", fmt.Errorf("resolve tools: %w", err)
	}
	// Orchestrator-curated per-step tool surface — when the plan
	// step's Tools field is set, intersect the agent's resolved pool
	// down to JUST those names. The worker sees exactly what the
	// orchestrator picked, no surplus, no classifier-trim noise.
	// Empty Tools falls back to the full resolved pool (the
	// orchestrator chose not to specify; degraded behavior).
	// Intersection (not raw substitution) keeps the agent's allowlist
	// + cap filters honored — the orchestrator can't grant a tool
	// the agent doesn't have.
	if len(cur.Tools) > 0 {
		want := make(map[string]bool, len(cur.Tools))
		for _, n := range cur.Tools {
			want[strings.TrimSpace(n)] = true
		}
		keptTools := tools[:0]
		keptNames := toolNames[:0]
		for i, t := range tools {
			if want[t.Tool.Name] {
				keptTools = append(keptTools, t)
				if i < len(toolNames) {
					keptNames = append(keptNames, toolNames[i])
				}
			}
		}
		Log("[orchestrate.worker] step %d (%q): orchestrator picked %d tools → %d resolved",
			cur.ID, cur.Title, len(cur.Tools), len(keptTools))
		tools = keptTools
		toolNames = keptNames
	}
	tools, toolNames = filterToolAuthoringWithoutFocus(tools, toolNames, t.session)
	t.gateAgentCRUDTools(tools)
	// Three layers, each gated by its own helper — same split as
	// the orchestrator catalog in runPlan. Workers are the ones
	// actually researching, so they get write access to the Inferred
	// Memory layer when it's enabled. Knowledge is always available.
	if !unifiedMemoryEnabled() {
		tools = append(tools, t.searchKnowledgeToolDef(), t.fetchKnowledgeDocToolDef())
		toolNames = append(toolNames, "knowledge_search", "fetch_knowledge_doc")
	}
	for _, td := range t.skillToolDefs() {
		tools = append(tools, td)
		toolNames = append(toolNames, td.Tool.Name)
	}
	// (dispatch_to_worker temporarily unmounted on the worker step
	// too — same reason as the orchestrator catalog: discoverability
	// problem, not a wiring problem.)
	if unifiedMemoryEnabled() {
		// Collapsed surface (see frameworkConversationalTools). recall fronts
		// knowledge search, so the legacy knowledge tools above are skipped too.
		if t.hasAnyMemoryLayer() {
			for _, td := range t.unifiedMemoryTools() {
				tools = append(tools, td)
				toolNames = append(toolNames, td.Tool.Name)
			}
		}
		if !t.explicitOff() {
			tools = append(tools, t.linkEntitiesToolDef(), t.recallAboutToolDef(), t.forgetGraphToolDef())
		}
	} else {
		if !t.inferredOff() {
			tools = append(tools, t.memoryToolDef())
			toolNames = append(toolNames, "memory")
		}
		if !t.explicitOff() {
			tools = append(tools, t.storeFactToolDef(), t.forgetFactToolDef(), t.searchFactsToolDef(),
				t.linkEntitiesToolDef(), t.recallAboutToolDef(), t.forgetGraphToolDef())
		}
	}
	// Working notes (rewritable running-state block) — its own opt-in layer,
	// independent of the Explicit/Reference memory toggles.
	if t.agent.EnableNotes {
		tools = append(tools, t.updateNotesToolDef())
		toolNames = append(toolNames, "update_notes")
	}
	// create_pipeline_tool intentionally NOT added — add_tool with
	// mode="pipeline" is the single pipeline-authoring surface.
	// See the matching note in runPlan above.
	// Worker step catalog — always include agents(run). Builder's
	// orchestrator-round restriction (no run) is structural for the
	// chat-facing conductor turn, where Builder→Chat→Builder cycles
	// can form. Worker steps spawned via plan_set are different:
	// they smoke-test newly-created agents ("Step N (verify): worker
	// dispatches to the new agent with a representative input"), and
	// stripping run here breaks that pattern.
	tools = append(tools, t.agentsGroupedToolDef(true))
	if !t.agent.Fleet {
		// Fleet agents schedule through create_standing_agent, not the generic
		// per-session recurring scheduler — see runPlan's note.
		tools = append(tools, t.recurringToolDef())
	}
	tools = append(tools, t.openSessionToolDef())
	if t.agent.AllowExplorer {
		tools = append(tools, t.enterExplorerModeToolDef())
	}
	// Include persistent temp tools in the static set so the group
	// rewriter (called below) can collapse them when they're members
	// of an admin-curated group. Without this, temp tools come in
	// via DynamicTools after the rewriter has already run and stay
	// visible at the top level even when grouped — the LLM then sees
	// both the toolbox AND the individual temp tools and picks
	// non-deterministically. Matches the runPlan fix.
	staticTempTools := temptool.BuildAgentToolDefs(sess)
	tools = append(tools, staticTempTools...)
	if t.staticTempToolNames == nil {
		t.staticTempToolNames = map[string]bool{}
	}
	for _, td := range staticTempTools {
		toolNames = append(toolNames, td.Tool.Name)
		t.staticTempToolNames[td.Tool.Name] = true
	}
	// Private-mode backstop for worker-step catalog — same pattern as
	// runPlan. Drops any AgentToolDef that declares CapNetwork in its
	// Tool.Caps (the `agents` grouped tool, any temp/source-hook tool
	// appended above with a network cap). Without this, a Private
	// orchestrator turn could plan a step, then the worker step gets
	// `agents` + dispatch its way to a network-capable sub-agent.
	if t.privateMode {
		filtered := tools[:0]
		filteredNames := toolNames[:0]
		nameSet := map[string]bool{}
		dropped := []string{}
		for _, td := range tools {
			hasNet := false
			for _, c := range td.Tool.Caps {
				if c == CapNetwork {
					hasNet = true
					break
				}
			}
			if hasNet {
				dropped = append(dropped, td.Tool.Name)
				continue
			}
			filtered = append(filtered, td)
			nameSet[td.Tool.Name] = true
		}
		for _, n := range toolNames {
			if nameSet[n] {
				filteredNames = append(filteredNames, n)
			}
		}
		tools = filtered
		toolNames = filteredNames
		if len(dropped) > 0 {
			Log("[orchestrate.tools] step %d private mode dropped %d network-capable tool(s): %v",
				cur.ID, len(dropped), dropped)
		}
	}
	Log("[orchestrate.tools] step %d resolved %d tools: %v",
		cur.ID, len(tools), toolNames)

	var ctxBlock strings.Builder
	if priorTurn := t.priorAssistantContext(); priorTurn != "" {
		ctxBlock.WriteString(priorTurn)
	}
	if len(prior) > 0 {
		ctxBlock.WriteString("## Earlier steps in this plan\n\n")
		for _, p := range prior {
			fmt.Fprintf(&ctxBlock, "### Step %d: %s\n%s\n\n", p.ID, p.Title, strings.TrimSpace(p.Output))
		}
	}
	stepUser := fmt.Sprintf(
		"## Original user request\n%s\n\n%s%s%s## Your task (step %d)\n%s",
		userMsg,
		notesContextBlock(notes),
		t.toolLogPromptSection(),
		ctxBlock.String(),
		cur.ID,
		cur.Title,
	)

	// Stream the worker's content into nothing visible — the runner
	// emits the final step output via the plan block in the chat
	// pane. Capturing chunks here gives a non-empty fallback when
	// the LLM responds in reasoning-only.
	var fullOut strings.Builder
	stream := func(chunk string) { fullOut.WriteString(chunk) }

	// Tag every tool call this worker makes with a "↳ [worker: <Title>]"
	// nesting prefix so the user can tell at a glance which calls are
	// the orchestrator's vs the worker's. Without this, plan_set's
	// inner tool_def / create_agent / etc. chips blend visually with
	// Builder's own chips and the conversation reads as if Builder
	// authored them inline — defeating the whole conductor metaphor.
	// Mirrors the agents(run) sub-dispatch nesting ("↳ [TargetName] …").
	workerLabel := strings.TrimSpace(cur.Title)
	if workerLabel == "" {
		workerLabel = "step"
	}
	if len(workerLabel) > 32 {
		workerLabel = workerLabel[:32] + "…"
	}
	t.wrapToolsForActivity(sess, tools, t.agent, "↳ [worker: "+workerLabel+"] ")

	// Collapse admin-curated tool groups. Workers may have a totally
	// different tool surface than the orchestrator (each step
	// resolves its own AllowedTools), so each step needs its own
	// (Runtime tool-group rewriting retired — see the matching note
	// in runPlan. Vector pre-selection on the orchestrator's side
	// handles catalog-saturation; worker steps run on whatever
	// surface the orchestrator picked. find_tools is still the
	// LLM's explicit-search fallback.)

	// Compose the worker's system prompt:
	//   1. The orchestrator's per-step brief (cur.WorkerBrief), or a
	//      minimal fallback derived from title + intent when the
	//      orchestrator didn't author one.
	//   2. Rules + memory prepended via the standard agent context
	//      helper (so the worker honors the agent's policy + prior
	//      learning even though it has no user-authored persona).
	//   3. The framework-side tool-use directive when tools are
	//      present, so the worker prefers tool calls over fabricating
	//      from training for time-sensitive or specific information.
	brief := strings.TrimSpace(cur.WorkerBrief)
	if brief == "" {
		brief = fmt.Sprintf(
			"Execute this step: %s. %s\n\nBe direct, factual, no preamble.",
			cur.Title,
			strings.TrimSpace(cur.Intent),
		)
	}
	sysPrompt := prependAgentContext(t.gatedPersona(brief), t.agent, t.facts(), t.operatingNotes())
	if len(tools) > 0 {
		sysPrompt += "\n\n" + buildToolUseDirective(tools)
	}
	sysPrompt += noWebAccessNotice(tools)
	if frag := RenderToolPromptFragments(tools); frag != "" {
		sysPrompt += "\n\n" + frag
	}
	// Universal authoring directives for Builder-spawned workers.
	// Workers don't see Builder's OrchestratorPrompt (only the brief
	// + per-tool fragments), so the cross-cutting rules that apply
	// to any script the worker writes — URL encoding, no pip install,
	// hook usage, script-body patterns, etc. — never reach them
	// otherwise. Injected here for any worker whose parent agent is
	// Builder. Kept short on purpose: the rules that catch the most
	// common authoring mistakes, no narrative.
	if isBuilderAgent(t.agent.ID) {
		// builderWorkerDirectives is appended after prependAgentContext and this
		// worker path skips appendAgentCapabilityBlocks, so run the mode-aware
		// rewrite here too (the directives name store_fact in prose).
		sysPrompt += "\n\n" + rewriteMemoryToolNames(builderWorkerDirectives)
		sysPrompt += sandboxPythonNoteSection()
	}

	stopKeepalive := startKeepalive(t.sse)
	f := false
	// Reset explorer state per worker step so a step starts at the
	// soft cap; the LLM must opt back into explorer mode if it
	// still needs more rounds.
	t.explorerMode = false
	t.explorerReason = ""
	softCap := resolveMaxWorkerRounds(t.agent)
	hardCap := softCap
	if t.agent.AllowExplorer && softCap < explorerHardCap {
		hardCap = explorerHardCap
	}
	roundsUsed := 0
	// The step's own composition. The plan round is not the turn: a turn runs
	// one plan call and a call per step, each re-sending the whole catalog, and
	// the usage report totals ALL of them — so a 43k prompt and a 212k turn are
	// the same turn seen from two ends. Without this line the difference has to
	// be inferred, and it was: the 212k read as one enormous prompt for days.
	logPromptComposition(fmt.Sprintf("step %d", cur.ID), t.chatSessionID(), sysPrompt, tools,
		[]Message{{Role: "user", Content: stepUser}})
	resp, _, err := t.app.RunAgentLoop(t.ctx, []Message{{Role: "user", Content: stepUser}}, AgentLoopConfig{
		SendGuardKey:         sendGuardKey,
		SystemPrompt:         sysPrompt,
		Tools:                tools,
		DynamicTools:         t.dynamicNewTempTools(sess),
		ToolFallbackResolver: t.lazyToolFallback,
		MaxRounds:            hardCap,
		ThinkBudget:          t.agent.ThinkBudget, // per-agent override; 0 = inherit route/global
		ActionQuotas:         t.agent.ActionQuotas,
		BudgetKey:            t.agent.ID,
		DailySpendUSD:        t.agent.DailySpendUSD,
		Stream:               stream,
		// Same turn-scoped notes the orchestrator round gets. A worker step is
		// where the work usually actually runs, so leaving them out would hand the
		// context to the layer that plans and withhold it from the one that acts.
		TurnNotes: func(user string) string { return turnNotes(sess, t.udb, t.chatSessionID(), user) },
		// Last look before the reply goes out: is it true about what this turn
		// actually did? Backstops the phrase-list guards on the shapes they don't
		// know. See turn_judge.go.
		TurnClaimJudge: t.app.turnClaimJudge(t.ctx),
		// What ran for this turn BEFORE the loop did — the machine steps. The
		// loop cannot see them (they run during system-prompt assembly, on a
		// session of their own), so without this the judge reads a turn whose
		// step did the searching as a turn that did nothing and convicts the
		// reply for reporting it.
		PriorWork: t.priorWorkForJudge,
		// And what this agent's own scheduled runs already filed into the
		// thread — the work a recap is about, which this turn did not do.
		PriorReports: t.priorReportsForJudge,
		// And whether the reply KNOWS what it asserts. Scope is the notes the
		// memory block marked unchecked, so a turn holding none never reaches a
		// model call. See grounding_judge.go.
		TurnGroundingJudge: t.app.turnGroundingJudge(t.ctx),
		UncheckedClaims:    UncheckedFactNotes(t.facts()),
		DeliveredCount:     func() int { return len(sess.Images) + len(sess.Videos) + len(sess.Files) },
		Backgrounded:       func() bool { return sess.Detach.Any() },
		BackgroundEstimate: func() string { return sess.Detach.EstimateText() },
		// Worker-step corrections breadcrumb into the same session trail as
		// the orchestrator loop — a silent re-prompt during a plan step is
		// still a framework decision the user should be able to see.
		OnDiag: t.turnDiag,
		// One-shot advice for the failure-shape guard: three hits on one wall
		// means the arguments aren't what's wrong, so ask a model that can
		// ANSWER instead of nudging the one that's stuck. See consult.go.
		Consult: t.consult,
		// OnStep feeds telemetry — rounds, tool calls, dup-args
		// fingerprints. Summary log fires from the deferred block at
		// the top of runWorkerStep.
		OnStep: func(info StepInfo) { telem.record(info) },
		// Soft-cap enforcement for explorer-mode agents: pass hardCap
		// as MaxRounds upfront, then stop early at softCap UNLESS the
		// LLM has flipped explorerMode via enter_explorer_mode. For
		// non-explorer agents softCap == hardCap so StopRound never
		// fires until both are exhausted.
		StopRound: func() bool {
			roundsUsed++
			if t.explorerMode {
				return false
			}
			return roundsUsed > softCap
		},
		// Escalation policy hook (confirm.go) — same policy as the
		// orchestrator loop: flagged-credential calls park on the
		// in-chat approval card; everything else auto-approves (no
		// stdin fallback — gohort runs as a service).
		Confirm:             t.confirmFuncFor(sess),
		GuardrailCheck:      t.guardrailEnforcer().Check,
		GuardrailActionGate: t.guardrailEnforcer().ActionGate,
		GuardrailHalted:     t.guardrailEnforcer().Halted,
		GuardrailReject:     t.guardrailEnforcer().Reject,
		GuardrailDeclines:   t.agent.GuardrailDeclines,
		// A step must be able to END ITSELF. Without these, respond_directly
		// inside a worker step is an ordinary tool call: it returns, the round
		// completes, and the loop takes another turn — observed as a step that
		// decided it was finished and then ran to nine rounds and six tool
		// errors anyway. Same set the orchestrator round declares, minus
		// plan_set: a step does not get to re-plan the turn it belongs to.
		RoundAbortTools: workerAbortTools,
		// (No SingleFireGroups for image/video producers — same
		// rationale as the orchestrator round above. Multi-fire is
		// intentional under the write-to-workspace + workspace(attach)
		// architecture.)
		ChatOptions: []ChatOption{
			WithRouteKey("app.orchestrate.worker"),
			WithThink(f),
		},
	})
	stopKeepalive()
	// A step can close the whole turn too: stay_silent inside a step means the
	// same thing it means anywhere else, and the driver checks this before the
	// next step runs.
	if sess != nil && sess.Silenced {
		t.turnClosed = true
	}
	if err != nil {
		return "", err
	}
	out := fullOut.String()
	if out == "" && resp != nil {
		out = resp.Content
	}
	// Defensive markup strip — models sometimes emit prompt-style
	// <tool_call><function=...>...</function></tool_call> markup IN
	// their text content (especially when emitting multiple calls in
	// one response). The agent loop only dispatches the first parsed
	// call and strips its markup from history, but a parse failure
	// or extra unparsed blocks leak markup into the step output.
	// Always sweep before returning so the user never sees raw XML.
	out = StripToolCallMarkup(out)
	out = strings.TrimSpace(out)
	if out == "" {
		out = "(no output)"
	}
	// Finalize the per-step bubble (if a tool created one via
	// ensureBubbleForTool) so it sheds the streaming class and any
	// app-side decorators fire. Idempotent — no-op when no bubble
	// was created this step.
	if id := t.getCurrentMsgID(); id != "" {
		t.sse.Send(map[string]any{"kind": "message_done", "id": id})
		t.setCurrentMsgID("")
	}
	return out, nil
}

// runSynthesis has the orchestrator compose the final user-facing
// reply given the original message + every worker step output. Streamed
// to the user via SSE chunk events as it generates.
func (t *chatTurn) runSynthesis(userMsg string, steps []PlanStep, notes []injectionNote) (string, error) {
	// Build the LLM message array as full conversation history with
	// the synthesis-flavored content REPLACING the latest user turn
	// (handleSend already appended the user's current message; we
	// strip it and re-add a beefed-up version that folds in worker
	// findings + mid-flight notes). This gives the synthesizer the
	// same multi-turn continuity the orchestrator gets, so follow-
	// ups like "tell me more about that" land with the right
	// referent instead of being synthesized in isolation.
	var msgs []Message
	if t.session != nil {
		hist := t.session.Messages
		if n := len(hist); n > 0 && hist[n-1].Role == "user" {
			hist = hist[:n-1]
		}
		msgs = toLLMMessages(hist)
	}

	var body strings.Builder
	body.WriteString(userMsg)
	body.WriteString("\n\n")
	if nb := notesContextBlock(notes); nb != "" {
		body.WriteString(nb)
	}
	body.WriteString("## Worker findings for this turn (internal context — the user can't see this)\n\n")
	for _, s := range steps {
		fmt.Fprintf(&body, "### Step %d: %s\n%s\n\n", s.ID, s.Title, strings.TrimSpace(s.Output))
	}
	body.WriteString("\n## Your task\nCompose the final reply to the user's message above. Use the worker findings as your source material; integrate them naturally with the prior conversation context. Don't restate the plan unless it helps explain the result, and don't repeat content already established earlier in the conversation.")
	msgs = append(msgs, Message{Role: "user", Content: body.String()})

	// Allocate a stable message id so SSE chunk events know which
	// bubble in the conversation pane to stream into.
	msgID := fmt.Sprintf("synth-%d", time.Now().UnixNano())
	t.sse.Send(map[string]any{
		"kind": "message",
		"role": "assistant",
		"id":   msgID,
		"text": "",
	})

	var full strings.Builder
	handler := func(chunk string) {
		full.WriteString(chunk)
		t.sse.Send(map[string]any{
			"kind": "chunk",
			"id":   msgID,
			"text": chunk,
		})
	}
	stopKeepalive := startKeepalive(t.sse)
	// Same route stage as the plan round; honors its thinking preference,
	// and — through ChatStreamWithReport below — its TIER. This round used
	// to call t.app.LLM.ChatStream straight, which meant an agent with "Use
	// Lead model" on planned its turn on the lead and then wrote the reply
	// the user actually reads on the worker, contradicting the stage's own
	// documented scope ("plan + synthesis"). It also meant not one token of
	// the largest prompt in the turn — the whole conversation plus every
	// tool result — reached the usage tracker, so the admin cost history
	// under-counted every agent turn by roughly a synthesis prompt.
	// Read from t.machine rather than the plan round's copy: change_phase can
	// move the phase mid-turn, and the reply belongs to where the turn ENDED.
	tierPin, routeKey := t.turnRouting()
	think := true
	if p := RouteThink(routeKey); p != nil {
		think = *p
	}
	// Per-agent override wins over the route default (see plan round
	// for rationale).
	switch t.agent.Think {
	case "on":
		think = true
	case "off":
		think = false
	}
	synthStart := time.Now()
	// Same skill injection the orchestrator round saw — synthesis
	// IS the user-facing voice, so any skill the LLM activated this
	// turn must shape the final reply as well. The orchestrator
	// round sees the skill via the tool result in conversation
	// history; synthesis builds a fresh system prompt and re-injects
	// here so a skill that prescribed a tone or reference convention
	// governs both planning and reply. No-op when no skills active.
	synthSys := prependAgentContext(t.gatedPersona(t.agent.OrchestratorPrompt), t.agent, t.facts(), t.operatingNotes())
	// Re-inject any trigger-matched skill instructions so a skill that
	// prescribed a tone/convention governs the synthesis reply too.
	// Re-inject the full instructions of any skill the LLM consulted this
	// turn so its lens governs the synthesis reply. No trigger hints here —
	// synthesis has no tools, so a "go consult it" nudge would be useless.
	synthSys += t.renderTriggeredSkills()
	resp, err := t.app.ChatStreamWithReport(t.ctx,
		msgs,
		handler,
		WithSystemPrompt(synthSys),
		WithRouteKey(routeKey),
		WithTierOverride(tierPin), // the phase's pin, which the route key alone cannot carry
		WithThink(think),
	)
	stopKeepalive()
	if err != nil {
		return "", err
	}
	reply := full.String()
	// Thinking models sometimes emit their entire response in the
	// reasoning channel — the content stream stays silent and resp.Content
	// is only populated after agent_loop promotes reasoning. Catch that
	// case and flush as a single chunk so the bubble isn't empty.
	if reply == "" && resp != nil && strings.TrimSpace(resp.Content) != "" {
		reply = resp.Content
		t.sse.Send(map[string]any{
			"kind": "chunk",
			"id":   msgID,
			"text": reply,
		})
	}
	t.sse.Send(map[string]any{"kind": "message_done", "id": msgID})
	t.emitStats(msgID, resp, synthStart)
	// Scrub framework-internal markers AND enforce the house style on the
	// saved/exported copy (the client also strips em-dashes on render — see
	// uiRenderMarkdown). Cheap no-op when none is present.
	//
	// The "classic" rule is enforced HERE and not in the prompt because the
	// prompt already asks and is already ignored: a worker-tier reply broke
	// both this rule and the em-dash one in a single conversation. A tic that
	// is a pure function of the text belongs at the output boundary.
	return strings.TrimSpace(prompts.ApplyRuleEnforcers(StripMetaTags(reply))), nil
}
