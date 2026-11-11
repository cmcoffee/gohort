package core

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
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
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
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
	if len([]rune(got)) != 120+len("...") || !strings.HasSuffix(got, "...") {
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

// A tool handler is the only thing that can end its own work: the loop tests
// for cancellation at a round boundary, so between a user pressing stop and a
// handler returning, nothing else is looking. These two cover the framework's
// half of that bargain — the handler is HANDED the run's context, and a call
// that was already cancelled never starts.

func TestAHandlerIsHandedTheRunsContext(t *testing.T) {
	type runKey struct{}
	ctx := context.WithValue(context.Background(), runKey{}, "this run")
	var saw any
	if _, err := safeInvoke(ctx, "probe", func(c context.Context, _ map[string]any) (string, error) {
		saw = c.Value(runKey{})
		return "", nil
	}, nil); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if saw != "this run" {
		t.Errorf("the handler got a detached context, so nothing it starts can be cancelled: %v", saw)
	}
}

// The case the round-boundary check cannot cover: a round with three calls,
// cancelled during the first, would still run the second and third to
// completion because the loop does not look again until they are all back.
func TestACancelledCallNeverStarts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := false
	out, err := safeInvoke(ctx, "probe", func(context.Context, map[string]any) (string, error) {
		ran = true
		return "scanned the whole store", nil
	}, nil)
	if ran {
		t.Error("a call cancelled before it started should not run")
	}
	if err == nil {
		t.Fatalf("want the cancellation reported, got output %q and no error", out)
	}
	if out != "" {
		t.Errorf("a call that did not run has no output: %q", out)
	}

	// A live context still runs, or the guard has turned every call into a
	// refusal — the failure that would be much worse than the one it fixes.
	if out, err := safeInvoke(context.Background(), "probe", func(context.Context, map[string]any) (string, error) {
		return "ran", nil
	}, nil); err != nil || out != "ran" {
		t.Errorf("an uncancelled call must still run: out=%q err=%v", out, err)
	}

	// And nil reads as uncancellable: safeInvoke is reached from CLI paths
	// that never had a context to give.
	if out, err := safeInvoke(nil, "probe", func(context.Context, map[string]any) (string, error) {
		return "ran", nil
	}, nil); err != nil || out != "ran" {
		t.Errorf("a nil context should run, got out=%q err=%v", out, err)
	}
}

// TestReleaseOutputTakesTheResultOutOfTheConversation is the claim the whole
// feature rests on: when the model says it is finished with a capture, the
// text leaves the conversation and the handle stays.
//
// The loop acts on the tool NAME, so the tool here is a stand-in for the real
// release_output (which lives in tools/, and cannot be imported from core).
// That is the contract being tested — a loop that never heard of that package
// still honors the call.
func TestReleaseOutputTakesTheResultOutOfTheConversation(t *testing.T) {
	var spilledID string
	big := AgentToolDef{
		Tool: Tool{Name: "run_command", Description: "runs a command"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			out := SpillOutput(strings.Repeat("a line of log output\n", 900), 1500, "read_output")
			const marker = `output_id="`
			rest := out[strings.Index(out, marker)+len(marker):]
			spilledID = rest[:strings.Index(rest, `"`)]
			return out, nil
		},
	}
	release := AgentToolDef{
		Tool: Tool{Name: ReleaseOutputToolName, Description: "lets go of a capture"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "Released.", nil
		},
	}

	app, _ := withTierStubs(t, "test.release", func(n int) []ToolCall {
		switch n {
		case 1:
			return []ToolCall{{ID: "c1", Name: "run_command", Args: map[string]any{}}}
		case 2:
			return []ToolCall{{ID: "c2", Name: ReleaseOutputToolName, Args: map[string]any{"output_id": spilledID}}}
		}
		return nil
	})

	_, history, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		SystemPrompt: "test",
		Tools:        []AgentToolDef{big, release},
		MaxRounds:    5,
		RouteKey:     "test.release",
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if spilledID == "" {
		t.Fatal("the command tool never ran")
	}

	var found bool
	for _, m := range history {
		for _, r := range m.ToolResults {
			if r.ID != "c1" {
				continue
			}
			found = true
			if strings.Contains(r.Content, "a line of log output") {
				t.Fatalf("the released result is still carrying its text (%d chars)", len(r.Content))
			}
			if !strings.Contains(r.Content, spilledID) {
				t.Fatalf("the stub does not name the handle:\n%s", r.Content)
			}
		}
	}
	if !found {
		t.Fatal("the command's result is not in the returned history")
	}

	// Reversible: the capture is still there to be read back.
	if back, err := (OutputPage{ID: spilledID, Max: 200, Tool: "read_output"}).Read(); err != nil {
		t.Fatalf("the capture did not survive the release: %v", err)
	} else if !strings.Contains(back, "a line of log output") {
		t.Fatalf("read-back lost the text:\n%s", back)
	}
}

