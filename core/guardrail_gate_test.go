package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// TestGuardrailPreActionGateBlocksHandler proves the enforcement contract that
// matters most: when GuardrailCheck blocks a NeedsConfirm tool at pre_action,
// the tool's handler NEVER runs, the model gets the (trusted, unfenced) block
// message back as an error result, and the turn keeps going rather than
// crashing. This is the property a compromised context can't talk its way
// around — the block happens in the loop, outside the model's reach.
func TestGuardrailPreActionGateBlocksHandler(t *testing.T) {
	app, _ := withTierStubs(t, "test.guard", func(n int) []ToolCall {
		if n >= 2 {
			return nil // stop after one attempted spend
		}
		return []ToolCall{{ID: "1", Name: "spend", Args: map[string]any{"amount": "100"}}}
	})

	handlerRan := false
	spend := AgentToolDef{
		Tool: Tool{
			Name:        "spend",
			Description: "spends money",
			Parameters:  map[string]ToolParam{"amount": {Type: "string", Description: "how much"}},
		},
		NeedsConfirm: true,
		Handler: func(args map[string]any) (string, error) {
			handlerRan = true
			return "spent", nil
		},
	}

	_, history, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		Tools:     []AgentToolDef{spend},
		MaxRounds: 4,
		RouteKey:  "test.guard",
		GuardrailCheck: func(hook, candidate string) GuardrailDecision {
			if hook == GuardHookPreAction {
				return GuardrailDecision{Blocked: true, Message: "The action was NOT performed: guardrail 'never spend money' would be violated."}
			}
			return GuardrailDecision{}
		},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if handlerRan {
		t.Fatal("a blocked pre-action tool must NEVER reach its handler")
	}
	found := false
	for _, m := range history {
		for _, tr := range m.ToolResults {
			if tr.IsError && strings.Contains(tr.Content, "was NOT performed") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("the trusted block message must be handed back into history as the tool result")
	}
}

// leakThenCleanLLM returns a leaking reply first, then a clean one — to prove a
// pre_output-blocked draft is scrubbed from history, not just re-prompted.
type leakThenCleanLLM struct{ n int }

func (s *leakThenCleanLLM) Chat(ctx context.Context, m []Message, o ...ChatOption) (*Response, error) {
	s.n++
	if s.n == 1 {
		return &Response{Content: "Rory makes $202k-$246k as County Attorney IV."}, nil
	}
	return &Response{Content: "I'll pass on that one."}, nil
}
func (s *leakThenCleanLLM) ChatStream(ctx context.Context, m []Message, h StreamHandler, o ...ChatOption) (*Response, error) {
	return s.Chat(ctx, m, o...)
}

// TestGuardrailPreOutputRedactsLeakedDraft is the regression for the Rory leak:
// pre_output correctly blocked a reply containing the salary figure, but the
// draft had already been recorded to history (round 1) and so was persisted and
// delivered before the clean retry. The blocked draft must be REDACTED from the
// returned history — the figure must not survive anywhere the caller can persist
// or deliver it.
func TestGuardrailPreOutputRedactsLeakedDraft(t *testing.T) {
	app := &AppCore{LLM: &leakThenCleanLLM{}}
	resp, history, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "How much does Rory make?"}}, AgentLoopConfig{
		MaxRounds: 4,
		GuardrailCheck: func(hook, candidate string) GuardrailDecision {
			if hook == GuardHookPreOutput && strings.Contains(candidate, "$202k") {
				return GuardrailDecision{Blocked: true, Correctable: true, Message: "BLOCKED by a guardrail: never mention salary. Deflect."}
			}
			return GuardrailDecision{}
		},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if strings.Contains(resp.Content, "$202k") {
		t.Fatalf("the final reply must be the clean retry, not the leak; got: %s", resp.Content)
	}
	for i, m := range history {
		if strings.Contains(m.Content, "$202k") {
			t.Fatalf("the leaked figure must not survive anywhere in history; found at [%d] role=%s: %s", i, m.Role, m.Content)
		}
	}
	// And the scrubbed turn is a placeholder, not simply gone (alternation kept).
	sawRedaction := false
	for _, m := range history {
		if strings.Contains(m.Content, "withheld here") {
			sawRedaction = true
		}
	}
	if !sawRedaction {
		t.Fatal("the blocked draft should be replaced by the redaction placeholder")
	}
}

