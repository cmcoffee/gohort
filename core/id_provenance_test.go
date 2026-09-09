package core

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

// The observed failure, exactly: an agent invented a post id by splicing the
// front of one real id onto the tail of another, sent it twice, got 404 both
// times, and reported a routing bug in the API. The call must not go out.
func TestAnInventedIDIsRefusedBeforeItIsSent(t *testing.T) {
	real1, real2 := "d5bd73b4-3ad0-4a39-b756-8a6f613a3f72", "debf4574-7178-4dda-90e5-30c80b5916b2"
	spliced := "d5bd73b4-7178-4dda-90e5-30c80b5916b2"
	known := collectKnownIDs("", []Message{
		{Role: "user", Content: "check the thread"},
		{Role: "assistant", ToolResults: []ToolResult{{Content: `{"posts":[{"id":"` + real1 + `"},{"id":"` + real2 + `"}]}`}}},
	})

	refusal := idProvenanceRefusal("moltbook", map[string]any{"action": "reply_to_post", "post_id": spliced}, known)
	if refusal == "" {
		t.Fatal("a spliced id must be refused")
	}
	for _, want := range []string{"was NOT called", "post_id", spliced, real1, "joined pieces of two different ids"} {
		if !strings.Contains(refusal, want) {
			t.Errorf("refusal lacks %q:\n%s", want, refusal)
		}
	}
	// The real ids pass, and so does a reference the user typed themselves.
	if r := idProvenanceRefusal("moltbook", map[string]any{"action": "reply_to_post", "post_id": real1}, known); r != "" {
		t.Errorf("an id from a tool result must pass: %s", r)
	}
	typed := collectKnownIDs("", []Message{{Role: "user", Content: "look at " + spliced}})
	if r := idProvenanceRefusal("moltbook", map[string]any{"action": "get_message", "message_id": spliced}, typed); r != "" {
		t.Errorf("an id the user supplied must pass: %s", r)
	}
}

// The model's own prose is not a source. Laundering an invented id through a
// sentence it wrote is the whole failure mode.
func TestAssistantProseIsNotASourceOfIDs(t *testing.T) {
	id := "2bd98b45-04b0-4fc4-8de4-df71f9a570e8"
	known := collectKnownIDs("", []Message{
		{Role: "user", Content: "summarize that research"},
		{Role: "assistant", Content: "I'll pull up research " + id + " now."},
	})
	if known[id] {
		t.Fatal("an id the model only said must not count as issued")
	}
	if idProvenanceRefusal("recall", map[string]any{"id": id}, known) == "" {
		t.Error("the call must still be refused")
	}
	// A system prompt legitimately carries the ids a turn is entitled to name.
	if seeded := collectKnownIDs("Appliance "+id+" is in scope.", nil); !seeded[id] {
		t.Error("ids in the system prompt are issued")
	}
}

// What the gate must never block: creating a record, non-reference arguments,
// and identifiers that are not UUIDs (a slug or a number cannot be told from a
// value the model legitimately composed).
func TestTheGateLeavesLegitimateCallsAlone(t *testing.T) {
	fresh := "11111111-2222-3333-4444-555555555555"
	empty := map[string]bool{}
	cases := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"create with a client id", "create_post", map[string]any{"id": fresh}},
		{"grouped create action", "moltbook", map[string]any{"action": "create_post", "id": fresh}},
		{"a body that quotes an id", "moltbook", map[string]any{"action": "reply_to_post", "content": "about " + fresh}},
		{"a slug reference", "docs", map[string]any{"action": "read", "doc_id": "getting-started"}},
		{"a numeric reference", "tickets", map[string]any{"action": "get", "ticket_id": "48213"}},
		{"no arguments at all", "list_posts", map[string]any{}},
	}
	for _, c := range cases {
		if r := idProvenanceRefusal(c.tool, c.args, empty); r != "" {
			t.Errorf("%s must not be refused:\n%s", c.name, r)
		}
	}
}

