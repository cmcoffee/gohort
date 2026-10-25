package orchestrate

import (
	"fmt"
	"slices"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/textutil"
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
//
// The step's state is workerStep and each phase is a method on it. A phase
// that ends the step early returns false, and what it handed back is parked
// by exit for this method to return through the named results the deferred
// summary reads.
func (t *chatTurn) runWorkerStep(prior []PlanStep, cur PlanStep, userMsg string, notes []injectionNote) (stepOut string, stepErr error) {
	ws := &workerStep{t: t, prior: prior, cur: cur, userMsg: userMsg, notes: notes}
	ws.startTelemetry()
	defer func() {
		softCap := resolveMaxWorkerRounds(t.agent)
		hardCap := softCap
		if t.agent.AllowExplorer {
			if ec := resolveExplorerHardCap(t.agent); ec > hardCap {
				hardCap = ec
			}
		}
		exitReason := classifyWorkerExit(stepErr, ws.telem.rounds, softCap, len(stepOut))
		label := fmt.Sprintf("orchestrate.worker step=%d", cur.ID)
		Log("%s", ws.telem.summary(label, softCap, hardCap, exitReason)+" agent="+t.agent.ID)
		if line := ws.telem.toolCallSummary(label); line != "" {
			Log("%s", line)
		}
		// Churn is a user-visible outcome, not just a log line — route it
		// to the ⚠ trail so the person who saw the turn go wrong can see
		// WHY without reading server logs.
		if kind, detail, ok := ws.telem.churnDiag(); ok {
			t.turnDiag(kind, detail)
		}
	}()
	ws.openSession()
	// Persist any managed-workspace switch this step performs so the
	// next step's fresh session shares the same workspace (a script
	// written here is then runnable from the following step).
	defer t.captureActiveWorkspace(ws.sess)
	if !ws.resolveTools() {
		return ws.ret, ws.retErr
	}
	ws.addFrameworkTools()
	ws.narrowForTurn()
	ws.buildPrompt()
	if !ws.runLoop() {
		return ws.ret, ws.retErr
	}
	ws.finish()
	return ws.ret, ws.retErr
}

// workerStep is one plan step run by the worker, from its tool surface to
// the output the plan block shows. Each field is a local or parameter of the
// method it was cut from; runWorkerStep is now the order of the phases and
// the defers between them.
type workerStep struct {
	t               *chatTurn
	prior           []PlanStep
	cur             PlanStep
	userMsg         string
	notes           []injectionNote
	telem           *turnTelemetry
	stamp           *buildStamper
	sess            *ToolSession
	tools           []AgentToolDef
	toolNames       []string
	err             error
	staticTempTools []AgentToolDef
	ctxBlock        strings.Builder
	stepUser        string
	// Stream the worker's content into nothing visible — the runner
	// emits the final step output via the plan block in the chat
	// pane. Capturing chunks here gives a non-empty fallback when
	// the LLM responds in reasoning-only.
	fullOut       strings.Builder
	workerLabel   string
	brief         string
	sysPrompt     string
	stopKeepalive func()
	f             bool
	softCap       int
	hardCap       int
	roundsUsed    int
	resp          *Response
	out           string
	// ret and retErr are what the step hands back, parked by exit.
	ret    string
	retErr error
}

// exit records what an early return hands back and tells the driver to take
// it.
func (ws *workerStep) exit(out string, err error) bool {
	ws.ret, ws.retErr = out, err
	return false
}

func (ws *workerStep) startTelemetry() {
	t := ws.t
	// Per-step telemetry — same instrumentation as the orchestrator's
	// runPlan. Each worker step gets its own round budget and its own
	// telem; the summary log fires when the step exits.
	ws.telem = newTurnTelemetry()
	ws.stamp = newBuildStamper(t.chatSessionID())
}

func (ws *workerStep) openSession() {
	t := ws.t
	// Reset the active-bubble id at each worker-step boundary so the
	// first tool call of THIS step materializes its own card under
	// the just-emitted intent block, instead of attaching to the
	// previous step's bubble. (currentMsgID lingers from the
	// orchestrator's last round / prior step otherwise.)
	t.setCurrentMsgID("")

	// Build the worker's per-call session — same helper the
	// orchestrator uses, so persistent temp tools land on both paths.
	ws.sess = t.newToolSession()
}

func (ws *workerStep) resolveTools() bool {
	t := ws.t
	cur := ws.cur
	// forOrchestrator=false — this is the per-step worker running one
	// plan_set step. The Builder branch returns builderWorkerResearchTools
	// (raw call_<credential> for endpoint probing + the full authoring
	// catalog: tool_def / create_agent / add_tool / skill_def) on top of
	// the standard AllowedTools pool. Workers can either author directly
	// (call tool_def themselves and return confirmation) or return
	// components for Builder to assemble. The framework doesn't dictate;
	// Builder's brief decides which shape the worker takes.
	ws.tools, ws.toolNames, ws.err = t.resolveWorkerTools(ws.sess, false)
	if ws.err != nil {
		return ws.exit("", fmt.Errorf("resolve tools: %w", ws.err))
	}
	// The agent's attached sources (file stores, systems, doc spaces) and
	// pipelines, which the lead's round offers directly and a step never got:
	// a step asked to search a store had no search_<store>, and a lead that
	// plans (runner_lead_plans.go) has no other way to reach them.
	for _, td := range append(t.buildAttachedSourceToolDefs(ws.sess), t.buildAttachedPipelineToolDefs()...) {
		if !slices.Contains(ws.toolNames, td.Tool.Name) {
			ws.tools = append(ws.tools, td)
			ws.toolNames = append(ws.toolNames, td.Tool.Name)
		}
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
		keptTools := ws.tools[:0]
		keptNames := ws.toolNames[:0]
		for i, t := range ws.tools {
			if want[t.Tool.Name] {
				keptTools = append(keptTools, t)
				if i < len(ws.toolNames) {
					keptNames = append(keptNames, ws.toolNames[i])
				}
			}
		}
		Log("[orchestrate.worker] step %d (%q): orchestrator picked %d tools → %d resolved",
			cur.ID, cur.Title, len(cur.Tools), len(keptTools))
		ws.tools = keptTools
		ws.toolNames = keptNames
	}
	// Builder's coder (code_app) writes an app, so it gets app_def; no other
	// step does.
	if cur.appCoder && isBuilderAgent(t.agent.ID) {
		for _, td := range t.authoringPrimitiveToolDefs() {
			if td.Tool.Name == "app_def" && !slices.Contains(ws.toolNames, td.Tool.Name) {
				ws.tools = append(ws.tools, td)
				ws.toolNames = append(ws.toolNames, td.Tool.Name)
			}
		}
	}
	ws.tools, ws.toolNames = filterToolAuthoringWithoutFocus(ws.tools, ws.toolNames, t.session)
	t.gateAgentCRUDTools(ws.tools)
	return true
}

func (ws *workerStep) addFrameworkTools() {
	t := ws.t
	// Three layers, each gated by its own helper — same split as
	// the orchestrator catalog in runPlan. Workers are the ones
	// actually researching, so they get write access to the Inferred
	// Memory layer when it's enabled.
	//
	// The corpus comes from corpusToolDefs, the one helper all three
	// callers share (conversational catalog, machine step, worker). This
	// path is the drift it was extracted to prevent and had already
	// drifted twice over: it appended the knowledge pair UNCONDITIONALLY,
	// skipping the "nothing retrievable" gate the other two apply, so a
	// worker on an agent with an empty corpus was handed a search tool
	// over nothing and invited to make up the doc_ids its handler then had
	// to refuse. And it resolved the legacy-vs-unified split itself, in
	// two separate branches, which is the second copy of a decision that
	// now lives in one place.
	for _, td := range t.corpusToolDefs() {
		ws.tools = append(ws.tools, td)
		ws.toolNames = append(ws.toolNames, td.Tool.Name)
	}
	for _, td := range t.skillToolDefs() {
		ws.tools = append(ws.tools, td)
		ws.toolNames = append(ws.toolNames, td.Tool.Name)
	}
	// (dispatch_to_worker temporarily unmounted on the worker step
	// too — same reason as the orchestrator catalog: discoverability
	// problem, not a wiring problem.)
	if !t.explicitOff() {
		ws.tools = append(ws.tools, t.linkEntitiesToolDef(), t.recallAboutToolDef(), t.forgetGraphToolDef())
	}
	// Working notes (rewritable running-state block) — its own opt-in layer,
	// independent of the Explicit/Reference memory toggles.
	if t.agent.EnableNotes {
		ws.tools = append(ws.tools, t.updateNotesToolDef())
		ws.toolNames = append(ws.toolNames, "update_notes")
	}
	// create_pipeline_tool intentionally NOT added — add_tool with
	// mode="pipeline" is the single pipeline-authoring surface.
	// See the matching note in runPlan above.
	// Worker step catalog — always include agents(run). A Builder worker
	// step smoke-tests what Builder made ("Step N (verify): send the new
	// agent a representative input"), and its run reaches only that
	// (builder_run_scope.go).
	// …when there is something to dispatch TO. A worker step that can reach
	// nothing gets no delegation surface; see agentsToolWanted.
	if t.agentsToolWanted() {
		ws.tools = append(ws.tools, t.agentsGroupedToolDef(true))
	}
	if !t.agent.Fleet {
		// Fleet agents schedule through schedule(when="every"), not the generic
		// per-session recurring scheduler — see runPlan's note.
		ws.tools = append(ws.tools, t.recurringToolDef())
	}
	ws.tools = append(ws.tools, t.openSessionToolDef())
	if t.agent.AllowExplorer {
		ws.tools = append(ws.tools, t.enterExplorerModeToolDef())
	}
	// Include persistent temp tools in the static set so the group
	// rewriter (called below) can collapse them when they're members
	// of an admin-curated group. Without this, temp tools come in
	// via DynamicTools after the rewriter has already run and stay
	// visible at the top level even when grouped — the LLM then sees
	// both the toolbox AND the individual temp tools and picks
	// non-deterministically. Matches the runPlan fix.
	ws.staticTempTools = temptool.BuildAgentToolDefs(ws.sess)
	ws.tools = append(ws.tools, ws.staticTempTools...)
	if t.staticTempToolNames == nil {
		t.staticTempToolNames = map[string]bool{}
	}
	for _, td := range ws.staticTempTools {
		ws.toolNames = append(ws.toolNames, td.Tool.Name)
		t.staticTempToolNames[td.Tool.Name] = true
	}
}

func (ws *workerStep) narrowForTurn() {
	t := ws.t
	cur := ws.cur
	// Private-mode backstop for worker-step catalog — same pattern as
	// runPlan. Drops any AgentToolDef that declares CapNetwork in its
	// Tool.Caps (the `agents` grouped tool, any temp/source-hook tool
	// appended above with a network cap). Without this, a Private
	// orchestrator turn could plan a step, then the worker step gets
	// `agents` + dispatch its way to a network-capable sub-agent.
	if t.privateMode {
		filtered := ws.tools[:0]
		filteredNames := ws.toolNames[:0]
		nameSet := map[string]bool{}
		dropped := []string{}
		for _, td := range ws.tools {
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
		for _, n := range ws.toolNames {
			if nameSet[n] {
				filteredNames = append(filteredNames, n)
			}
		}
		ws.tools = filtered
		ws.toolNames = filteredNames
		if len(dropped) > 0 {
			Log("[orchestrate.tools] step %d private mode dropped %d network-capable tool(s): %v",
				cur.ID, len(dropped), dropped)
		}
	}
	// A worker step runs as the same agent and is bound by the same rules, so
	// it loses the same tools. Names kept in step: the log below is what gets
	// read when a tool is missing, and a name list that disagrees with the
	// catalog is worse than no list.
	if withheld := t.guardrailWithheldTools(); len(withheld) > 0 {
		ws.tools = t.applyGuardrailToolWithholding(ws.tools)
		kept := ws.toolNames[:0]
		for _, n := range ws.toolNames {
			if _, gone := withheld[n]; !gone {
				kept = append(kept, n)
			}
		}
		ws.toolNames = kept
	}
	Log("[orchestrate.tools] step %d resolved %d tools: %v",
		cur.ID, len(ws.tools), ws.toolNames)
}

func (ws *workerStep) stream(chunk string) {
	ws.fullOut.WriteString(chunk)
}

func (ws *workerStep) buildPrompt() {
	t := ws.t
	prior := ws.prior
	cur := ws.cur
	userMsg := ws.userMsg
	notes := ws.notes
	if priorTurn := t.priorAssistantContext(); priorTurn != "" {
		ws.ctxBlock.WriteString(priorTurn)
	}
	if len(prior) > 0 {
		ws.ctxBlock.WriteString("## Earlier steps in this plan\n\n")
		for _, p := range prior {
			fmt.Fprintf(&ws.ctxBlock, "### Step %d: %s\n%s\n\n", p.ID, p.Title, strings.TrimSpace(p.Output))
		}
	}
	ws.stepUser = fmt.Sprintf(
		"## Original user request\n%s\n\n%s%s%s## Your task (step %d)\n%s",
		userMsg,
		notesContextBlock(notes),
		t.toolLogPromptSection(),
		ws.ctxBlock.String(),
		cur.ID,
		cur.Title,
	)

	// Tag every tool call this worker makes with a "↳ [worker: <Title>]"
	// nesting prefix so the user can tell at a glance which calls are
	// the orchestrator's vs the worker's. Without this, plan_set's
	// inner tool_def / create_agent / etc. chips blend visually with
	// Builder's own chips and the conversation reads as if Builder
	// authored them inline — defeating the whole conductor metaphor.
	// Mirrors the agents(run) sub-dispatch nesting ("↳ [TargetName] …").
	ws.workerLabel = strings.TrimSpace(cur.Title)
	if ws.workerLabel == "" {
		ws.workerLabel = "step"
	}
	ws.workerLabel = textutil.Clip(ws.workerLabel, 32)
	t.wrapToolsForActivity(ws.sess, ws.tools, t.agent, "↳ [worker: "+ws.workerLabel+"] ")

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
	ws.brief = strings.TrimSpace(cur.WorkerBrief)
	if ws.brief == "" {
		ws.brief = fmt.Sprintf(
			"Execute this step: %s. %s\n\nBe direct, factual, no preamble.",
			cur.Title,
			strings.TrimSpace(cur.Intent),
		)
	}
	ws.sysPrompt = prependAgentContext(t.gatedPersona(ws.brief), t.agent, t.facts(), t.operatingNotes())
	if len(ws.tools) > 0 {
		ws.sysPrompt += "\n\n" + buildToolUseDirective(ws.tools)
	}
	ws.sysPrompt += noWebAccessNotice(ws.tools)
	if frag := RenderToolPromptFragments(ws.tools); frag != "" {
		ws.sysPrompt += "\n\n" + frag
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
		ws.sysPrompt += "\n\n" + rewriteMemoryToolNames(builderWorkerDirectives)
		ws.sysPrompt += sandboxPythonNoteSection()
	}
}

func (ws *workerStep) runLoop() bool {
	t := ws.t
	cur := ws.cur
	ws.stopKeepalive = startKeepalive(t.sse)
	ws.f = false
	// Builder's coder thinks: it is writing a whole app from a design, the
	// one step whose quality comes from working it out first (Qwen built a
	// designed app one-shot mostly by thinking). Its budget is the agent's,
	// as any step's is.
	if cur.appCoder {
		ws.f = true
	}
	ws.f = chainThink(t.ctx, ws.f)
	// Reset explorer state per worker step so a step starts at the
	// soft cap; the LLM must opt back into explorer mode if it
	// still needs more rounds.
	t.explorerMode = false
	t.explorerReason = ""
	ws.softCap = resolveMaxWorkerRounds(t.agent)
	ws.hardCap = ws.softCap
	if t.agent.AllowExplorer && ws.softCap < explorerHardCap {
		ws.hardCap = explorerHardCap
	}
	ws.roundsUsed = 0
	// The step's own composition. The plan round is not the turn: a turn runs
	// one plan call and a call per step, each re-sending the whole catalog, and
	// the usage report totals ALL of them — so a 43k prompt and a 212k turn are
	// the same turn seen from two ends. Without this line the difference has to
	// be inferred, and it was: the 212k read as one enormous prompt for days.
	logPromptComposition(fmt.Sprintf("step %d", cur.ID), t.chatSessionID(), ws.sysPrompt, ws.tools,
		[]Message{{Role: "user", Content: ws.stepUser}})
	ws.resp, _, ws.err = t.app.RunAgentLoop(t.ctx, []Message{{Role: "user", Content: ws.stepUser}}, AgentLoopConfig{
		SendGuardKey:         sendGuardKey,
		SystemPrompt:         ws.sysPrompt,
		Tools:                ws.tools,
		DynamicTools:         t.dynamicNewTempTools(ws.sess),
		ToolFallbackResolver: t.lazyToolFallback,
		MaxRounds:            ws.hardCap,
		ThinkBudget:          t.agent.ThinkBudget, // per-agent override; 0 = inherit route/global
		Effort:               t.agent.Effort,      // per-agent level; a budget above wins
		ActionQuotas:         t.agent.ActionQuotas,
		BudgetKey:            t.agent.ID,
		DailySpendUSD:        t.agent.DailySpendUSD,
		SharedBudgets:        familyBudgets(t.agent),
		Stream:               ws.stream,
		OnResponse:           t.thinkResponse, // the footer totals every call of the turn
		// Same turn-scoped notes the orchestrator round gets. A worker step is
		// where the work usually actually runs, so leaving them out would hand the
		// context to the layer that plans and withhold it from the one that acts.
		// All but the unkept promise from the agent's last turn, which is keyed
		// by the session: a worker never said it, and was told "Do it NOW" about
		// a step that may not be its own.
		TurnNotes: func(user string) string { return turnNotes(ws.sess, t.udb, "", user) },
		// Last look before the reply goes out: is it true about what this turn
		// actually did? Backstops the phrase-list guards on the shapes they don't
		// know. See turn_judge.go.
		CapturePrompt:  t.agent.CapturePrompt,
		TurnClaimJudge: t.claimJudge(t.ctx),
		// What ran for this turn BEFORE the loop did — the machine steps. The
		// loop cannot see them (they run during system-prompt assembly, on a
		// session of their own), so without this the judge reads a turn whose
		// step did the searching as a turn that did nothing and convicts the
		// reply for reporting it.
		PriorWork: t.priorWorkForJudge,
		// And what this agent's own scheduled runs already filed into the
		// thread — the work a recap is about, which this turn did not do.
		PriorReports: t.priorReportsForJudge,
		// And what EARLIER turns of this conversation ran, so a reply asked to
		// write up the work so far is read as the recap it is rather than as a
		// claim about a turn that called nothing.
		PriorTurnWork: t.priorTurnWorkForJudge,
		// And whether the reply KNOWS what it asserts. Scope is the notes the
		// memory block marked unchecked, so a turn holding none never reaches a
		// model call. See grounding_judge.go.
		TurnGroundingJudge: t.app.turnGroundingJudge(t.ctx),
		UncheckedClaims:    t.uncheckedClaims(t.facts()),
		DeliveredCount:     func() int { return len(ws.sess.Images) + len(ws.sess.Videos) + len(ws.sess.Files) },
		Backgrounded:       func() bool { return ws.sess.Detach.Any() },
		BackgroundEstimate: func() string { return ws.sess.Detach.EstimateText() },
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
		OnStep: func(info StepInfo) { ws.telem.record(info); ws.stamp.step(info) },
		// The step's prompt clauses, for the build ledger's stamps.
		OnPromptDigest: ws.stamp.digest,
		// Soft-cap enforcement for explorer-mode agents: pass hardCap
		// as MaxRounds upfront, then stop early at softCap UNLESS the
		// LLM has flipped explorerMode via enter_explorer_mode. For
		// non-explorer agents softCap == hardCap so StopRound never
		// fires until both are exhausted.
		StopRound: func() bool {
			ws.roundsUsed++
			if t.explorerMode {
				return false
			}
			return ws.roundsUsed > ws.softCap
		},
		// Escalation policy hook (confirm.go) — same policy as the
		// orchestrator loop: flagged-credential calls park on the
		// in-chat approval card; everything else auto-approves (no
		// stdin fallback — gohort runs as a service).
		Confirm:             t.confirmFuncFor(ws.sess),
		ConfirmRefusal:      t.confirmRefusal,
		AskFirst:            t.asksFirst, // the owner's ask marks reach every tool (ask_first.go)
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
			WithThink(ws.f),
		},
	})
	ws.stopKeepalive()
	// A step can close the whole turn too: stay_silent inside a step means the
	// same thing it means anywhere else, and the driver checks this before the
	// next step runs.
	if ws.sess != nil && ws.sess.Silenced {
		t.turnClosed = true
	}
	if ws.err != nil {
		return ws.exit("", ws.err)
	}
	return true
}

func (ws *workerStep) finish() bool {
	t := ws.t
	ws.out = ws.fullOut.String()
	if ws.out == "" && ws.resp != nil {
		ws.out = ws.resp.Content
	}
	// Defensive markup strip — models sometimes emit prompt-style
	// <tool_call><function=...>...</function></tool_call> markup IN
	// their text content (especially when emitting multiple calls in
	// one response). The agent loop only dispatches the first parsed
	// call and strips its markup from history, but a parse failure
	// or extra unparsed blocks leak markup into the step output.
	// Always sweep before returning so the user never sees raw XML.
	ws.out = StripToolCallMarkup(ws.out)
	ws.out = strings.TrimSpace(ws.out)
	if ws.out == "" {
		ws.out = "(no output)"
	}
	// Finalize the per-step bubble (if a tool created one via
	// ensureBubbleForTool) so it sheds the streaming class and any
	// app-side decorators fire. Idempotent — no-op when no bubble
	// was created this step.
	if id := t.getCurrentMsgID(); id != "" {
		t.sse.Send(map[string]any{"kind": "message_done", "id": id})
		t.setCurrentMsgID("")
	}
	return ws.exit(ws.out, nil)
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
	body.WriteString("## Worker findings for this turn (internal context: the user can't see this)\n\n")
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
	switch t.agent.thinkMode() {
	case "on":
		think = true
	case "off":
		think = false
	}
	think = chainThink(t.ctx, think)
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
	t.thinkNewCall()
	resp, err := t.app.ChatStreamWithReport(t.ctx,
		msgs,
		handler,
		WithSystemPrompt(synthSys),
		WithRouteKey(routeKey),
		WithTierOverride(tierPin), // the phase's pin, which the route key alone cannot carry
		WithThink(think),
		WithReasoningStream(t.thinkChunk), // the live "Thinking" line, as the orchestrator round has
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
	t.thinkResponse(resp)
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
