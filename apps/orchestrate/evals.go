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

func (T *OrchestrateApp) RunAgentEvals(ctx context.Context, udb Database, user string, agent AgentRecord, runs int, stub bool) []EvalResult {
	if runs < 1 {
		runs = 1
	}
	results := make([]EvalResult, 0, len(agent.Evals))
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

	sysPrompt := prependAgentContext(agent.OrchestratorPrompt, agent, facts)
	sysPrompt = StripPromptSectionsForTools(sysPrompt, nil)

	for _, c := range agent.Evals {
		if err := ctx.Err(); err != nil {
			return results
		}
		// Run each case `runs` times and report the pass RATE — a single run of a
		// non-deterministic model is noise; the rate is the signal (and cheap on
		// a local GPU). Same prefix every run, so the host prompt cache stays warm.
		perRun := make([]EvalResult, 0, runs)
		for i := 0; i < runs; i++ {
			if err := ctx.Err(); err != nil {
				break
			}
			perRun = append(perRun, T.runOneEvalCase(ctx, agent, sysPrompt, tools, c, stub))
		}
		results = append(results, aggregateEvalRuns(c.Name, perRun))
	}
	return results
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

func (T *OrchestrateApp) runOneEvalCase(ctx context.Context, agent AgentRecord, sysPrompt string, tools []AgentToolDef, c EvalCase, stub bool) EvalResult {
	res := EvalResult{Name: c.Name}
	caseCtx, cancel := context.WithTimeout(ctx, evalsRunTimeout)
	defer cancel()
	// Eval stub mode: swap tool handlers for side-effect-free stubs so a tool-use
	// case doesn't queue real messages or create real monitors. Schema unchanged,
	// so the model's tool choices are unaffected.
	if stub {
		tools = stubTools(tools, c.StubResults)
	}
	f := false
	// Capture which tools the model actually CALLED (not just narrated), so a
	// case can grade on tool-use — the "is the model effective at using our
	// tools" question. OnStep fires per round with that round's tool calls.
	var calledMu sync.Mutex
	called := map[string]bool{}
	resp, _, err := T.RunAgentLoop(caseCtx, []Message{{Role: "user", Content: c.Prompt}}, AgentLoopConfig{
		SystemPrompt: sysPrompt,
		Tools:        tools,
		MaxRounds:    resolveMaxWorkerRounds(agent),
		ThinkBudget:  agent.ThinkBudget, // per-agent override; 0 = inherit route/global
		Confirm:      func(name, args string) bool { return true },
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
	// ?stub=1 → eval stub mode: tools run as side-effect-free stubs (no real
	// message queued / monitor created / network). Use for tool-USE cases so
	// they're safe to run; leave off (default) for cases that need real results.
	stub := false
	if v := strings.TrimSpace(r.URL.Query().Get("stub")); v == "1" || strings.EqualFold(v, "true") {
		stub = true
	}
	results := T.RunAgentEvals(r.Context(), udb, user, agent, runs, stub)
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
		"agent_id": agent.ID,
		"agent":    agent.Name,
		"runs":     runs,
		"stub":     stub,
		"pass":     pass, // cases that passed ALL runs
		"fail":     fail,
		"total":    len(results),
		"results":  results, // each row carries passes/runs for the rate
	})
}