// A probed run stops at the first call the watch picks out: that call is
// recorded and never made, the calls before it run, and the run is cancelled
// so nothing after it runs either.
func TestAProbeStopsAtTheCallItWatchesFor(t *testing.T) {
	ctx, probe, cancel := WithToolProbe(context.Background(), func(name string, args map[string]any) bool {
		return name == "machine" && args["action"] == "create"
	})
	defer cancel()
	ran := map[string]int{}
	tool := func(name string) ToolHandlerFunc {
		return func(context.Context, map[string]any) (string, error) { ran[name]++; return "ok", nil }
	}
	if out, _ := safeInvoke(ctx, "machine", tool("machine help"), map[string]any{"action": "help"}); out != "ok" {
		t.Fatalf("a call the probe passes over did not run: %q", out)
	}
	out, err := safeInvoke(ctx, "machine", tool("machine create"), map[string]any{"action": "create", "unattended": true})
	if err != nil || out != probeStoppedText || ran["machine create"] != 0 {
		t.Fatalf("the watched call: %q %v, ran %d", out, err, ran["machine create"])
	}
	if ctx.Err() == nil {
		t.Fatal("the run was not cancelled at the stop")
	}
	if _, err := safeInvoke(ctx, "tool_def", tool("tool_def"), nil); err == nil || ran["tool_def"] != 0 {
		t.Fatal("a call after the stop ran")
	}
	hit, ok := probe.Hit()
	if !ok || hit.Name != "machine" || hit.Args["unattended"] != true {
		t.Fatalf("hit = %+v %v", hit, ok)
	}
	if seen := probe.Seen(); len(seen) != 2 || seen[0].Args["action"] != "help" {
		t.Fatalf("seen = %+v", seen)
	}
	// Without a probe, nothing changes.
	if out, _ := safeInvoke(context.Background(), "machine", tool("plain"), map[string]any{"action": "create"}); out != "ok" || ran["plain"] != 1 {
		t.Fatal("a run with no probe was stopped")
	}
}

// orderProbe is a tool that records when it ran and how many of its kind ran
// at once.
type orderProbe struct {
	mu      sync.Mutex
	log     []string
	running int32
	peak    int32
}

func (p *orderProbe) tool(name string, caps []Capability, actionCaps map[string][]Capability) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{Name: name, Description: "d", Caps: caps, ActionCaps: actionCaps,
			Parameters: map[string]ToolParam{"id": {Type: "string"}, "action": {Type: "string"}}},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			n := atomic.AddInt32(&p.running, 1)
			for q := atomic.LoadInt32(&p.peak); n > q && !atomic.CompareAndSwapInt32(&p.peak, q, n); q = atomic.LoadInt32(&p.peak) {
			}
			time.Sleep(40 * time.Millisecond)
			atomic.AddInt32(&p.running, -1)
			p.mu.Lock()
			p.log = append(p.log, name+":"+args["id"].(string))
			p.mu.Unlock()
			return "ok", nil
		},
	}
}