// End to end through the loop: the refusal reaches the model as a tool error,
// the handler never runs, and the turn is flagged in the diagnostics.
func TestTheLoopRefusesAnInventedIDWithoutCallingTheTool(t *testing.T) {
	invented := "99999999-8888-7777-6666-555555555555"
	ran := 0
	app, _ := withTierStubs(t, "test.idprov", func(n int) []ToolCall {
		if n == 1 {
			return []ToolCall{{ID: "c1", Name: "get_post", Args: map[string]any{"post_id": invented}}}
		}
		return nil
	})
	var diags []string
	_, history, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "read that post"}}, AgentLoopConfig{
		MaxRounds: 3, RouteKey: "test.idprov",
		Tools: []AgentToolDef{{
			Tool:    Tool{Name: "get_post", Description: "read a post"},
			Handler: func(context.Context, map[string]any) (string, error) { ran++; return "{}", nil },
		}},
		OnDiag: func(kind, detail string) { diags = append(diags, kind+": "+detail) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if ran != 0 {
		t.Errorf("the tool ran %d time(s); the call should never have gone out", ran)
	}
	var refusal string
	for _, m := range history {
		for _, r := range m.ToolResults {
			if strings.Contains(r.Content, "was NOT called") {
				refusal = r.Content
				if !r.IsError {
					t.Error("the refusal must reach the model as an error")
				}
			}
		}
	}
	if refusal == "" {
		t.Fatalf("no refusal in history: %+v", history)
	}
	var flagged bool
	for _, d := range diags {
		if strings.HasPrefix(d, "invented-id: ") {
			flagged = true
		}
	}
	if !flagged {
		t.Errorf("the turn must say an id was invented, got %v", diags)
	}
}

// The escape hatch, for a caller whose tools take ids from a store this loop
// cannot read.
func TestTheGateCanBeTurnedOff(t *testing.T) {
	ran := 0
	app, _ := withTierStubs(t, "test.idprov.off", func(n int) []ToolCall {
		if n == 1 {
			return []ToolCall{{ID: "c1", Name: "get_post", Args: map[string]any{"post_id": "99999999-8888-7777-6666-555555555555"}}}
		}
		return nil
	})
	if _, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "read it"}}, AgentLoopConfig{
		MaxRounds: 3, RouteKey: "test.idprov.off", DisableIDProvenanceGate: true,
		Tools: []AgentToolDef{{
			Tool:    Tool{Name: "get_post"},
			Handler: func(context.Context, map[string]any) (string, error) { ran++; return "{}", nil },
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if ran != 1 {
		t.Errorf("with the gate off the call must run, ran=%d", ran)
	}
}

// --- failure memory ---------------------------------------------------------

// withRootDB points the package's root store at a fresh in-memory one for the
// duration of a test.
func withRootDB(t *testing.T) {
	t.Helper()
	prev := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prev })
}

// A scheduled fire rebuilds its history from stored messages, which carry no
// tool results, so the repeat guard used to start every cycle knowing nothing:
// the same broken call ran again every hour and was re-diagnosed every hour.
// The count now carries between loops under the caller's key.
func TestFailureMemoryCarriesBetweenLoops(t *testing.T) {
	withRootDB(t)
	key := "sched:agent-1:session-1"
	sig := "reply_to_comment\x00{parent_id:...}"

	saveFailureMemory(key, map[string]int{sig: 2})
	carried := map[string]int{}
	loadFailureMemory(key, carried, 0)
	if carried[sig] != 2 {
		t.Fatalf("the count must carry, got %d", carried[sig])
	}
	// One more failure next cycle crosses the limit, where a fresh loop would
	// have been on its first.
	carried[sig]++
	saveFailureMemory(key, carried)
	again := map[string]int{}
	loadFailureMemory(key, again, 0)
	if again[sig] != 3 {
		t.Errorf("the count must accumulate across loops, got %d", again[sig])
	}

	// A different thread's failures are not this thread's.
	other := map[string]int{}
	loadFailureMemory("sched:agent-1:session-2", other, 0)
	if len(other) != 0 {
		t.Errorf("memory must be scoped to its key, got %v", other)
	}
	// No key means no memory: an ordinary conversation re-arms from history.
	loose := map[string]int{}
	saveFailureMemory("", map[string]int{sig: 3})
	loadFailureMemory("", loose, 0)
	if len(loose) != 0 {
		t.Errorf("an empty key must store and read nothing, got %v", loose)
	}
}