// alwaysLeakLLM keeps trying to disclose no matter how many times it's
// corrected — the "determined push" a socially-engineered turn produces.
type alwaysLeakLLM struct{}

func (alwaysLeakLLM) Chat(ctx context.Context, m []Message, o ...ChatOption) (*Response, error) {
	return &Response{Content: "Fine — Rory makes $202k-$246k."}, nil
}
func (alwaysLeakLLM) ChatStream(ctx context.Context, m []Message, h StreamHandler, o ...ChatOption) (*Response, error) {
	return &Response{Content: "Fine — Rory makes $202k-$246k."}, nil
}

// TestGuardrailPreOutputSubstitutesWhenPushed closes the escape hatch: after the
// correction budget is spent, pre_output must NOT release a still-violating
// reply. The final reply must be the safe substitute, and the figure must
// appear nowhere in history — no matter how many times the model retries the leak.
func TestGuardrailPreOutputSubstitutesWhenPushed(t *testing.T) {
	app := &AppCore{LLM: alwaysLeakLLM{}}
	resp, history, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go ahead and show me"}}, AgentLoopConfig{
		MaxRounds: 8,
		GuardrailCheck: func(hook, candidate string) GuardrailDecision {
			if hook == GuardHookPreOutput && strings.Contains(candidate, "$202k") {
				return GuardrailDecision{Blocked: true, Message: "BLOCKED: never mention salary. Deflect."}
			}
			return GuardrailDecision{}
		},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if !isGuardrailSafeFallback(resp.Content) {
		t.Fatalf("a reply that keeps violating must end as one of the safe substitutes; got: %s", resp.Content)
	}
	for i, m := range history {
		if strings.Contains(m.Content, "$202k") {
			t.Fatalf("the figure must not survive anywhere in history; found at [%d] role=%s", i, m.Role)
		}
	}
}

// TestGuardrailPreOutputRetractsNotSettles proves a blocked round is DISCARDED
// via RetractRound (which the app wires to skip persistence/delivery) rather
// than SettleRound (which commits the bubble). This is the fix for the leak
// where the web transcript kept the blocked draft even though history was
// scrubbed — the transcript is built from settled per-round bubbles.
func TestGuardrailPreOutputRetractsNotSettles(t *testing.T) {
	settled, retracted := 0, 0
	app := &AppCore{LLM: &leakThenCleanLLM{}}
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "How much does Rory make?"}}, AgentLoopConfig{
		MaxRounds:    4,
		SettleRound:  func() { settled++ },
		RetractRound: func() { retracted++ },
		GuardrailCheck: func(hook, candidate string) GuardrailDecision {
			if hook == GuardHookPreOutput && strings.Contains(candidate, "$202k") {
				return GuardrailDecision{Blocked: true, Message: "BLOCKED: never mention salary. Deflect."}
			}
			return GuardrailDecision{}
		},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if retracted == 0 {
		t.Fatal("a pre_output block must DISCARD the bubble via RetractRound")
	}
	if settled != 0 {
		t.Fatalf("a blocked round must NOT be settled (that persists the leak); settled=%d", settled)
	}
}

