// Eval harness — run an agent's saved test cases (AgentRecord.Evals)
// and report pass/fail per case. Catches prompt regressions after
// edits to orchestrator_prompt, allowed_tools, or agent-scoped tools.
//
// Mechanics per case:
//   1. Spawn a worker-tier RunAgentLoop with the target agent's
//      gated persona + memory + facts + allowed tools.
//   2. Send case.Prompt as the user message.
//   3. Capture the synthesis.
//   4. Grade against MustInclude / MustNotInclude (cheap substring
//      checks) AND optionally JudgePrompt (LLM-as-judge).
//   5. Return per-case results.
//
// Not a faithful 1:1 of the live runPlan/synthesis flow — that path
// is bound to an HTTP session via chatTurn. The eval uses the
// worker-tier loop directly so it can run synchronously off an
// HTTP request. Good enough to catch regressions in persona / tool
// behavior; whole-plan-flow eval is a follow-up if needed.

package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const evalsRunTimeout = 5 * time.Minute

// RunAgentEvals executes every case on the agent and returns a result
// row per case. Stops early only on context cancellation; individual
// case errors land on that case's ErrText without aborting the rest.
// stubTools returns copies of the tool defs with side-effect-free handlers for
// eval STUB mode: the schema (name / description / params) is IDENTICAL, so the
// model sees the same catalog and decides the same way, but no real handler runs
// — nothing is texted, no monitor is created, no network is hit. Tool-USE grading
// (via OnStep) is unaffected since the call still happens; only its EFFECT is
// removed. A tool with a scripted result returns it (so a multi-step case reads
// realistically); otherwise a generic notice.
func stubTools(tools []AgentToolDef, scripted map[string]string) []AgentToolDef {
	out := make([]AgentToolDef, len(tools))
	for i, td := range tools {
		name := td.Tool.Name
		canned := strings.TrimSpace(scripted[name])
		out[i] = td // copies schema + flags; only the handler is replaced below
		out[i].Handler = func(args map[string]any) (string, error) {
			if canned != "" {
				return canned, nil
			}
			return fmt.Sprintf("[eval-stub] %s called — no real effect (eval stub mode).", name), nil
		}
	}
	return out
}

// evalExecutor runs ONE case against whatever is being graded and returns the
// graded result.
//
// The seam between the half of evals that is generic and the half that is not.
// Everything above it — run each case N times, aggregate into a pass rate,
// stop when the context is cancelled — is true of grading anything. Everything
// below it is what "send a case" means for one kind of target, and an agent is
// only the first of four.
type evalExecutor func(ctx context.Context, c EvalCase) EvalResult

// runEvalCases is the generic half: N runs per case, aggregated to a rate.
//
// The rate rather than a boolean because a single run of a non-deterministic
// model is an anecdote. Same prefix every run, so the host prompt cache stays
// warm across them.
func runEvalCases(ctx context.Context, cases []EvalCase, runs int, exec evalExecutor) []EvalResult {
	return runEvalCasesWith(ctx, cases, runs, exec, nil)
}

// runEvalCasesWith is runEvalCases with a per-case hook, so a caller streaming
// a suite can report each row as it lands rather than everything at the end.
//
// A thirty-case suite is minutes of work; handing back one blob when it
// finishes means a reader watching it has nothing to watch, and no way to tell
// a slow suite from a stuck one.
func runEvalCasesWith(ctx context.Context, cases []EvalCase, runs int, exec evalExecutor, onCase func(EvalResult)) []EvalResult {
	if runs < 1 {
		runs = 1
	}
	results := make([]EvalResult, 0, len(cases))
	for _, c := range cases {
		if ctx.Err() != nil {
			// Cancelled: return what was graded rather than nothing. A stopped
			// suite still measured the cases it reached.
			return results
		}
		perRun := make([]EvalResult, 0, runs)
		for i := 0; i < runs; i++ {
			if ctx.Err() != nil {
				break
			}
			perRun = append(perRun, exec(ctx, c))
		}
		row := aggregateEvalRuns(c.Name, perRun)
		results = append(results, row)
		if onCase != nil {
			onCase(row)
		}
	}
	return results
}