// Success is what stops a failure being remembered, and a fault nobody has
// hit in days ages out on its own.
func TestFailureMemoryForgetsWhatWasFixedOrWentStale(t *testing.T) {
	withRootDB(t)
	key, sig := "sched:a:s", "broken_call\x00{}"

	saveFailureMemory(key, map[string]int{sig: 3})
	// The guard clears a signature on success; saving that state forgets it.
	saveFailureMemory(key, map[string]int{sig: 0})
	got := map[string]int{}
	loadFailureMemory(key, got, 0)
	if len(got) != 0 {
		t.Errorf("a call that succeeded must not stay remembered, got %v", got)
	}

	// Stale: written long ago, never renewed.
	stale := failureMemory{
		Counts: map[string]int{sig: 3},
		Seen:   map[string]time.Time{sig: time.Now().Add(-failureMemoryTTL - time.Hour)},
	}
	RootDB.Set(failureMemoryTable, key, &stale)
	aged := map[string]int{}
	loadFailureMemory(key, aged, 0)
	if len(aged) != 0 {
		t.Errorf("a failure older than the TTL must be forgotten, got %v", aged)
	}

	// And a retry cannot renew its own lease: the first-seen stamp is kept.
	fresh := failureMemory{
		Counts: map[string]int{sig: 3},
		Seen:   map[string]time.Time{sig: time.Now().Add(-failureMemoryTTL + 2*time.Hour)},
	}
	RootDB.Set(failureMemoryTable, key, &fresh)
	carried := map[string]int{}
	loadFailureMemory(key, carried, 0)
	saveFailureMemory(key, carried)
	var after failureMemory
	RootDB.Get(failureMemoryTable, key, &after)
	if !after.Seen[sig].Equal(fresh.Seen[sig]) {
		t.Errorf("re-saving a carried failure must keep its original stamp: %v vs %v", after.Seen[sig], fresh.Seen[sig])
	}
}

// --- action quotas ----------------------------------------------------------