func runRound(t *testing.T, tools []AgentToolDef, calls []ToolCall) {
	t.Helper()
	app := &AppCore{LLM: &FakeLLM{Turns: []FakeTurn{{ToolCalls: calls}, {Content: "done", Repeat: true}}}}
	if _, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}},
		AgentLoopConfig{Tools: tools, MaxRounds: 3}); err != nil {
		t.Fatalf("loop: %v", err)
	}
}

func call(name, id string, args ...string) ToolCall {
	a := map[string]any{"id": id}
	if len(args) == 2 {
		a[args[0]] = args[1]
	}
	return ToolCall{ID: name + id, Name: name, Args: a}
}

// A round that changes something runs in the order asked, so the call meant
// to see a write sees it. Observed: Builder edited an app's folder and
// published it in one round, and the publish read the folder first.
func TestARoundThatWritesRunsInOrder(t *testing.T) {
	p := &orderProbe{}
	tools := []AgentToolDef{p.tool("edit", []Capability{CapWrite}, nil), p.tool("publish", []Capability{CapRead}, nil)}
	runRound(t, tools, []ToolCall{call("edit", "1"), call("publish", "2"), call("publish", "3")})
	if got := p.log; len(got) != 3 || got[0] != "edit:1" || got[1] != "publish:2" || got[2] != "publish:3" {
		t.Errorf("a writing round must run in the order asked, got %v", got)
	}
	if p.peak != 1 {
		t.Errorf("nothing in a writing round should overlap, peak %d", p.peak)
	}
}

// A round that only reads still fans out.
func TestARoundThatOnlyReadsRunsTogether(t *testing.T) {
	p := &orderProbe{}
	tools := []AgentToolDef{p.tool("search", []Capability{CapNetwork, CapRead}, nil)}
	runRound(t, tools, []ToolCall{call("search", "1"), call("search", "2"), call("search", "3")})
	if p.peak < 2 {
		t.Errorf("reads should run together, peak %d", p.peak)
	}
}

// A tool that declares nothing has promised nothing, so it orders the round:
// the cost of guessing wrong that way is a slower round, and the cost of the
// other guess is the race.
func TestAnUnannotatedCallOrdersTheRound(t *testing.T) {
	p := &orderProbe{}
	tools := []AgentToolDef{p.tool("mystery", nil, nil), p.tool("search", []Capability{CapRead}, nil)}
	runRound(t, tools, []ToolCall{call("search", "1"), call("mystery", "2"), call("search", "3")})
	if p.peak != 1 {
		t.Errorf("an unannotated call must order the round, peak %d", p.peak)
	}
}

// One action of a grouped tool writing orders the round; its read actions on
// their own do not.
func TestAGroupedToolOrdersByTheActionCalled(t *testing.T) {
	ac := map[string][]Capability{"cat": {CapRead}, "write": {CapWrite}}
	p := &orderProbe{}
	runRound(t, []AgentToolDef{p.tool("workspace", []Capability{CapRead, CapWrite}, ac)},
		[]ToolCall{call("workspace", "1", "action", "cat"), call("workspace", "2", "action", "cat")})
	if p.peak < 2 {
		t.Errorf("two reads through a grouped tool should run together, peak %d", p.peak)
	}
	q := &orderProbe{}
	runRound(t, []AgentToolDef{q.tool("workspace", []Capability{CapRead, CapWrite}, ac)},
		[]ToolCall{call("workspace", "1", "action", "write"), call("workspace", "2", "action", "cat")})
	if q.peak != 1 || len(q.log) != 2 || q.log[0] != "workspace:1" {
		t.Errorf("a write action must order the round, peak %d log %v", q.peak, q.log)
	}
}