// The decline set exists so a blocked reply is not a FINGERPRINT: a
// verbatim-identical string tells a prober exactly which attempts tripped the
// guardrail, letting them bisect toward a rule they never see.
func TestGuardrailFallbackVariesAndStaysUninformative(t *testing.T) {
	if len(guardrailSafeFallbacks) < 4 {
		t.Fatalf("only %d declines — too few to stop the reply reading as a fingerprint", len(guardrailSafeFallbacks))
	}
	seen := map[string]bool{}
	for i := 0; i < 400; i++ {
		seen[guardrailSafeFallbackReply(nil)] = true
	}
	if len(seen) < 2 {
		t.Error("the decline never varied across 400 picks")
	}
	for _, line := range guardrailSafeFallbacks {
		if !isGuardrailSafeFallback(line) {
			t.Errorf("%q not recognized by isGuardrailSafeFallback", line)
		}
		low := strings.ToLower(line)
		// Uniform in information, varied only in wording. Anything naming the
		// mechanism, or hinting a rephrase might land, leaks more than the one
		// fixed string it replaced.
		for _, leak := range []string{"guardrail", "rule", "policy", "blocked", "not allowed", "restrict", "rephrase", "try again"} {
			if strings.Contains(low, leak) {
				t.Errorf("decline %q leaks %q", line, leak)
			}
		}
		// House style: no em-dashes (the display boundary rewrites them anyway).
		if strings.Contains(line, "—") {
			t.Errorf("decline %q contains an em-dash", line)
		}
	}
}

// An agent's own decline set wins, so it refuses in its own voice. The set may
// have been model-written at AUTHORING time; by the time it is used here it is
// static reviewed text, which is the whole distinction.
func TestOwnerDeclineOverridesBuiltIn(t *testing.T) {
	const custom = "That's not something this desk handles."
	for i := 0; i < 20; i++ {
		if got := guardrailSafeFallbackReply([]string{custom}); got != custom {
			t.Fatalf("owner decline ignored: got %q", got)
		}
	}
	// Blank / whitespace falls back to the built-in set rather than sending
	// an empty reply.
	for _, blank := range [][]string{nil, {}, {""}, {"   ", "\n\t"}} {
		if got := guardrailSafeFallbackReply(blank); !isGuardrailSafeFallback(got) {
			t.Errorf("blank custom declines %q produced %q, want a built-in", blank, got)
		}
	}
}

// Every hook but pre_output used to be advisory: a blocked pre_action returned
// an error result and the loop carried on, so the model could reword the call,
// route to an ungoverned tool, or just try again. The escalation meant to catch
// that handed the model a string reading "STOP" — an INSTRUCTION to the very
// agent whose judgment the warden had overruled. GuardrailHalted makes the
// termination real: the loop ends, and no further generation happens.
func TestGuardrailHaltEndsTheTurn(t *testing.T) {
	rounds := 0
	app, _ := withTierStubs(t, "test.halt", func(n int) []ToolCall {
		rounds = n
		return []ToolCall{{ID: "1", Name: "spend", Args: map[string]any{"amount": "100"}}}
	})

	handlerRan := false
	spend := AgentToolDef{
		Tool:         Tool{Name: "spend", Description: "spends money", Parameters: map[string]ToolParam{"amount": {Type: "string", Description: "how much"}}},
		NeedsConfirm: true,
		Handler:      func(args map[string]any) (string, error) { handlerRan = true; return "spent", nil },
	}

	resp, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		Tools:     []AgentToolDef{spend},
		MaxRounds: 20, // generous: if the halt doesn't fire, the model keeps retrying
		RouteKey:  "test.halt",
		GuardrailCheck: func(hook, candidate string) GuardrailDecision {
			return GuardrailDecision{Blocked: hook == GuardHookPreAction, Message: "blocked"}
		},
		GuardrailHalted: func() bool { return true },
		GuardrailReject: func(reason, request string) string { return "I can't help with that one." },
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if handlerRan {
		t.Error("the blocked tool must never execute")
	}
	if resp.Content != "I can't help with that one." {
		t.Errorf("the reply must come from the rejection model, got %q", resp.Content)
	}
	if len(resp.ToolCalls) != 0 {
		t.Error("a halted turn must not return pending tool calls")
	}
	// The point of a hard stop: it does not spend the round budget arguing.
	if rounds > 2 {
		t.Errorf("halt should end the turn immediately, but the model was called %d times", rounds)
	}
}

