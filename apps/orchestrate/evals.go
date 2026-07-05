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
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const evalsRunTimeout = 5 * time.Minute

// RunAgentEvals executes every case on the agent and returns a result
// row per case. Stops early only on context cancellation; individual
// case errors land on that case's ErrText without aborting the rest.
func (T *OrchestrateApp) RunAgentEvals(ctx context.Context, udb Database, user string, agent AgentRecord) []EvalResult {
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
		results = append(results, T.runOneEvalCase(ctx, agent, sysPrompt, tools, c))
	}
	return results
}

func (T *OrchestrateApp) runOneEvalCase(ctx context.Context, agent AgentRecord, sysPrompt string, tools []AgentToolDef, c EvalCase) EvalResult {
	res := EvalResult{Name: c.Name}
	caseCtx, cancel := context.WithTimeout(ctx, evalsRunTimeout)
	defer cancel()
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
	results := T.RunAgentEvals(r.Context(), udb, user, agent)
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
		"pass":     pass,
		"fail":     fail,
		"total":    len(results),
		"results":  results,
	})
}