// RunAgentEvals grades an agent against its own saved cases.
//
// Kept as-is for its existing caller (the per-agent eval endpoint); it is now
// the agent-shaped executor plus the generic loop, which is the whole of the
// generalization — behaviour is unchanged.
func (T *OrchestrateApp) RunAgentEvals(ctx context.Context, udb Database, user string, agent AgentRecord, runs int, stub, allowConsequential bool) []EvalResult {
	return runEvalCases(ctx, agent.Evals, runs, T.agentEvalExecutor(udb, agent, stub, allowConsequential))
}

// agentEvalExecutor resolves an agent's prompt and tools ONCE and returns the
// per-case runner. Resolved once because it is the same set for every case and
// every repeat of it.
func (T *OrchestrateApp) agentEvalExecutor(udb Database, agent AgentRecord, stub, allowConsequential bool) evalExecutor {
	facts := ListMemoryFacts(udb, factsNamespace(agent.ID))

	// Pre-resolve the agent's tool set once; same set for every case.
	toolNames := agent.AllowedTools
	if len(toolNames) == 0 {
		for _, td := range RegisteredChatTools() {
			toolNames = append(toolNames, td.Name())
		}
	}
	tools, err := GetAgentTools(toolNames...)
	if err != nil {
		tools = nil
		for _, n := range toolNames {
			if td, terr := GetAgentTools(n); terr == nil && len(td) > 0 {
				tools = append(tools, td[0])
			}
		}
	}

	sysPrompt := prependAgentContext(agent.OrchestratorPrompt, agent, facts, agentOperatingNotes(udb, agent))
	sysPrompt = StripPromptSectionsForTools(sysPrompt, nil)

	return func(ctx context.Context, c EvalCase) EvalResult {
		return T.runOneEvalCase(ctx, agent, sysPrompt, tools, c, stub, allowConsequential)
	}
}

// agentFingerprint hashes what makes an agent BEHAVE as it does: its prompt,
// its tool surface and its tier.
//
// Not its name, description or timestamps. A rename that read as a change
// would put every run under a fresh hash and the score history would compare
// nothing to nothing.
func agentFingerprint(agent AgentRecord) string {
	tools := append([]string{}, agent.AllowedTools...)
	sort.Strings(tools) // reordering an allowlist is not a behaviour change
	tier := "worker"
	if agent.LeadModel {
		tier = "lead"
	}
	return EvalTargetFingerprint(agent.OrchestratorPrompt, strings.Join(tools, ","), tier)
}

// aggregateEvalRuns collapses the N runs of one case into a single row: the pass
// RATE (Passes/Runs), a representative sample (the first FAILING run if any, else
// the last), the union of tools called across runs, and any run error. Passed is
// strict (all runs passed); the rate is what you actually read.
func aggregateEvalRuns(name string, runs []EvalResult) EvalResult {
	agg := EvalResult{Name: name, Runs: len(runs)}
	if len(runs) == 0 {
		agg.ErrText = "no runs completed (cancelled)"
		return agg
	}
	toolSet := map[string]bool{}
	sampleIdx := -1
	for i := range runs {
		r := runs[i]
		if r.ErrText != "" {
			agg.ErrText = r.ErrText
		}
		if r.Passed && r.ErrText == "" {
			agg.Passes++
		} else if sampleIdx < 0 {
			sampleIdx = i // first failing run — the informative sample
		}
		for _, t := range r.ToolsCalled {
			toolSet[t] = true
		}
	}
	if sampleIdx < 0 {
		sampleIdx = len(runs) - 1 // all passed → show the last run
	}
	for t := range toolSet {
		agg.ToolsCalled = append(agg.ToolsCalled, t)
	}
	sort.Strings(agg.ToolsCalled)
	agg.Passed = agg.Passes == agg.Runs
	agg.Output = runs[sampleIdx].Output
	agg.Reasons = append([]string{fmt.Sprintf("passed %d/%d runs", agg.Passes, agg.Runs)}, runs[sampleIdx].Reasons...)
	return agg
}