// A tool with its own BatchLane keeps its lanes in a writing round: it has
// already said which of its calls may overlap.
func TestOwnLanesSurviveAWritingRound(t *testing.T) {
	p := &orderProbe{}
	var n atomic.Int64
	probe := p.tool("probe", nil, nil)
	probe.BatchLane = func(map[string]any) string { return strconv.FormatInt(n.Add(1)%3, 10) }
	tools := []AgentToolDef{probe, p.tool("note", []Capability{CapWrite}, nil)}
	runRound(t, tools, []ToolCall{call("note", "1"), call("probe", "a"), call("probe", "b"), call("probe", "c")})
	if p.peak < 3 {
		t.Errorf("a tool's own lanes should still run together, peak %d", p.peak)
	}
}

// A tool the model calls by name, outside the round's catalog, is confirmed
// and judged as it would be in the catalog. The fallback used to hand the
// loop a bare handler, which carried none of that: a confirm tool loaded on
// an earlier turn and called directly ran with no confirm and no pre_action
// check.
func TestAToolCalledByNameIsConfirmedAndJudged(t *testing.T) {
	app, _ := withTierStubs(t, "test.fallbackconfirm", func(n int) []ToolCall {
		if n == 1 {
			return []ToolCall{{ID: "1", Name: "pay", Args: map[string]any{"to": "x"}}}
		}
		return nil
	})
	var mu sync.Mutex
	var asked, judged []string
	ran := false
	pay := AgentToolDef{
		Tool:         Tool{Name: "pay", Description: "pay", Parameters: map[string]ToolParam{"to": {Type: "string"}}},
		NeedsConfirm: true,
		Handler: func(context.Context, map[string]any) (string, error) {
			mu.Lock()
			ran = true
			mu.Unlock()
			return "paid", nil
		},
	}
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		RouteKey:  "test.fallbackconfirm",
		MaxRounds: 3,
		ToolFallbackResolver: func(name string) (AgentToolDef, bool) {
			return pay, name == "pay"
		},
		GuardrailCheck: func(hook, candidate string) GuardrailDecision {
			if hook == GuardHookPreAction {
				mu.Lock()
				judged = append(judged, candidate)
				mu.Unlock()
			}
			return GuardrailDecision{}
		},
		Confirm: func(name, args string) bool {
			mu.Lock()
			asked = append(asked, name)
			mu.Unlock()
			return false
		},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(judged) != 1 || !strings.HasPrefix(judged[0], "pay") {
		t.Errorf("the pre_action check judged %v, want the pay call", judged)
	}
	if len(asked) != 1 || asked[0] != "pay" {
		t.Errorf("Confirm was asked about %v, want [pay]", asked)
	}
	if ran {
		t.Error("a confirm tool called by name ran after Confirm said no")
	}
}

// A reply a check followed up on is marked corrected, with the reason, rather
// than settled bare: settled bare it read as the agent answering twice, and
// the save dropped it as a repeat of the reply after it, so the reload and the
// live screen disagreed. A host that wires no CorrectRound still settles.
func TestAFollowedUpReplyIsMarkedCorrected(t *testing.T) {
	look := AgentToolDef{
		Tool:    Tool{Name: "check_logs", Description: "reads the logs", Parameters: map[string]ToolParam{"q": {Type: "string"}}},
		Handler: func(context.Context, map[string]any) (string, error) { return "all quiet", nil },
	}
	run := func(wire bool) (corrected []string, settled int) {
		app := &AppCore{LLM: &FakeLLM{Turns: []FakeTurn{
			{Content: "Let me check the logs now."},
			{ToolCalls: []ToolCall{{ID: "1", Name: "check_logs", Args: map[string]any{"q": "errors"}}}},
			{Content: "The logs are quiet.", Repeat: true},
		}}}
		cfg := AgentLoopConfig{
			MaxRounds:   6,
			RouteKey:    "test.correctround",
			Tools:       []AgentToolDef{look},
			SettleRound: func() { settled++ },
		}
		if wire {
			cfg.CorrectRound = func(reason string) { corrected = append(corrected, reason) }
		}
		if _, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "anything in the logs?"}}, cfg); err != nil {
			t.Fatalf("loop: %v", err)
		}
		return corrected, settled
	}
	corrected, _ := run(true)
	if len(corrected) != 1 || !strings.Contains(corrected[0], "called no tool") {
		t.Fatalf("the promise was not marked corrected with its reason: %q", corrected)
	}
	if _, settled := run(false); settled == 0 {
		t.Error("a host with no CorrectRound did not get the settle it relies on")
	}
}