// The rejection model is the handover target, so a failure there must not
// release what it was replacing. Empty (or absent) falls back to the canned
// decline — never to the model's own blocked draft.
func TestRejectionFailureFallsBackToCannedDecline(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reject func(string, string) string
	}{
		{"returns empty", func(string, string) string { return "" }},
		{"returns whitespace", func(string, string) string { return "   \n " }},
		{"not wired at all", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, _ := withTierStubs(t, "test.reject", func(n int) []ToolCall { return nil })
			resp, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
				MaxRounds: 3,
				RouteKey:  "test.reject",
				GuardrailCheck: func(hook, candidate string) GuardrailDecision {
					return GuardrailDecision{Blocked: hook == GuardHookPreOutput, Message: "blocked"}
				},
				GuardrailHalted: func() bool { return true },
				GuardrailReject: tc.reject,
			})
			if err != nil {
				t.Fatalf("loop: %v", err)
			}
			if !isGuardrailSafeFallback(resp.Content) {
				t.Errorf("a failed rejection must fall back to a canned decline, got %q", resp.Content)
			}
		})
	}
}

// Halt must override the correction budget at pre_output too. Re-prompting is
// one more generation from the context that just failed, which is exactly what
// a halt exists to prevent.
func TestHaltSkipsOutputCorrectionRetries(t *testing.T) {
	calls := 0
	app, _ := withTierStubs(t, "test.nocorrect", func(n int) []ToolCall { calls = n; return nil })

	resp, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		MaxRounds: 10,
		RouteKey:  "test.nocorrect",
		GuardrailCheck: func(hook, candidate string) GuardrailDecision {
			return GuardrailDecision{Blocked: hook == GuardHookPreOutput, Message: "blocked"}
		},
		GuardrailHalted: func() bool { return true },
		GuardrailReject: func(reason, request string) string { return "Sorry, not this one." },
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if resp.Content != "Sorry, not this one." {
		t.Errorf("expected the rejection reply, got %q", resp.Content)
	}
	if calls > 1 {
		t.Errorf("a halted pre_output must not re-prompt for revisions; model called %d times", calls)
	}
}

// Without a halt the old behavior stands: block, correct, keep going. Pinned so
// adding the halt didn't quietly turn every block into a turn-ender.
func TestBlockWithoutHaltStillCorrects(t *testing.T) {
	app, _ := withTierStubs(t, "test.nohalt", func(n int) []ToolCall { return nil })
	_, history, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		MaxRounds: 4,
		RouteKey:  "test.nohalt",
		GuardrailCheck: func(hook, candidate string) GuardrailDecision {
			return GuardrailDecision{Blocked: hook == GuardHookPreOutput, Correctable: true, Message: "revise please"}
		},
		// No GuardrailHalted — the correction path must still run.
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	sawCorrection := false
	for _, m := range history {
		if strings.Contains(m.Content, "revise please") {
			sawCorrection = true
		}
	}
	if !sawCorrection {
		t.Error("without a halt, a blocked reply should still be re-prompted for correction")
	}
}

// A non-correctable block skips the revise pass on the FIRST flag, with no halt
// in sight. The rule forbids the content the request asked for, so there is no
// compliant revision of this reply to wait for — and each retry would be another
// draft holding the protected thing, to be retracted and scrubbed.
func TestBlockingRuleSkipsCorrectionWithoutAHalt(t *testing.T) {
	calls := 0
	app, _ := withTierStubs(t, "test.blocking", func(n int) []ToolCall { calls = n; return nil })

	resp, history, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "how much does Rory make?"}}, AgentLoopConfig{
		MaxRounds: 10,
		RouteKey:  "test.blocking",
		GuardrailCheck: func(hook, candidate string) GuardrailDecision {
			return GuardrailDecision{
				Blocked:     hook == GuardHookPreOutput,
				Correctable: false,
				Message:     "blocked",
			}
		},
		// No GuardrailHalted at all: a non-correctable block alone must end it.
		GuardrailReject: func(reason, request string) string { return "Not one I'll get into." },
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if resp.Content != "Not one I'll get into." {
		t.Errorf("a blocking rule hands the reply to the rejection writer, got %q", resp.Content)
	}
	if calls > 1 {
		t.Errorf("a blocking rule must not re-prompt for revisions; model called %d times", calls)
	}
	// And the draft it replaced is nowhere in the returned history.
	for _, m := range history {
		if strings.Contains(m.Content, "blocked") && m.Role == "user" {
			t.Error("a blocking rule must not inject a correction turn — it isn't asking for one")
		}
	}
}