func (T *OrchestrateApp) runOneEvalCase(ctx context.Context, agent AgentRecord, sysPrompt string, tools []AgentToolDef, c EvalCase, stub, allowConsequential bool) EvalResult {
	res := EvalResult{Name: c.Name}
	caseCtx, cancel := context.WithTimeout(ctx, evalsRunTimeout)
	defer cancel()
	// Eval stub mode: swap tool handlers for side-effect-free stubs so a tool-use
	// case doesn't queue real messages or create real monitors. Schema unchanged,
	// so the model's tool choices are unaffected.
	if stub {
		tools = stubTools(tools, c.StubResults)
	}
	// Consequential tools (NeedsConfirm) normally require human approval. In a
	// LIVE eval we refuse to auto-approve them unless the caller explicitly opted
	// in (?live=all), so a live run can exercise real read/search tools without
	// firing messages, spend, or state changes by accident. (In stub mode the
	// handlers are inert, so this never gates anything.)
	needsConfirm := map[string]bool{}
	for _, td := range tools {
		if td.NeedsConfirm {
			needsConfirm[td.Tool.Name] = true
		}
	}
	f := false
	// Capture which tools the model actually CALLED (not just narrated), so a
	// case can grade on tool-use — the "is the model effective at using our
	// tools" question. OnStep fires per round with that round's tool calls.
	var calledMu sync.Mutex
	called := map[string]bool{}
	// Evals measure the agent as it actually behaves, and guardrails are part of
	// that behavior — an eval that runs unguarded grades a configuration nobody
	// ships. A case written to probe a rule now shows whether the rule holds,
	// instead of always passing straight through the warden. The turn exists only
	// to carry the hook's inputs (app, agent, ctx); there is no live client, so
	// its diagnostics land in the log rather than an SSE stream.
	evalTurn := &chatTurn{
		app:   T,
		agent: agent,
		user:  agent.Owner,
		udb:   UserDB(T.DB, agent.Owner),
		ctx:   caseCtx,
	}
	evalMsgs, gDecline := evalTurn.applyInputGuardrail([]Message{{Role: "user", Content: c.Prompt}})
	resp, _, err := T.RunAgentLoop(caseCtx, evalMsgs, AgentLoopConfig{
		// A terminal-rule pre_input block refused this request outright: the loop
		// delivers this text and never calls a model. Empty on every other turn.
		PreEmptedReply:      gDecline,
		SystemPrompt:        sysPrompt,
		Tools:               tools,
		MaxRounds:           resolveMaxWorkerRounds(agent),
		ThinkBudget:         agent.ThinkBudget, // per-agent override; 0 = inherit route/global
		GuardrailCheck:      evalTurn.guardrailEnforcer().Check,
		GuardrailActionGate: evalTurn.guardrailEnforcer().ActionGate,
		GuardrailHalted:     evalTurn.guardrailEnforcer().Halted,
		GuardrailReject:     evalTurn.guardrailEnforcer().Reject,
		GuardrailDeclines:   agent.GuardrailDeclines,
		Confirm: func(name, args string) bool {
			// Stub mode: handlers are side-effect-free, so approving is safe and
			// lets the scripted stub result reach the model. Live mode: approve
			// non-consequential tools; consequential ones need ?live=all.
			if stub || allowConsequential {
				return true
			}
			return !needsConfirm[name]
		},
		OnStep: func(step StepInfo) {
			calledMu.Lock()
			for _, tc := range step.ToolCalls {
				called[tc.Name] = true
			}
			calledMu.Unlock()
		},
		ChatOptions: []ChatOption{
			WithRouteKey("app.orchestrate.worker"),
			WithThink(f),
		},
	})
	if err != nil {
		res.ErrText = err.Error()
		return res
	}
	if resp == nil {
		res.ErrText = "no response from agent"
		return res
	}
	output := strings.TrimSpace(resp.Content)
	res.Output = truncateForEval(output, 1200)

	// Substring grading (cheap, runs first).
	lower := strings.ToLower(output)
	allPass := true
	for _, want := range c.MustInclude {
		want = strings.TrimSpace(want)
		if want == "" {
			continue
		}
		if !strings.Contains(lower, strings.ToLower(want)) {
			res.Reasons = append(res.Reasons, fmt.Sprintf("missing required substring: %q", want))
			allPass = false
		}
	}
	for _, bad := range c.MustNotInclude {
		bad = strings.TrimSpace(bad)
		if bad == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(bad)) {
			res.Reasons = append(res.Reasons, fmt.Sprintf("found forbidden substring: %q", bad))
			allPass = false
		}
	}

	// Tool-use grading — did the model actually CALL the tools the scenario
	// expects (or avoid the ones it shouldn't)? This is the part that catches a
	// model that describes an action in prose but never emits the tool call.
	for name := range called {
		res.ToolsCalled = append(res.ToolsCalled, name)
	}
	sort.Strings(res.ToolsCalled)
	for _, want := range c.MustCallTools {
		if want = strings.TrimSpace(want); want != "" && !called[want] {
			res.Reasons = append(res.Reasons, fmt.Sprintf("did NOT call required tool: %q (called: %v)", want, res.ToolsCalled))
			allPass = false
		}
	}
	for _, bad := range c.MustNotCallTools {
		if bad = strings.TrimSpace(bad); bad != "" && called[bad] {
			res.Reasons = append(res.Reasons, fmt.Sprintf("called forbidden tool: %q", bad))
			allPass = false
		}
	}

	// Optional LLM-as-judge pass for harder criteria.
	if jp := strings.TrimSpace(c.JudgePrompt); jp != "" {
		judged, judgeErr := T.judgeEvalOutput(caseCtx, c.Prompt, output, jp)
		if judgeErr != nil {
			res.Reasons = append(res.Reasons, fmt.Sprintf("judge error: %v", judgeErr))
			allPass = false
		} else if !judged.Pass {
			res.Reasons = append(res.Reasons, "judge FAIL: "+judged.Reason)
			allPass = false
		} else {
			res.Reasons = append(res.Reasons, "judge ok: "+judged.Reason)
		}
	}

	res.Passed = allPass
	return res
}

