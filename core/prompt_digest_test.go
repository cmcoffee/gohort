package core

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestPromptDigestMeasuresEachPart(t *testing.T) {
	sys := strings.Repeat("s", 4000) // ~1000 tokens
	msgs := []Message{
		{Role: "user", Content: strings.Repeat("u", 400)},
		{Role: "assistant", ToolResults: []ToolResult{{Content: strings.Repeat("r", 8000)}}},
	}
	tools := []AgentToolDef{{Tool: Tool{Name: "web_search", Description: "search"}}}

	d := buildPromptDigest(sys, []string{"framework.actions"}, tools, msgs, 100000, 60000)

	if d.SystemBytes != 4000 || d.SystemTokens != 1000 {
		t.Errorf("system: got %d bytes / %d tokens", d.SystemBytes, d.SystemTokens)
	}
	if d.Messages != 2 {
		t.Errorf("messages: got %d", d.Messages)
	}
	// The tool-result body is the part every earlier instrument missed: a
	// standing thread's weight is in results, not in what anyone said.
	if d.HistoryChars != 8400 {
		t.Errorf("history chars should count tool-result bodies, got %d", d.HistoryChars)
	}
	if d.HistoryTokens < 2000 {
		t.Errorf("history tokens should count tool-result bodies, got %d", d.HistoryTokens)
	}
	if d.ToolCount != 1 || d.ToolTokens <= 0 {
		t.Errorf("tools: got %d tools / %d tokens", d.ToolCount, d.ToolTokens)
	}
	if d.Window != 100000 || d.Budget != 60000 {
		t.Errorf("window/budget not carried: %d / %d", d.Window, d.Budget)
	}
	if len(d.ClauseKeys) != 1 || d.ClauseKeys[0] != "framework.actions" {
		t.Errorf("clause keys not carried: %v", d.ClauseKeys)
	}
	if d.Estimated != d.SystemTokens+d.ToolTokens+d.HistoryTokens {
		t.Error("Estimated should be the sum of the parts")
	}
}

// The alarm the whole record exists for. The measured failure sat ON the line:
// 523,749 chars against a 131,072-token budget, intermittent because it was
// close rather than over.
func TestPromptDigestFlagsATightPrompt(t *testing.T) {
	msgs := []Message{{Role: "user", Content: strings.Repeat("x", 380000)}} // ~95k tokens
	d := buildPromptDigest("", nil, nil, msgs, 100000, 60000)
	if !d.Tight {
		t.Errorf("a prompt %d tokens into a %d window should be tight (headroom %d)", d.Estimated, d.Window, d.Headroom)
	}
	if d.Headroom != d.Window-d.Estimated {
		t.Errorf("headroom should be window minus the estimate, got %d", d.Headroom)
	}

	roomy := buildPromptDigest("", nil, nil, []Message{{Role: "user", Content: "hi"}}, 100000, 60000)
	if roomy.Tight {
		t.Error("a two-character prompt is not tight")
	}
}

// No declared window means no claim about headroom. A zero that reads as a
// measurement is how the earlier percentage-of-window budget became a no-op
// nobody noticed.
func TestPromptDigestMakesNoHeadroomClaimWithoutAWindow(t *testing.T) {
	d := buildPromptDigest("system", nil, nil, []Message{{Role: "user", Content: "hi"}}, 0, 0)
	if d.Tight || d.Headroom != 0 {
		t.Errorf("no window should mean no headroom claim, got tight=%v headroom=%d", d.Tight, d.Headroom)
	}
}

// compactHistory reports the budget it USED, so a recorder never recomputes the
// formula beside it. Two copies of one quantity is how one turn came to be
// described as 4,635 tokens and 151,251 tokens by two layers of itself.
func TestCompactHistoryReportsTheBudgetItUsed(t *testing.T) {
	msgs := []Message{{Role: "user", Content: "hi"}}
	budget := compactHistory(msgs, "system", 100000, false)
	if budget <= 0 {
		t.Fatalf("expected a positive budget, got %d", budget)
	}
	if budget >= 100000 {
		t.Errorf("budget should be under the window, got %d", budget)
	}
	if off := compactHistory(msgs, "system", 0, false); off != 0 {
		t.Errorf("no window and no force means compaction is off, got budget %d", off)
	}
}