// The cap is kept by the framework, not by the model. An agent told "six a
// day" counted its own posts, mistook the UTC day boundary, and posted nine.
func TestAnActionQuotaIsEnforcedNotRequested(t *testing.T) {
	withRootDB(t)
	posts := 0
	app, _ := withTierStubs(t, "test.quota", func(n int) []ToolCall {
		if n <= 4 {
			return []ToolCall{{ID: fmt.Sprintf("c%d", n), Name: "moltbook",
				Args: map[string]any{"action": "create_post", "title": "again"}}}
		}
		return nil
	})
	var diags []string
	_, history, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "post"}}, AgentLoopConfig{
		MaxRounds: 8, RouteKey: "test.quota",
		ActionQuotas: map[string]int{"moltbook/create_post": 2}, BudgetKey: "agent-1",
		Tools: []AgentToolDef{{
			Tool:    Tool{Name: "moltbook"},
			Handler: func(context.Context, map[string]any) (string, error) { posts++; return `{"id":"p"}`, nil },
		}},
		OnDiag: func(kind, detail string) { diags = append(diags, kind) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if posts != 2 {
		t.Errorf("the allowance is 2; the tool ran %d time(s)", posts)
	}
	var refusal string
	for _, m := range history {
		for _, r := range m.ToolResults {
			if strings.Contains(r.Content, "allowance") {
				refusal = r.Content
			}
		}
	}
	for _, want := range []string{"was NOT called", "moltbook/create_post", "allowance of 2", "enforced by the framework"} {
		if !strings.Contains(refusal, want) {
			t.Errorf("refusal lacks %q:\n%s", want, refusal)
		}
	}
	var flagged bool
	for _, d := range diags {
		if d == "action-quota" {
			flagged = true
		}
	}
	if !flagged {
		t.Errorf("the turn must record that a quota bit, got %v", diags)
	}
}

// A failed call costs nothing: an outage must not spend the day's budget.
func TestAFailedCallDoesNotSpendTheAllowance(t *testing.T) {
	withRootDB(t)
	cfg := AgentLoopConfig{ActionQuotas: map[string]int{"post": 1}, BudgetKey: "a"}
	if r, _ := actionQuotaRefusal(cfg, "post", nil); r != "" {
		t.Fatalf("nothing has run yet: %s", r)
	}
	// A failure never calls chargeActionQuota, so the allowance is untouched.
	if r, _ := actionQuotaRefusal(cfg, "post", nil); r != "" {
		t.Errorf("a failed call must not be charged: %s", r)
	}
	chargeActionQuota(cfg, "post", nil)
	r, action := actionQuotaRefusal(cfg, "post", nil)
	if r == "" || action != "post" {
		t.Errorf("one success spends the only slot: %q / %q", r, action)
	}
}

// What the quota covers and what it leaves alone: a bare tool name caps every
// action under it, an unlisted action is uncapped, and quotas without a key
// are ignored rather than shared between agents.
func TestActionQuotaScoping(t *testing.T) {
	withRootDB(t)
	keyed := AgentLoopConfig{ActionQuotas: map[string]int{"moltbook": 1, "moltbook/get_feed": 5}, BudgetKey: "a"}

	if name, limit := actionQuotaLimit(keyed, "moltbook", map[string]any{"action": "get_feed"}); name != "moltbook/get_feed" || limit != 5 {
		t.Errorf("the exact action wins over the tool: %q/%d", name, limit)
	}
	if name, limit := actionQuotaLimit(keyed, "moltbook", map[string]any{"action": "create_post"}); name != "moltbook" || limit != 1 {
		t.Errorf("an action with no entry falls back to the tool: %q/%d", name, limit)
	}
	if _, limit := actionQuotaLimit(keyed, "other_tool", nil); limit != 0 {
		t.Error("an unlisted tool is uncapped")
	}
	unkeyed := AgentLoopConfig{ActionQuotas: map[string]int{"moltbook": 1}}
	if _, limit := actionQuotaLimit(unkeyed, "moltbook", nil); limit != 0 {
		t.Error("a quota with no key must be ignored, never shared")
	}
	// Two agents do not share one budget.
	a := AgentLoopConfig{ActionQuotas: map[string]int{"post": 1}, BudgetKey: "agent-a"}
	b := AgentLoopConfig{ActionQuotas: map[string]int{"post": 1}, BudgetKey: "agent-b"}
	chargeActionQuota(a, "post", nil)
	if r, _ := actionQuotaRefusal(a, "post", nil); r == "" {
		t.Error("agent a spent its slot")
	}
	if r, _ := actionQuotaRefusal(b, "post", nil); r != "" {
		t.Errorf("agent b has its own: %s", r)
	}
}

// A run that has aged out of the window frees its slot.
func TestActionQuotaWindowRolls(t *testing.T) {
	withRootDB(t)
	cfg := AgentLoopConfig{ActionQuotas: map[string]int{"post": 1}, BudgetKey: "a"}
	old := []time.Time{time.Now().Add(-actionQuotaWindow - time.Minute)}
	RootDB.Set(actionQuotaTable, "a|post", &old)
	if r, _ := actionQuotaRefusal(cfg, "post", nil); r != "" {
		t.Errorf("a run from more than 24h ago must not count: %s", r)
	}
	recent := []time.Time{time.Now().Add(-2 * time.Hour)}
	RootDB.Set(actionQuotaTable, "a|post", &recent)
	r, _ := actionQuotaRefusal(cfg, "post", nil)
	if !strings.Contains(r, "in about 22 hours") {
		t.Errorf("the refusal must say when the allowance frees up:\n%s", r)
	}
}

// --- daily spend ceiling ----------------------------------------------------

// withRates configures cost rates so a dollar ceiling has dollars to count.
func withRates(t *testing.T) {
	t.Helper()
	prev := GetCostRates()
	SetCostRates(CostRates{LeadInputPer1K: 1.0, LeadOutputPer1K: 1.0, WorkerInputPer1K: 0.001, WorkerOutputPer1K: 0.001})
	t.Cleanup(func() { SetCostRates(prev) })
}

// A turn that starts over the line is refused, and says so in the agent's own
// voice rather than failing.
func TestATurnOverTheDailyCapIsRefused(t *testing.T) {
	withRootDB(t)
	withRates(t)
	key := "agent-spendy"
	spent := []spendEntry{{At: time.Now().Add(-time.Hour), USD: 5.00}}
	RootDB.Set(spendLedgerTable, key, &spent)

	calls := 0
	app, _ := withTierStubs(t, "test.spend", func(n int) []ToolCall { calls++; return nil })
	var diags []string
	resp, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "hi"}}, AgentLoopConfig{
		MaxRounds: 3, RouteKey: "test.spend", BudgetKey: key, DailySpendUSD: 2.00,
		OnDiag: func(kind, detail string) { diags = append(diags, kind) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("no model call may be made over the cap, got %d", calls)
	}
	if !strings.Contains(resp.Content, "spending limit") || !strings.Contains(resp.Content, "$2.00") {
		t.Errorf("the refusal must say what happened: %q", resp.Content)
	}
	var flagged bool
	for _, d := range diags {
		if d == "spend-cap" {
			flagged = true
		}
	}
	if !flagged {
		t.Errorf("the turn must record the cap, got %v", diags)
	}
}