// The grounding correction marks the reply it follows up, too; the reply it
// asks for already carries the "Correction" label.
func TestAGroundingCorrectionMarksTheReplyCorrected(t *testing.T) {
	var corrected []string
	judged := 0
	app := &AppCore{LLM: &FakeLLM{Turns: []FakeTurn{
		{Content: "The staging server runs Ubuntu 22.04, so that package will work."},
		{Content: "You mentioned the staging server runs Ubuntu 22.04, so that package should work.", Repeat: true},
	}}}
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "will that package work?"}}, AgentLoopConfig{
		MaxRounds:       4,
		RouteKey:        "test.groundcorrect",
		CorrectRound:    func(reason string) { corrected = append(corrected, reason) },
		UncheckedClaims: []string{"the staging server runs Ubuntu 22.04"},
		TurnGroundingJudge: func(ev TurnGroundingEvidence) (TurnGroundingVerdict, bool) {
			judged++
			if judged > 1 {
				return TurnGroundingVerdict{}, true
			}
			return TurnGroundingVerdict{Asserted: true, Claim: ev.Reply, Basis: "the staging server runs Ubuntu 22.04"}, true
		},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if len(corrected) != 1 || !strings.Contains(corrected[0], "unchecked note") {
		t.Errorf("the ungrounded reply was not marked corrected: %q", corrected)
	}
}

// repeatedFetchLoop runs a model that makes the same call every round against
// a tool declaring caps, whose answer changes by a counter each time, and
// reports how many times the tool really ran.
func repeatedFetchLoop(t *testing.T, route string, caps []Capability) int32 {
	t.Helper()
	app, _ := withTierStubs(t, route, func(n int) []ToolCall {
		if n <= 10 { // per tier: the worker and the lead stubs count their own calls
			return []ToolCall{{ID: strconv.Itoa(n), Name: "read_page", Args: map[string]any{"url": "https://example.test/releases"}}}
		}
		return nil
	})
	var ran int32
	tool := AgentToolDef{
		Tool: Tool{Name: "read_page", Description: "read", Caps: caps, Parameters: map[string]ToolParam{"url": {Type: "string"}}},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			n := atomic.AddInt32(&ran, 1)
			return fmt.Sprintf("Release notes. Request id %d.", n), nil
		},
	}
	if _, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "versions"}}, AgentLoopConfig{
		Tools: []AgentToolDef{tool}, MaxRounds: 12, RouteKey: route,
	}); err != nil {
		t.Fatalf("loop: %v", err)
	}
	return ran
}

// The same web read made again and again is stopped even when the page's text
// moves by a byte each time (a request id, a clock): live, one step fetched one
// address fifteen times and the byte-identical check reset on every fetch.
func TestTheSameWebReadIsStoppedEvenWhenThePageDrifts(t *testing.T) {
	if ran := repeatedFetchLoop(t, "test.repeatweb", []Capability{CapNetwork, CapRead}); ran != repeatSameLimit+1 {
		t.Errorf("a web read repeated with drifting text must stop after %d repeats; it ran %d times", repeatSameLimit, ran)
	}
}

// A local reader whose answer changes (a job's progress) is still a reader
// with something new to say, and keeps the byte-identical rule.
func TestALocalReadWithChangingAnswersIsNotStopped(t *testing.T) {
	if ran := repeatedFetchLoop(t, "test.repeatlocal", []Capability{CapRead}); ran <= repeatSameLimit+1 {
		t.Errorf("a local read whose answer changes must not be stopped as a repeat; it ran only %d times", ran)
	}
}