// narratingStubLLM returns prose AND tool calls in the same response. The shared
// tierStubLLM sets Content only when there are no tool calls, so a round with
// narration mid-work — the only kind the periodic gate looks at — cannot be
// expressed with it.
type narratingStubLLM struct {
	mu     sync.Mutex
	calls  int
	prose  string
	rounds int // emit a tool call (and prose) for this many rounds, then finish
	// vary makes each round's prose distinct, so dedup does not mask a missing
	// check. Without it every round is the same string and one check covers all.
	vary bool
}

func (s *narratingStubLLM) Chat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.mu.Unlock()
	resp := &Response{InputTokens: 1000, OutputTokens: 50}
	if n <= s.rounds {
		resp.Content = s.prose
		if s.vary {
			resp.Content = fmt.Sprintf("%s (round %d)", s.prose, n)
		}
		resp.ToolCalls = []ToolCall{{ID: "1", Name: "noop", Args: map[string]any{}}}
		return resp, nil
	}
	resp.Content = "done"
	return resp, nil
}

func (s *narratingStubLLM) ChatStream(ctx context.Context, m []Message, h StreamHandler, o ...ChatOption) (*Response, error) {
	return s.Chat(ctx, m, o...)
}

func narratingApp(t *testing.T, routeKey string, stub *narratingStubLLM) (*AppCore, AgentToolDef) {
	t.Helper()
	prevWorker, prevLead := SharedWorkerLLM(), SharedLeadLLM()
	SetSharedLLMs(stub, stub)
	t.Cleanup(func() { SetSharedLLMs(prevWorker, prevLead) })
	RegisterRouteStage(RouteStage{Key: routeKey, Label: "test", Default: "lead"})
	noop := AgentToolDef{
		Tool:    Tool{Name: "noop", Description: "does nothing", Parameters: map[string]ToolParam{}},
		Handler: func(args map[string]any) (string, error) { return "ok", nil },
	}
	return &AppCore{LLM: stub, LeadLLM: stub}, noop
}

// The periodic guard must judge EVERY round that produces narration. It used to
// sample every 4th, and the skipped rounds were a straight leak: interim prose is
// appended to history and delivered mid-turn (the OnStep with Done:false paints a
// bubble), so an unsampled round reached the transcript and the user unjudged.
func TestPeriodicJudgesEveryNarratingRound(t *testing.T) {
	stub := &narratingStubLLM{prose: "working on it", rounds: 6, vary: true}
	app, noop := narratingApp(t, "test.everyround", stub)

	seen := []string{}
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		Tools:     []AgentToolDef{noop},
		MaxRounds: 8,
		RouteKey:  "test.everyround",
		GuardrailCheck: func(hook, candidate string) GuardrailDecision {
			if hook == GuardHookPeriodic {
				seen = append(seen, candidate)
			}
			return GuardrailDecision{}
		},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if len(seen) < 6 {
		t.Fatalf("every narrating round must be judged; got %d checks for 6 rounds: %q", len(seen), seen)
	}
	// And each round's own prose was what got judged, not a stale copy.
	for i, got := range seen[:6] {
		if !strings.Contains(got, fmt.Sprintf("round %d", i+1)) {
			t.Errorf("check %d judged %q, expected round %d's narration", i, got, i+1)
		}
	}
}