// The loop emits ONE digest per turn, on the first round, carrying the
// provider's own input count. Once per turn and not once per round: the
// question it answers is what this turn's prompt was, and later rounds grow
// history with results the model itself asked for.
func TestLoopEmitsOneDigestPerTurn(t *testing.T) {
	app, _ := withTierStubs(t, "test.digest", func(n int) []ToolCall {
		if n >= 3 {
			return nil
		}
		return []ToolCall{{ID: "1", Name: "probe", Args: map[string]any{"when": "now"}}}
	})

	var got []PromptDigest
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		SystemPrompt:   "you are a test agent",
		Tools:          []AgentToolDef{alwaysFailTool("probe", "nope")},
		MaxRounds:      6,
		RouteKey:       "test.digest",
		ContextSize:    200000,
		OnPromptDigest: func(d PromptDigest) { got = append(got, d) },
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly one digest for the turn, got %d", len(got))
	}
	d := got[0]
	if d.InputTokens != 100_000 {
		t.Errorf("provider-reported input tokens not carried: %d", d.InputTokens)
	}
	if d.Window != 200000 {
		t.Errorf("window not carried: %d", d.Window)
	}
	if d.Budget <= 0 {
		t.Errorf("budget should come from the compaction that ran: %d", d.Budget)
	}
	if d.SystemTokens <= 0 || d.Estimated <= 0 {
		t.Errorf("system prompt not measured: %+v", d)
	}
	// The framework clauses this turn actually carried. A tool-using agent gets
	// the grounding set; the gate is "has tools", so this is the live answer
	// rather than the general one.
	if len(d.ClauseKeys) == 0 {
		t.Error("a tool-using turn should record the framework clauses it carried")
	}
	var sawActions bool
	for _, k := range d.ClauseKeys {
		if k == "framework.actions" {
			sawActions = true
		}
	}
	if !sawActions {
		t.Errorf("expected the Actions clause on a tool-using turn, got %v", d.ClauseKeys)
	}
}

// A turn with no tools carries fewer clauses, and the digest says so. This is
// the whole point of recording keys per turn rather than reading a Gate: the
// gate says "only when the agent has tools" in general, the digest says what
// happened here.
func TestDigestClauseKeysFollowTheGates(t *testing.T) {
	app, _ := withTierStubs(t, "test.digest.notools", func(n int) []ToolCall { return nil })

	var d PromptDigest
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		SystemPrompt:   "you are a test agent",
		MaxRounds:      2,
		RouteKey:       "test.digest.notools",
		OnPromptDigest: func(got PromptDigest) { d = got },
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	for _, k := range d.ClauseKeys {
		if k == "framework.actions" || k == "framework.grounding" {
			t.Errorf("tool-gated clause %q recorded on a turn with no tools: %v", k, d.ClauseKeys)
		}
	}
	// The universal ones are still there.
	var sawSecrets bool
	for _, k := range d.ClauseKeys {
		if k == "framework.secrets" {
			sawSecrets = true
		}
	}
	if !sawSecrets {
		t.Errorf("Secrets is universal and should be recorded, got %v", d.ClauseKeys)
	}
}