// A tool that writes over the network is not a web read: each call may do
// something, so only byte-identical answers count as repeats.
func TestANetworkWriteWithChangingAnswersIsNotStopped(t *testing.T) {
	if ran := repeatedFetchLoop(t, "test.repeatwrite", []Capability{CapNetwork, CapWrite}); ran <= repeatSameLimit+1 {
		t.Errorf("a network write whose answer changes must not be stopped as a repeated read; it ran only %d times", ran)
	}
}

// consultsOnRepeatedFailure runs a turn that hits one failure from a tool with
// the given caps until the guard steps in, and counts the consults it made.
func consultsOnRepeatedFailure(t *testing.T, route, failure string, caps []Capability) int {
	t.Helper()
	prev := LeadTurnTokenBudget
	LeadTurnTokenBudget = 0
	t.Cleanup(func() { LeadTurnTokenBudget = prev })
	app, _ := withTierStubs(t, route, func(n int) []ToolCall {
		if n >= 5 {
			return nil
		}
		return []ToolCall{{ID: fmt.Sprint(n), Name: "read_page", Args: map[string]any{"when": fmt.Sprintf("page-%d", n)}}}
	})
	tool := alwaysFailTool("read_page", failure)
	tool.Tool.Caps = caps
	consults := 0
	if _, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		Tools: []AgentToolDef{tool}, MaxRounds: 8, RouteKey: route,
		Consult: func(q, ev string) (string, error) { consults++; return "Try another source.", nil },
	}); err != nil {
		t.Fatalf("loop: %v", err)
	}
	return consults
}

// Dead links are nothing for the lead to answer: every research turn spent
// three consults, about eight seconds each, being told to try another address.
// Any other repeated failure, and a 404 from a tool that does more than read
// the web, still consults.
func TestADeadLinkIsNotTakenToTheLead(t *testing.T) {
	web := []Capability{CapNetwork, CapRead}
	if n := consultsOnRepeatedFailure(t, "test.deadlink", "fetch failed: HTTP 404", web); n != 0 {
		t.Errorf("dead links from a web read were taken to the lead %d times", n)
	}
	if n := consultsOnRepeatedFailure(t, "test.badshape", "fetch failed: HTTP 400: missing field 'to'", web); n != 1 {
		t.Errorf("a repeated request-shape failure must still consult once, got %d", n)
	}
	if n := consultsOnRepeatedFailure(t, "test.apiwrite", "fetch failed: HTTP 404", []Capability{CapNetwork, CapWrite}); n != 1 {
		t.Errorf("a 404 from a tool that writes must still consult once, got %d", n)
	}
}

// Each call a round makes is reported once it has run, with whether it failed,
// so a guardrail judging the next call can be told what is already done.
func TestEachCallIsReportedForTheGuardrail(t *testing.T) {
	verify := AgentToolDef{
		Tool:    Tool{Name: "verify", Description: "checks a draft", Parameters: map[string]ToolParam{"text": {Type: "string"}}},
		Handler: func(context.Context, map[string]any) (string, error) { return "ok", nil },
	}
	flaky := AgentToolDef{
		Tool:    Tool{Name: "fetch", Description: "fetches", Parameters: map[string]ToolParam{"url": {Type: "string"}}},
		Handler: func(context.Context, map[string]any) (string, error) { return "", fmt.Errorf("timed out") },
	}
	app := &AppCore{LLM: &FakeLLM{Turns: []FakeTurn{
		{ToolCalls: []ToolCall{
			{ID: "1", Name: "verify", Args: map[string]any{"text": "draft"}},
			{ID: "2", Name: "fetch", Args: map[string]any{"url": "https://example.test"}},
		}},
		{Content: "Done.", Repeat: true},
	}}}
	var seen []string
	cfg := AgentLoopConfig{
		MaxRounds: 4,
		RouteKey:  "test.observe",
		Tools:     []AgentToolDef{verify, flaky},
		GuardrailObserve: func(call ToolCall, failed bool) {
			seen = append(seen, fmt.Sprintf("%s:%v", call.Name, failed))
		},
	}
	if _, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "check and fetch"}}, cfg); err != nil {
		t.Fatalf("loop: %v", err)
	}
	if strings.Join(seen, ",") != "verify:false,fetch:true" {
		t.Errorf("reported %v, want verify ok then fetch failed", seen)
	}
}

