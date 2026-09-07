package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// tierStubLLM records which tier served each round and replies with a scripted
// tool call so the loop keeps going. reply is invoked per call with the 1-based
// call index; returning nil ends the turn with a plain answer.
type tierStubLLM struct {
	name  string
	mu    sync.Mutex
	calls int
	log   *[]string
	reply func(n int) []ToolCall
	// content overrides the default "done" final text (loop-boundary tests).
	content string
	// onOpts, when set, receives each call's ChatOptions (sampling assertions).
	onOpts func([]ChatOption)
}

func (s *tierStubLLM) Chat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	*s.log = append(*s.log, s.name)
	s.mu.Unlock()

	if s.onOpts != nil {
		s.onOpts(opts)
	}
	resp := &Response{InputTokens: 100_000, OutputTokens: 200}
	if tcs := s.reply(n); len(tcs) > 0 {
		resp.ToolCalls = tcs
		return resp, nil
	}
	resp.Content = "done"
	if s.content != "" {
		resp.Content = s.content
	}
	return resp, nil
}

func (s *tierStubLLM) ChatStream(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	return s.Chat(ctx, messages, opts...)
}

// withTierStubs wires a distinct worker + lead pair and a route stage that
// defaults to lead, returning the per-round tier log and a restore func.
func withTierStubs(t *testing.T, routeKey string, reply func(n int) []ToolCall) (*AppCore, *[]string) {
	t.Helper()
	log := &[]string{}
	worker := &tierStubLLM{name: "worker", log: log, reply: reply}
	lead := &tierStubLLM{name: "lead", log: log, reply: reply}

	prevWorker, prevLead := SharedWorkerLLM(), SharedLeadLLM()
	SetSharedLLMs(worker, lead) // makes LeadIsDistinct() true
	t.Cleanup(func() { SetSharedLLMs(prevWorker, prevLead) })

	RegisterRouteStage(RouteStage{Key: routeKey, Label: "test", Default: "lead"})
	return &AppCore{LLM: worker, LeadLLM: lead}, log
}

// alwaysFailTool returns a tool that fails with the SAME message every time,
// mimicking the CalDAV wall.
func alwaysFailTool(name, failure string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        name,
			Description: "test tool",
			Parameters: map[string]ToolParam{
				"when": {Type: "string", Description: "varies per call"},
			},
		},
		Handler: func(args map[string]any) (string, error) {
			return "", fmt.Errorf("%s", failure)
		},
	}
}

// TestLeadBudgetDeescalatesMidTurn confirms the spend guard: once a turn burns
// its lead budget the remaining rounds are served by the worker instead. The
// turn still runs to completion — de-escalation is a cost decision, not a stop.
func TestLeadBudgetDeescalatesMidTurn(t *testing.T) {
	prev := LeadTurnTokenBudget
	LeadTurnTokenBudget = 250_000 // ~2 rounds of the stub's 100k input
	t.Cleanup(func() { LeadTurnTokenBudget = prev })

	// Vary the args every round so the signature-keyed guards never trip —
	// isolating the budget as the only thing that can change the tier.
	app, log := withTierStubs(t, "test.budget", func(n int) []ToolCall {
		if n >= 6 {
			return nil
		}
		return []ToolCall{{ID: fmt.Sprint(n), Name: "probe", Args: map[string]any{"when": fmt.Sprintf("day-%d", n)}}}
	})

	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		Tools:     []AgentToolDef{alwaysFailTool("probe", "upstream refused the connection outright")},
		MaxRounds: 8,
		RouteKey:  "test.budget",
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}

	if len(*log) < 4 {
		t.Fatalf("expected several rounds, got %v", *log)
	}
	if (*log)[0] != "lead" {
		t.Fatalf("turn must START on lead (its route says so); got %v", *log)
	}
	if last := (*log)[len(*log)-1]; last != "worker" {
		t.Fatalf("turn must FINISH on worker once the budget is spent; got %v", *log)
	}
	// And it must not flip back to lead after de-escalating.
	seenWorker := false
	for _, tier := range *log {
		if tier == "worker" {
			seenWorker = true
		} else if seenWorker {
			t.Fatalf("must not re-escalate after de-escalating; got %v", *log)
		}
	}
}