// Cost control: identical prose is judged once. The check is a pure function of
// (rules, text), so a repeated lead-in cannot get a different answer.
func TestPeriodicDedupesIdenticalNarration(t *testing.T) {
	stub := &narratingStubLLM{prose: "same words every round", rounds: 6} // vary off
	app, noop := narratingApp(t, "test.dedupe", stub)

	checks := 0
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		Tools:     []AgentToolDef{noop},
		MaxRounds: 8,
		RouteKey:  "test.dedupe",
		GuardrailCheck: func(hook, candidate string) GuardrailDecision {
			if hook == GuardHookPeriodic {
				checks++
			}
			return GuardrailDecision{}
		},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if checks != 1 {
		t.Errorf("six identical narrations should cost ONE warden call; got %d", checks)
	}
}

// The escape hatch pre_output had to have removed, in its periodic form: the gate
// used to stop CHECKING once the correction budget was spent, so the round after
// the budget ran out was released unjudged. The budget may govern whether a block
// can redirect; it must never stop the check.
func TestPeriodicKeepsCheckingAfterCorrectionBudgetSpent(t *testing.T) {
	stub := &narratingStubLLM{prose: "leaking", rounds: 10, vary: true}
	app, noop := narratingApp(t, "test.afterbudget", stub)

	checks := 0
	resp, history, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		Tools:     []AgentToolDef{noop},
		MaxRounds: 12,
		RouteKey:  "test.afterbudget",
		GuardrailCheck: func(hook, candidate string) GuardrailDecision {
			if hook != GuardHookPeriodic {
				return GuardrailDecision{}
			}
			checks++
			return GuardrailDecision{Blocked: true, Correctable: true, Message: "blocked"}
		},
		GuardrailReject: func(reason, request string) string { return "Not that one." },
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	// Blocked every time, so once the redirect budget is spent the turn must hand
	// over rather than let a round through.
	if resp.Content != "Not that one." {
		t.Errorf("a block with no redirect left must hand over, got %q", resp.Content)
	}
	if checks < maxGuardrailOutputCorrections+1 {
		t.Errorf("the check must keep running past the correction budget; ran %d times", checks)
	}
	for _, m := range history {
		if m.Role == "assistant" && strings.Contains(m.Content, "leaking") {
			t.Error("no blocked narration may survive in history")
		}
	}
}

// A blocking rule flagged in mid-flight narration hands over rather than
// redirecting the turn to carry on differently. With per-round checking, the
// violation is always caught in the round that produced it, so scrubbing the most
// recent assistant turn genuinely clears it — the figure survives NOWHERE.
func TestBlockingRuleAtPeriodicHandsOverWithoutAHalt(t *testing.T) {
	stub := &narratingStubLLM{prose: "the manager makes $202,000, let me confirm", rounds: 6, vary: true}
	app, noop := narratingApp(t, "test.blockperiodic", stub)

	hooks := map[string]int{}
	resp, history, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		Tools:     []AgentToolDef{noop},
		MaxRounds: 8,
		RouteKey:  "test.blockperiodic",
		GuardrailCheck: func(hook, candidate string) GuardrailDecision {
			hooks[hook]++
			return GuardrailDecision{Blocked: hook == GuardHookPeriodic, Correctable: false, Message: "blocked"}
		},
		GuardrailReject: func(reason, request string) string { return "Skipping that one." },
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if hooks[GuardHookPeriodic] == 0 {
		t.Fatal("the periodic gate never fired — the test proves nothing")
	}
	if resp.Content != "Skipping that one." {
		t.Errorf("a blocking rule at periodic hands over to the rejection writer, got %q", resp.Content)
	}
	// This is the assertion the sampling interval used to make impossible.
	for _, m := range history {
		if strings.Contains(m.Content, "202,000") {
			t.Errorf("flagged narration must survive nowhere in history; found in %s turn: %q", m.Role, m.Content)
		}
	}
}