// A caller several frames above the loop collects the digest off the context.
// This is the seam an event-monitor wake needs: it calls a registered waker it
// cannot see inside and writes the run record itself.
func TestContextCollectorReachesACallerWithNoConfig(t *testing.T) {
	app, _ := withTierStubs(t, "test.digest.ctx", func(n int) []ToolCall { return nil })

	ctx, promptDigest := WithPromptDigest(context.Background())
	if got := promptDigest(); got.Estimated != 0 {
		t.Fatalf("reader should report the zero digest before the turn, got %+v", got)
	}

	// No OnPromptDigest anywhere: the caller never touches the loop config.
	_, _, err := app.RunAgentLoop(ctx, []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		SystemPrompt: "you are a test agent",
		MaxRounds:    2,
		RouteKey:     "test.digest.ctx",
		ContextSize:  200000,
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	got := promptDigest()
	if got.Estimated <= 0 || got.Window != 200000 {
		t.Fatalf("context collector did not receive the digest: %+v", got)
	}
	if got.InputTokens != 100_000 {
		t.Errorf("provider count not carried to the collector: %d", got.InputTokens)
	}
}

// A context with no collector is the common path — almost every turn. It must
// cost nothing and blow up on nobody.
func TestLoopWithNoCollectorIsFine(t *testing.T) {
	app, _ := withTierStubs(t, "test.digest.nocollector", func(n int) []ToolCall { return nil })
	if _, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		MaxRounds: 2,
		RouteKey:  "test.digest.nocollector",
	}); err != nil {
		t.Fatalf("loop: %v", err)
	}
	recordPromptDigest(context.Background(), PromptDigest{Window: 1}) // no collector: a no-op
}

// The first digest under one context wins: a fanout stage dispatches its
// branches concurrently, and the run is about the prompt that opened it.
func TestContextCollectorKeepsTheFirstDigest(t *testing.T) {
	ctx, promptDigest := WithPromptDigest(context.Background())
	recordPromptDigest(ctx, PromptDigest{Window: 111})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recordPromptDigest(ctx, PromptDigest{Window: 999})
		}()
	}
	wg.Wait()

	if got := promptDigest(); got.Window != 111 {
		t.Fatalf("first digest should win, got window %d", got.Window)
	}
}

// A zero-valued first digest still counts as the answer. Tracking "has one
// arrived" separately from "is it non-zero" is what stops a genuinely empty
// measurement from being overwritten by a later one.
func TestAZeroDigestStillCounts(t *testing.T) {
	ctx, promptDigest := WithPromptDigest(context.Background())
	recordPromptDigest(ctx, PromptDigest{})
	recordPromptDigest(ctx, PromptDigest{Window: 999})
	if got := promptDigest(); got.Window != 0 {
		t.Fatalf("the first digest, even empty, is the one that stands: got %d", got.Window)
	}
}

// The event-monitor wake, end to end. fireWake calls a waker registered by an
// app this package cannot see and writes the RunRecord itself — so the digest
// has to arrive through the context, from whatever the waker runs.
func TestEventMonitorWakeRecordsThePromptDigest(t *testing.T) {
	db := memDB(t)

	// A waker that runs an agent turn, the way the real one does.
	RegisterEventWaker(func(ctx context.Context, owner, name, summary string) {
		recordPromptDigest(ctx, PromptDigest{Window: 200000, Estimated: 4321, InputTokens: 100})
	})
	defer RegisterEventWaker(nil)

	FireEventMonitor(context.Background(), db, EventMonitor{
		Name: "cve-watch", Owner: "craig", Kind: EventKindPoll,
	}, "CVE-2026-1 published")

	runs := ListRuns(db, "craig", RunFilter{})
	if len(runs) != 1 {
		t.Fatalf("expected the wake to record one run, got %d", len(runs))
	}
	if got := runs[0].Prompt; got.Estimated != 4321 || got.Window != 200000 {
		t.Fatalf("wake run did not carry the prompt digest: %+v", got)
	}
}

// A waker that runs no agent turn at all (a direct notify, straight to the
// owner's phone) leaves the zero digest rather than a half-filled one.
func TestAWakeThatRunsNoTurnRecordsNoDigest(t *testing.T) {
	db := memDB(t)
	RegisterEventWaker(func(ctx context.Context, owner, name, summary string) {})
	defer RegisterEventWaker(nil)

	FireEventMonitor(context.Background(), db, EventMonitor{
		Name: "quiet", Owner: "craig", Kind: EventKindPoll,
	}, "something happened")

	runs := ListRuns(db, "craig", RunFilter{})
	if len(runs) != 1 {
		t.Fatalf("expected one run, got %d", len(runs))
	}
	if runs[0].Prompt.Window != 0 || runs[0].Prompt.Estimated != 0 {
		t.Errorf("no turn ran, so there is nothing to report: %+v", runs[0].Prompt)
	}
}