// TestFailureShapeGuardNudgesAcrossVaryingArgs is the regression for the
// CalDAV turn: the same failure text came back from call after call with
// slightly different arguments, so no signature-keyed guard ever fired. The
// shape guard must notice and say so in the history.
func TestFailureShapeGuardNudgesAcrossVaryingArgs(t *testing.T) {
	prev := LeadTurnTokenBudget
	LeadTurnTokenBudget = 0 // disable the budget so the shape guard is isolated
	t.Cleanup(func() { LeadTurnTokenBudget = prev })

	app, _ := withTierStubs(t, "test.shape", func(n int) []ToolCall {
		if n >= 8 {
			return nil
		}
		return []ToolCall{{ID: fmt.Sprint(n), Name: "probe", Args: map[string]any{"when": fmt.Sprintf("day-%d", n)}}}
	})

	_, history, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		Tools:     []AgentToolDef{alwaysFailTool("probe", "Failed to create calendar: [exit: exit status 1]")},
		MaxRounds: 10,
		RouteKey:  "test.shape",
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}

	nudges := 0
	for _, m := range history {
		if strings.Contains(m.Content, "hit this SAME failure") {
			nudges++
		}
	}
	if nudges == 0 {
		t.Fatal("expected the failure-shape guard to inject a nudge; none found in history")
	}
	if nudges > 1 {
		t.Fatalf("the nudge must fire once per shape, got %d", nudges)
	}
}

// TestFailureShapeGuardDeescalates confirms the second stage: a wall that
// keeps coming back moves the rest of the turn onto the worker tier.
func TestFailureShapeGuardDeescalates(t *testing.T) {
	prev := LeadTurnTokenBudget
	LeadTurnTokenBudget = 0 // isolate: only the shape guard can de-escalate
	t.Cleanup(func() { LeadTurnTokenBudget = prev })

	app, log := withTierStubs(t, "test.shape2", func(n int) []ToolCall {
		if n >= 10 {
			return nil
		}
		return []ToolCall{{ID: fmt.Sprint(n), Name: "probe", Args: map[string]any{"when": fmt.Sprintf("day-%d", n)}}}
	})

	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		Tools:     []AgentToolDef{alwaysFailTool("probe", "Failed to create calendar: [exit: exit status 1]")},
		MaxRounds: 12,
		RouteKey:  "test.shape2",
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if last := (*log)[len(*log)-1]; last != "worker" {
		t.Fatalf("a turn stuck on one wall must finish on worker; got %v", *log)
	}
}

// TestNoDeescalationOnAHealthyTurn is the false-positive guard: a turn whose
// tools succeed keeps the tier its route asked for, start to finish.
func TestNoDeescalationOnAHealthyTurn(t *testing.T) {
	prev := LeadTurnTokenBudget
	LeadTurnTokenBudget = 0
	t.Cleanup(func() { LeadTurnTokenBudget = prev })

	app, log := withTierStubs(t, "test.healthy", func(n int) []ToolCall {
		if n >= 6 {
			return nil
		}
		return []ToolCall{{ID: fmt.Sprint(n), Name: "probe", Args: map[string]any{"when": fmt.Sprintf("day-%d", n)}}}
	})

	ok := AgentToolDef{
		Tool: Tool{Name: "probe", Description: "test tool", Parameters: map[string]ToolParam{
			"when": {Type: "string", Description: "varies"},
		}},
		Handler: func(args map[string]any) (string, error) {
			return fmt.Sprintf("result for %v", args["when"]), nil
		},
	}
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		Tools:     []AgentToolDef{ok},
		MaxRounds: 8,
		RouteKey:  "test.healthy",
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	for _, tier := range *log {
		if tier != "lead" {
			t.Fatalf("a healthy turn must stay on its routed tier; got %v", *log)
		}
	}
}

// TestNormalizeFailureShapeMatchesAcrossArgs is the core of the failure-shape
// guard: the CalDAV turn hit one wall from four different call signatures
// (two tools × two date ranges) and the signature-keyed guards never tripped.
// The failure TEXT was identical every time, so the shapes must match.
func TestNormalizeFailureShapeMatchesAcrossArgs(t *testing.T) {
	a := normalizeFailureShape("Error: Failed to create calendar: [exit: exit status 1]")
	b := normalizeFailureShape("Error: Failed to create calendar: [exit: exit status 1]")
	if a == "" || a != b {
		t.Fatalf("identical failures must share a shape; got %q vs %q", a, b)
	}
	// Same wall, different surrounding whitespace/case — still one shape.
	c := normalizeFailureShape("  ERROR: Failed to create calendar:  [exit: exit status 1]\n")
	if c != a {
		t.Fatalf("case/whitespace must not split the shape; got %q vs %q", c, a)
	}
}