type judgeVerdict struct {
	Pass   bool
	Reason string
}

// judgeEvalOutput asks the worker LLM to judge whether `output`
// satisfies `criterion` for the original `prompt`. Returns the
// verdict + a short reason. Best-effort — the model is instructed
// to reply with `PASS: <reason>` or `FAIL: <reason>`; anything else
// is treated as FAIL with the raw reply as the reason.
func (T *OrchestrateApp) judgeEvalOutput(ctx context.Context, prompt, output, criterion string) (judgeVerdict, error) {
	if T.LLM == nil {
		return judgeVerdict{}, fmt.Errorf("no worker LLM configured")
	}
	sys := "You are a strict evaluator. Judge whether the agent's reply meets the stated criterion for the user's prompt. Reply with the literal token PASS or FAIL, then a colon, then a one-sentence reason. Examples: \"PASS: cites both sources requested.\" or \"FAIL: doesn't address the timing question.\""
	body := fmt.Sprintf(
		"User prompt:\n%s\n\nAgent reply:\n%s\n\nCriterion:\n%s\n\nVerdict (PASS or FAIL with a one-sentence reason):",
		prompt, output, criterion,
	)
	resp, err := T.WorkerChat(ctx, []Message{{Role: "user", Content: body}},
		WithSystemPrompt(sys),
		WithMaxTokens(120),
		WithRouteKey("app.orchestrate.worker"),
		WithThink(false),
	)
	if err != nil {
		return judgeVerdict{}, err
	}
	raw := strings.TrimSpace(resp.Content)
	upper := strings.ToUpper(raw)
	if strings.HasPrefix(upper, "PASS") {
		reason := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(upper, "PASS:"), "PASS"))
		// Recover original casing of the reason from raw.
		if idx := strings.Index(raw, ":"); idx >= 0 && idx+1 < len(raw) {
			reason = strings.TrimSpace(raw[idx+1:])
		}
		return judgeVerdict{Pass: true, Reason: reason}, nil
	}
	if strings.HasPrefix(upper, "FAIL") {
		reason := ""
		if idx := strings.Index(raw, ":"); idx >= 0 && idx+1 < len(raw) {
			reason = strings.TrimSpace(raw[idx+1:])
		}
		return judgeVerdict{Pass: false, Reason: reason}, nil
	}
	return judgeVerdict{Pass: false, Reason: "unparseable judge reply: " + raw}, nil
}