// A round whose tools the host calls side notes to its prose ends the turn on
// that prose: one model call, the prose as the reply, and in history once,
// after the work, not beside the calls. A host that says no, and a note from
// the user that arrived meanwhile, each get the next round as before.
func TestARoundOfSideNotesEndsOnItsProse(t *testing.T) {
	answer := strings.Repeat("Here is the whole answer to what you asked. ", 20)
	note := AgentToolDef{
		Tool:    Tool{Name: "note", Description: "d", Parameters: map[string]ToolParam{"text": {Type: "string"}}},
		Handler: func(context.Context, map[string]any) (string, error) { return "Saved.", nil },
	}
	script := func() *FakeLLM {
		return &FakeLLM{Turns: []FakeTurn{
			{Content: answer, ToolCalls: []ToolCall{{ID: "c1", Name: "note", Args: map[string]any{"text": "x"}}}},
			{Content: "Noted.", Repeat: true},
		}}
	}
	var asked []string
	finish := func(content string, calls []ToolCall, results []ToolResult) bool {
		asked = append(asked, calls[0].Name+":"+results[0].Content)
		return content == strings.TrimSpace(answer)
	}

	fake := script()
	resp, hist, err := (&AppCore{LLM: fake}).RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "q"}},
		AgentLoopConfig{Tools: []AgentToolDef{note}, MaxRounds: 4, FinishAfterTools: finish})
	if err != nil {
		t.Fatal(err)
	}
	if fake.Calls() != 1 || resp.Content != answer || len(resp.ToolCalls) != 0 {
		t.Fatalf("after %d call(s) the reply was %q (tool calls %d)", fake.Calls(), resp.Content, len(resp.ToolCalls))
	}
	if len(asked) != 1 || asked[0] != "note:Saved." {
		t.Errorf("the host was asked %v", asked)
	}
	last := hist[len(hist)-1]
	if last.Role != "assistant" || last.Content != answer || len(last.ToolCalls) != 0 {
		t.Errorf("history does not end on the reply: %+v", last)
	}
	for _, m := range hist[:len(hist)-1] {
		if strings.Contains(m.Content, "whole answer") {
			t.Errorf("the reply is in history twice, once as %s", m.Role)
		}
	}

	// The host says no: the next round answers.
	fake = script()
	resp, _, _ = (&AppCore{LLM: fake}).RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "q"}},
		AgentLoopConfig{Tools: []AgentToolDef{note}, MaxRounds: 4,
			FinishAfterTools: func(string, []ToolCall, []ToolResult) bool { return false }})
	if fake.Calls() != 2 || resp.Content != "Noted." {
		t.Errorf("a host that said no: %d call(s), %q", fake.Calls(), resp.Content)
	}

	// A note from the user arrived during the round: it is read, not dropped.
	fake = script()
	var pending []Message
	arrives := note
	arrives.Handler = func(context.Context, map[string]any) (string, error) {
		pending = []Message{{Role: "user", Content: "one more thing"}}
		return "Saved.", nil
	}
	resp, _, _ = (&AppCore{LLM: fake}).RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "q"}},
		AgentLoopConfig{Tools: []AgentToolDef{arrives}, MaxRounds: 4, FinishAfterTools: finish,
			InjectionDrain: func() []Message { out := pending; pending = nil; return out }})
	if fake.Calls() != 2 || !strings.Contains(fake.Prompt(1), "one more thing") {
		t.Errorf("a note that arrived meanwhile: %d call(s)", fake.Calls())
	}
}