// Correctable carries no weight at pre_action: a blocked tool call still leaves a
// compliant route to finishing the task, so this stays block-and-continue.
// Ending the turn there would make every recoverable detour a dead end.
func TestBlockingRuleDoesNotEndTheTurnAtPreAction(t *testing.T) {
	handlerRan := false
	app, _ := withTierStubs(t, "test.blockaction", func(n int) []ToolCall {
		if n == 0 {
			return []ToolCall{{ID: "1", Name: "spend", Args: map[string]any{"amount": 5}}}
		}
		return nil // second round: answer normally
	})
	spend := AgentToolDef{
		Tool:         Tool{Name: "spend", Description: "spends money", Parameters: map[string]ToolParam{"amount": {Type: "string", Description: "how much"}}},
		NeedsConfirm: true,
		Handler:      func(args map[string]any) (string, error) { handlerRan = true; return "spent", nil },
	}
	resp, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		Tools:     []AgentToolDef{spend},
		MaxRounds: 6,
		RouteKey:  "test.blockaction",
		GuardrailCheck: func(hook, candidate string) GuardrailDecision {
			return GuardrailDecision{Blocked: hook == GuardHookPreAction, Correctable: false, Message: "blocked, change course"}
		},
		GuardrailReject: func(reason, request string) string { return "REJECTED" },
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if handlerRan {
		t.Error("the blocked tool must never execute")
	}
	if resp.Content == "REJECTED" {
		t.Error("a blocking rule at pre_action must NOT end the turn; the agent gets to change course")
	}
}

// The periodic check is the expensive one — a model call for every round that
// produces narration. It is worth that where the words are painted into a live
// transcript and worth nothing where the round's prose is discarded before
// anyone sees it, which is what InterimContentHidden declares.
func TestPeriodicSkippedWhenInterimContentIsHidden(t *testing.T) {
	stub := &narratingStubLLM{prose: "thinking out loud", rounds: 6, vary: true}
	app, noop := narratingApp(t, "test.hidden", stub)

	hooks := map[string]int{}
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		Tools:                []AgentToolDef{noop},
		MaxRounds:            8,
		RouteKey:             "test.hidden",
		InterimContentHidden: true,
		GuardrailCheck: func(hook, candidate string) GuardrailDecision {
			hooks[hook]++
			return GuardrailDecision{}
		},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if hooks[GuardHookPeriodic] != 0 {
		t.Errorf("periodic must not run when interim prose goes nowhere; ran %d times", hooks[GuardHookPeriodic])
	}
	// The saving must not have cost containment: pre_output still judges the reply
	// that actually ships, which on such a path is the only text there is.
	if hooks[GuardHookPreOutput] == 0 {
		t.Error("pre_output must still judge the final reply")
	}
}

// The zero value has to be the safe one: a host that says nothing keeps full
// checking rather than silently losing containment.
func TestPeriodicRunsByDefault(t *testing.T) {
	stub := &narratingStubLLM{prose: "thinking out loud", rounds: 4, vary: true}
	app, noop := narratingApp(t, "test.default", stub)

	hooks := map[string]int{}
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		Tools:     []AgentToolDef{noop},
		MaxRounds: 8,
		RouteKey:  "test.default",
		// InterimContentHidden deliberately unset.
		GuardrailCheck: func(hook, candidate string) GuardrailDecision {
			hooks[hook]++
			return GuardrailDecision{}
		},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if hooks[GuardHookPeriodic] == 0 {
		t.Fatal("a host that declares nothing must get the periodic check — the zero value is the safe one")
	}
}