func truncateForEval(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n… [truncated]"
}

// HTTP handler — POST /api/agents/{id}/eval. Resolves the agent in
// the caller's per-user store, runs every case, returns the result
// array as JSON. Admin-gated via the standard Routes() wrapper.
func (T *OrchestrateApp) handleAgentEval(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.URL.Path)
	id = strings.TrimPrefix(id, "/api/agents/")
	id = strings.TrimSuffix(id, "/eval")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	agent, ok := loadAgent(udb, id)
	if !ok || (agent.Owner != user && agent.Owner != seedOwner) {
		http.NotFound(w, r)
		return
	}
	if len(agent.Evals) == 0 {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []EvalResult{}, "message": "no eval cases configured on this agent"})
		return
	}
	// ?runs=N repeats each case N times for a pass rate (default 1, capped so a
	// stray value can't pin the GPU). Free locally, so high N is fine.
	runs := 1
	if v := strings.TrimSpace(r.URL.Query().Get("runs")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			runs = n
		}
	}
	if runs > 100 {
		runs = 100
	}
	// SAFETY: eval defaults to STUB mode — every tool is swapped for a
	// side-effect-free stub, so a run can never send a message, create a monitor,
	// spend, or hit the network by accident. Running against REAL tools is an
	// explicit opt-in, graduated so consequential actions need the strongest signal:
	//   (default)   stub    — nothing real runs; scripted stub results feed the model
	//   ?live=1     live    — real NON-consequential tools run; NeedsConfirm tools stay denied
	//   ?live=all   live+   — real tools run INCLUDING consequential (NeedsConfirm) ones
	// ?stub=0 is accepted as an alias for ?live=1 (back-compat). A bare ?stub=1
	// is now a no-op since stub is already the default.
	stub := true
	allowConsequential := false
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("live"))) {
	case "1", "true", "yes":
		stub = false
	case "all", "consequential":
		stub = false
		allowConsequential = true
	}
	if v := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("stub"))); v == "0" || v == "false" {
		stub = false
	}
	results := T.RunAgentEvals(r.Context(), udb, user, agent, runs, stub, allowConsequential)
	pass, fail := 0, 0
	for _, r := range results {
		if r.Passed && r.ErrText == "" {
			pass++
		} else {
			fail++
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"agent_id":           agent.ID,
		"agent":              agent.Name,
		"runs":               runs,
		"stub":               stub,
		"live":               !stub,              // real tools ran
		"live_consequential": allowConsequential, // NeedsConfirm tools were allowed to fire
		"pass":               pass,               // cases that passed ALL runs
		"fail":               fail,
		"total":              len(results),
		"results":            results, // each row carries passes/runs for the rate
	})
}