// Under the cap nothing changes, and what a round costs is charged against
// the tier that actually served it.
func TestSpendIsChargedAndTheCapLetsWorkThrough(t *testing.T) {
	withRootDB(t)
	withRates(t)
	cfg := AgentLoopConfig{BudgetKey: "a", DailySpendUSD: 1.00}

	if over, _ := overDailySpend(cfg); over {
		t.Fatal("a fresh budget is not spent")
	}
	// A lead round at a dollar per 1k tokens: 400 in + 100 out = $0.50.
	total, crossed := chargeDailySpend(cfg, &Response{Tier: LEAD, InputTokens: 400, OutputTokens: 100})
	if total < 0.49 || total > 0.51 || crossed {
		t.Errorf("half the budget spent, not crossed: %.2f crossed=%v", total, crossed)
	}
	// The next identical round crosses it exactly.
	total, crossed = chargeDailySpend(cfg, &Response{Tier: LEAD, InputTokens: 400, OutputTokens: 100})
	if !crossed || total < 0.99 {
		t.Errorf("the second round must cross: %.2f crossed=%v", total, crossed)
	}
	// Crossing is reported ONCE — the round that did it — not on every later round.
	if _, again := chargeDailySpend(cfg, &Response{Tier: LEAD, InputTokens: 400, OutputTokens: 100}); again {
		t.Error("crossing must be reported once, not repeatedly")
	}
	if over, _ := overDailySpend(cfg); !over {
		t.Error("the budget is now spent")
	}
}

// What must never be charged or capped: an uncapped agent, work with no
// budget key, and any deployment with no cost rates — a local-only stack has
// no dollars to count and must not be throttled by a ceiling in dollars.
func TestTheSpendCeilingStaysOutOfTheWay(t *testing.T) {
	withRootDB(t)

	withRates(t)
	uncapped := AgentLoopConfig{BudgetKey: "a"}
	if total, crossed := chargeDailySpend(uncapped, &Response{Tier: LEAD, InputTokens: 9_000_000}); total != 0 || crossed {
		t.Error("an agent with no cap is never charged")
	}
	keyless := AgentLoopConfig{DailySpendUSD: 0.01}
	if total, _ := chargeDailySpend(keyless, &Response{Tier: LEAD, InputTokens: 9_000_000}); total != 0 {
		t.Error("work with no budget key is uncapped, never pooled")
	}
	if over, _ := overDailySpend(keyless); over {
		t.Error("a keyless budget can never be over")
	}

	// No rates configured: nothing is priced, so nothing is capped.
	prev := GetCostRates()
	SetCostRates(CostRates{})
	defer SetCostRates(prev)
	local := AgentLoopConfig{BudgetKey: "b", DailySpendUSD: 0.01}
	if total, crossed := chargeDailySpend(local, &Response{Tier: LEAD, InputTokens: 9_000_000}); total != 0 || crossed {
		t.Errorf("with no rates a dollar ceiling must do nothing: %.4f %v", total, crossed)
	}
}

// Spend ages out of the window the same way action counts do.
func TestSpendAgesOutOfTheWindow(t *testing.T) {
	withRootDB(t)
	withRates(t)
	cfg := AgentLoopConfig{BudgetKey: "a", DailySpendUSD: 1.00}
	old := []spendEntry{{At: time.Now().Add(-actionQuotaWindow - time.Minute), USD: 99}}
	RootDB.Set(spendLedgerTable, "a", &old)
	if over, spent := overDailySpend(cfg); over || spent != 0 {
		t.Errorf("spend older than the window must not count: over=%v spent=%.2f", over, spent)
	}
}

// Standing work gets exactly ONE attempt per run at something that has been
// failing, never zero. A count carried in at the guard's limit would refuse
// the call outright on evidence gathered yesterday, so an endpoint fixed
// overnight would stay "broken" until the memory aged out.
func TestFailureMemoryCarriesOneAttemptBack(t *testing.T) {
	withRootDB(t)
	key, sig := "agent:agent-1:sess-1", "call\x00{}"
	saveFailureMemory(key, map[string]int{sig: 9})

	const limit = 3
	carried := map[string]int{}
	loadFailureMemory(key, carried, limit-1)
	if carried[sig] != limit-1 {
		t.Fatalf("carried %d, want %d — one attempt short of the block", carried[sig], limit-1)
	}
	if carried[sig] >= limit {
		t.Error("a fresh run must never start already blocked")
	}
	// That attempt failing again blocks the rest of the run.
	carried[sig]++
	if carried[sig] < limit {
		t.Error("a second failure in the same run must reach the limit")
	}
}