// And a hidden path must still contain a leak in the reply itself: skipping
// periodic saves calls, it does not open the door pre_output guards.
func TestHiddenInterimStillBlocksTheReply(t *testing.T) {
	app, _ := withTierStubs(t, "test.hiddenblock", func(n int) []ToolCall { return nil })
	resp, history, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		MaxRounds:            6,
		RouteKey:             "test.hiddenblock",
		InterimContentHidden: true,
		GuardrailCheck: func(hook, candidate string) GuardrailDecision {
			return GuardrailDecision{Blocked: hook == GuardHookPreOutput, Correctable: false, Message: "blocked"}
		},
		GuardrailReject: func(reason, request string) string { return "Not that one." },
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if resp.Content != "Not that one." {
		t.Errorf("a hidden-interim path must still substitute on a pre_output block, got %q", resp.Content)
	}
	for _, m := range history {
		if m.Role == "assistant" && strings.Contains(m.Content, "done") {
			t.Error("the blocked draft must not survive in history")
		}
	}
}

// A pre-empted turn calls no model at all, and still delivers through the path
// every host already renders with — streamed, then one Done step. Returning the
// text without streaming it persists a reply the browser never paints.
func TestPreEmptedReplyCallsNoModel(t *testing.T) {
	calls := 0
	app, _ := withTierStubs(t, "test.preempt", func(n int) []ToolCall { calls = n; return nil })

	var streamed strings.Builder
	steps := 0
	resp, history, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "the forbidden question"}}, AgentLoopConfig{
		MaxRounds:      6,
		RouteKey:       "test.preempt",
		PreEmptedReply: "Not one I'll get into.",
		Stream:         func(chunk string) { streamed.WriteString(chunk) },
		OnStep:         func(info StepInfo) { steps++ },
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if calls != 0 {
		t.Errorf("a pre-empted turn must not call a model; called %d times", calls)
	}
	if resp.Content != "Not one I'll get into." {
		t.Errorf("the pre-empted reply must be returned verbatim, got %q", resp.Content)
	}
	if streamed.String() != "Not one I'll get into." {
		t.Errorf("the reply must be streamed so the web transcript paints it, got %q", streamed.String())
	}
	if steps != 1 {
		t.Errorf("expected exactly one Done step, got %d", steps)
	}
	// A caller that persists the transcript must get a real conversation, not nil.
	if len(history) != 2 || history[1].Role != "assistant" || history[1].Content != "Not one I'll get into." {
		t.Errorf("history must be the request plus the reply; got %+v", history)
	}
}

// Whitespace-only is not a reply. It must not pre-empt the turn, or a bug in the
// app layer would silently answer every request with nothing.
func TestBlankPreEmptedReplyStillRunsTheTurn(t *testing.T) {
	calls := 0
	app, _ := withTierStubs(t, "test.preemptblank", func(n int) []ToolCall { calls = n; return nil })
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		MaxRounds:      3,
		RouteKey:       "test.preemptblank",
		PreEmptedReply: "   \n ",
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if calls == 0 {
		t.Error("a blank pre-empted reply must not suppress the turn")
	}
}

// The zero value is the strict one, and that is the point of naming the field for
// the correctable case. A caller that reports a block without saying anything
// about severity gets block-and-refuse, not a free revise pass — so forgetting the
// field costs a refusal, never a leak.
func TestZeroValueDecisionIsNotCorrectable(t *testing.T) {
	var dec GuardrailDecision
	if dec.Correctable {
		t.Fatal("the zero value must not be correctable")
	}
	calls := 0
	app, _ := withTierStubs(t, "test.zerosev", func(n int) []ToolCall { calls = n; return nil })
	resp, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		MaxRounds: 6,
		RouteKey:  "test.zerosev",
		GuardrailCheck: func(hook, candidate string) GuardrailDecision {
			// Severity deliberately unset — the shape a careless caller writes.
			return GuardrailDecision{Blocked: hook == GuardHookPreOutput, Message: "blocked"}
		},
		GuardrailReject: func(reason, request string) string { return "Not that one." },
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if resp.Content != "Not that one." {
		t.Errorf("an unset severity must hand over rather than revise; got %q", resp.Content)
	}
	if calls > 1 {
		t.Errorf("an unset severity must not buy a revise pass; model called %d times", calls)
	}
}