// TestNormalizeFailureShapeCollapsesVolatileIDs confirms one failure carrying a
// rotating id is still counted as one wall, not as N distinct failures.
func TestNormalizeFailureShapeCollapsesVolatileIDs(t *testing.T) {
	a := normalizeFailureShape("run 88421903 not found for approval 55120")
	b := normalizeFailureShape("run 99333817 not found for approval 77410")
	if a != b {
		t.Fatalf("long digit runs must collapse to one shape; got %q vs %q", a, b)
	}
}

// TestNormalizeFailureShapeKeepsDistinctFailuresApart guards the other
// direction: over-normalizing would merge unrelated walls and trip the guard
// on a turn that is actually making progress.
func TestNormalizeFailureShapeKeepsDistinctFailuresApart(t *testing.T) {
	a := normalizeFailureShape("Error: Failed to create calendar: [exit: exit status 1]")
	b := normalizeFailureShape(`ERROR: agent "Gohort" has no attached tools to run`)
	if a == b {
		t.Fatalf("distinct failures must not share a shape: %q", a)
	}
}

// TestNormalizeFailureShapeSkipsTooShort confirms a result too small to
// fingerprint is ignored rather than counted as a shape everything matches.
func TestNormalizeFailureShapeSkipsTooShort(t *testing.T) {
	for _, s := range []string{"", "   ", "nope", "error: 42"} {
		if got := normalizeFailureShape(s); got != "" {
			t.Errorf("%q should not fingerprint; got %q", s, got)
		}
	}
}

// TestNormalizeFailureShapeTruncates keeps the fingerprint bounded so a huge
// error body doesn't sit in the map (and so a varying tail can't split a wall).
func TestNormalizeFailureShapeTruncates(t *testing.T) {
	long := "upstream rejected the request: " + strings.Repeat("detail ", 200)
	got := normalizeFailureShape(long)
	if len(got) > 160 {
		t.Fatalf("shape must be capped at 160 chars, got %d", len(got))
	}
	// The varying tail beyond the cap must not split one wall into many.
	other := normalizeFailureShape(long + " trailing difference 12")
	if got != other {
		t.Fatalf("tail beyond the cap must not change the shape")
	}
}

// TestOneLineShape covers the log/directive rendering.
func TestOneLineShape(t *testing.T) {
	if got := oneLineShape("short"); got != "short" {
		t.Errorf("short shapes pass through, got %q", got)
	}
	long := strings.Repeat("x", 200)
	got := oneLineShape(long)
	if len([]rune(got)) != 121 || !strings.HasSuffix(got, "…") {
		t.Errorf("long shapes truncate with an ellipsis, got %d runes", len([]rune(got)))
	}
}

// TestLeadTurnTokenBudgetDefault pins the default cap as enabled — a zero
// value silently disables the guard, which is a mistake worth catching here.
func TestLeadTurnTokenBudgetDefault(t *testing.T) {
	if LeadTurnTokenBudget <= 0 {
		t.Fatal("lead turn budget must default to a positive cap")
	}
}

// TestNormalizeFailureShapeKeepsShortNumbers guards the digit handling: long
// runs collapse (rotating ids) but short ones are preserved verbatim — "exit
// status 1" and "exit status 2" are different failures, and the shape is
// quoted back to the model, so it must not misreport what it saw.
func TestNormalizeFailureShapeKeepsShortNumbers(t *testing.T) {
	got := normalizeFailureShape("Failed to create calendar: [exit: exit status 1]")
	if !strings.Contains(got, "exit status 1") {
		t.Fatalf("short numbers must survive verbatim; got %q", got)
	}
	other := normalizeFailureShape("Failed to create calendar: [exit: exit status 2]")
	if got == other {
		t.Fatalf("different exit codes are different failures; both normalized to %q", got)
	}
}
