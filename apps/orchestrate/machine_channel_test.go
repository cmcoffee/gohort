package orchestrate

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// A run with no web session (a channel inbound, a wake, a delegation) keeps
// its machine cursor on the thread it loaded. Before, enterMachine required a
// web session, so an agent's machine ran only in web chat and everything that
// arrived by bridge went straight to the agent.
func channelTurnFixture(t *testing.T, def MachineDef) *chatTurn {
	t.Helper()
	turn, _ := machineTurnFixture(t, def)
	thread, err := saveChatSession(turn.udb, ChatSession{ID: "channel:a1", AgentID: "a1"})
	if err != nil {
		t.Fatal(err)
	}
	turn.session = nil
	turn.machineThread = &thread
	return turn
}

func TestAChannelRunEntersTheAgentsMachine(t *testing.T) {
	turn := channelTurnFixture(t, residentMachine())
	m := turn.enterMachine("a message over the bridge")
	if !m.on || m.Name() != "answer" {
		t.Fatalf("a channel run should enter the machine and park on answer, got on=%v step=%q", m.on, m.Name())
	}
	if turn.machineThread.Phase != "answer" || turn.machineThread.MachineID == "" {
		t.Errorf("the cursor should sit on the run's thread: %+v", turn.machineThread)
	}
	if got, ok := loadChatSession(turn.udb, "a1", "channel:a1"); !ok || got.Phase != "answer" {
		t.Errorf("the cursor should persist on the thread, got %+v", got)
	}
}

// The waiting step's next is what routes every message: after the reply the
// thread moves on, and the next inbound starts there.
func TestAChannelRunHandsOffAfterItsTurn(t *testing.T) {
	turn := channelTurnFixture(t, MachineDef{
		Name: "relay", Start: "first",
		Phases: []MachinePhase{
			{Name: "first", Prompt: "Reply.", Resident: true, Next: "second"},
			{Name: "second", Prompt: "Reply again.", Resident: true},
		},
	})
	m := turn.enterMachine("hello")
	turn.completeMachine(m)
	if turn.machineThread.Phase != "second" {
		t.Fatalf("after its turn the waiting step should hand off to its next, got %q", turn.machineThread.Phase)
	}
	if got, _ := loadChatSession(turn.udb, "a1", "channel:a1"); got.Phase != "second" {
		t.Errorf("the handoff should persist on the thread, got %q", got.Phase)
	}
}

func TestADispatchedStepThatNamesTheLeadGetsIt(t *testing.T) {
	turn := channelTurnFixture(t, MachineDef{
		Name: "lead-step", Start: "think",
		Phases: []MachinePhase{{Name: "think", Prompt: "Reason it through.", Resident: true, Model: "lead"}},
	})
	turn.enterMachine("hello")
	open := WithNetworkConnector(context.Background(), NewNetworkConnector(false))
	if pin, key := dispatchRouting(open, turn); pin != LEAD || key != orchestratorRouteKey("a1", true) {
		t.Errorf("a step naming the lead should route there: got (%v, %q)", pin, key)
	}
	closed := WithNetworkConnector(context.Background(), NewNetworkConnector(true))
	if pin, key := dispatchRouting(closed, turn); pin == LEAD || key != "app.orchestrate.worker" {
		t.Errorf("a Private run stays on the worker whatever the step asks: got (%v, %q)", pin, key)
	}
}

// Every way an agent is reached runs its machine: a machine replaces the
// agent's brain. It ran on web chat alone, then on the channel path alone, and
// a delegated agent answered as a plain one. A behavioural test cannot see a
// path that silently stops entering it, which is how each gap shipped.
func TestEveryDispatchPathRunsTheMachine(t *testing.T) {
	for file, want := range map[string]int{
		"agent_dispatch.go":      2, // the continuing path (channels, wakes, handoffs) and delegations
		"agents_grouped_tool.go": 1, // an awaited agents(run)
		"scheduled_updates.go":   1, // a scheduled fire
	} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Skip("source unavailable")
		}
		body := string(src)
		if n := strings.Count(body, "subTurn.enterDispatchMachine("); n != want {
			t.Errorf("%s enters the agent's machine %d time(s), want %d", file, n, want)
		}
		for _, call := range []string{"subTurn.machineRelay()", "subTurn.machineToolFilter()"} {
			if !strings.Contains(body, call) {
				t.Errorf("%s no longer calls %s: a machine step there would lose its relay or its deny", file, call)
			}
		}
	}
	if src, err := os.ReadFile("agent_dispatch.go"); err == nil && !strings.Contains(string(src), "subTurn.machineTrace") {
		t.Error("the continuing path no longer stores the machine's steps")
	}
}

// The log line says what the machine did with the message, so a turn that
// resumed a parked step reads as that rather than as the machine being skipped.
func TestTheMachineLogSaysWhetherItRouted(t *testing.T) {
	start := time.Now()
	earlier := start.Add(-time.Hour)
	hops := []PhaseHop{
		{From: "Router", To: "Direct", At: earlier}, // a previous turn's walk
		{From: "Router", To: "Decide", At: start},
		{From: "Decide", To: "Direct", At: start.Add(time.Millisecond)},
	}
	if got := machineWalkSummary("Direct", "Direct", hops, start); got != "walked Router → Decide → Direct (Direct answers)" {
		t.Errorf("a routed turn should name its path, got %q", got)
	}
	if got := machineWalkSummary("Direct", "Direct", hops[:1], start); !strings.Contains(got, "resumed in Direct") || !strings.Contains(got, "nothing was routed") {
		t.Errorf("a parked turn should say nothing was routed, got %q", got)
	}
	if got := machineWalkSummary("", "answer", nil, start); !strings.HasPrefix(got, "started in answer") {
		t.Errorf("a first turn that starts in a waiting step should say so, got %q", got)
	}
}

// The machine's steps land in the turn's trace, so a channel message's card
// shows the routing: each step the walk ran this turn, what it decided, and the
// delegate it handed the work to. A restart's hop off a waiting step and an
// earlier turn's hops are not steps this turn ran.
func TestTheTraceRecordsTheStepsThisTurnRan(t *testing.T) {
	def := MachineDef{Name: "HumorRouter", Start: "Router", Phases: []MachinePhase{
		{Name: "Router", Next: "Decide"},
		{Name: "Decide", Choices: []string{"Delegate", "Direct"}},
		{Name: "Delegate", Agent: "Comedian", Next: "Report"},
		{Name: "Direct", Resident: true},
		{Name: "Report", Resident: true},
	}}
	start := time.Now()
	cur := &MachineCursor{
		Log: []PhaseHop{
			{From: "Router", To: "Decide", At: start.Add(-time.Hour)}, // an earlier turn
			{From: "Direct", To: "Router", At: start},                 // the restart
			{From: "Router", To: "Decide", At: start},
			{From: "Decide", To: "Delegate", At: start},
			{From: "Delegate", To: "Report", At: start},
		},
		State: MachineState{
			"Router":   {Text: "yes"},
			"Decide":   {Fields: map[string]any{"next_step": "Delegate"}},
			"Delegate": {Text: "Why did the scarecrow win an award?"},
		},
	}
	trace := machineStepTrace(def, cur, start)
	if len(trace) != 3 {
		t.Fatalf("three steps ran this turn, got %d: %+v", len(trace), trace)
	}
	for i, want := range []string{"Router", "Decide", "Delegate"} {
		if trace[i].Args["step"] != want || !trace[i].Framework || trace[i].Name != "machine_step" {
			t.Errorf("record %d should be framework step %s, got %+v", i, want, trace[i])
		}
	}
	if trace[2].Args["agent"] != "Comedian" || trace[2].Args["then"] != "Report" || !strings.Contains(trace[2].Result, "scarecrow") {
		t.Errorf("the delegate step should name the agent, where it went next, and what came back: %+v", trace[2])
	}
	if trace[2].Label != "HumorRouter: Delegate (Comedian) → Report" {
		t.Errorf("the list should read as the step it was, got %q", trace[2].Label)
	}
	if !strings.Contains(trace[1].Result, "Delegate") {
		t.Errorf("a deciding step with only fields should still show its decision: %+v", trace[1])
	}
}

// A framework record is shown, never replayed: the model did not make that
// call, and a history saying it called machine_step invites it to try.
func TestFrameworkRecordsAreNotReplayedToTheModel(t *testing.T) {
	step := PersistedToolCall{Name: "machine_step", Args: map[string]any{"step": "Router"}, Result: "yes", Framework: true}
	real := PersistedToolCall{Name: "web_search", Args: map[string]any{"q": "x"}, Result: "hits"}

	only := toLLMMessages([]ChatMessage{{Role: "assistant", Content: "a joke", ToolCalls: []PersistedToolCall{step}}})
	if len(only) != 1 || len(only[0].ToolCalls) != 0 {
		t.Fatalf("a reply whose only records are the machine's should replay as plain text, got %+v", only)
	}
	mixed := toLLMMessages([]ChatMessage{{Role: "assistant", Content: "found it", ToolCalls: []PersistedToolCall{step, real}}})
	if len(mixed) != 2 || len(mixed[0].ToolCalls) != 1 || mixed[0].ToolCalls[0].Name != "web_search" || len(mixed[1].ToolResults) != 1 {
		t.Fatalf("only the model's own call should replay, got %+v", mixed)
	}
}

// A step with reply_with sends the rendered text in place of a model turn, so
// a relay cannot be rewritten, re-delegated or declined. It still answers to
// the agent's output rules, and hands back to the step's prompt when there is
// nothing to fill it or a rule stops it.
func TestAStepThatRepliesWithSendsTheTextItself(t *testing.T) {
	relayTurn := func(state MachineState, blocked bool) *chatTurn {
		ph := MachinePhase{Name: "Report", Resident: true, ReplyWith: "{state:Delegate}"}
		turn := &chatTurn{agent: AgentRecord{ID: "a1"}, machine: turnMachine{
			def: MachineDef{Name: "m", Phases: []MachinePhase{ph}}, phase: ph, state: state, on: true, input: "tell a joke",
		}}
		turn.guardrails = &guardrailEnforcement{Check: func(hook, candidate string) GuardrailDecision {
			return GuardrailDecision{Blocked: blocked && hook == GuardHookPreOutput}
		}}
		return turn
	}
	joke := MachineState{"Delegate": {Text: "Why did the scarecrow win an award?"}}
	if got := relayTurn(joke, false).machineRelay(); got != "Why did the scarecrow win an award?" {
		t.Errorf("the delegate's answer should be the reply, got %q", got)
	}
	if got := relayTurn(MachineState{}, false).machineRelay(); got != "" {
		t.Errorf("with nothing to fill it, the step answers from its prompt, got %q", got)
	}
	if got := relayTurn(joke, true).machineRelay(); got != "" {
		t.Errorf("an output rule that stops the relay hands the turn back, got %q", got)
	}
	plain := relayTurn(joke, false)
	plain.machine.phase.ReplyWith = ""
	if got := plain.machineRelay(); got != "" {
		t.Errorf("a step without reply_with runs as usual, got %q", got)
	}
}

// A web turn stores the machine's steps ahead of the model's own calls, on
// every path a reply can be saved by. The direct-reply path, the usual one for
// a machine turn, stored only the model's calls, so the routing never showed.
func TestEveryWebSavePathKeepsTheMachineSteps(t *testing.T) {
	step := PersistedToolCall{Name: "machine_step", Label: "M: A → B", Framework: true}
	own := PersistedToolCall{Name: "web_search"}
	turn := &chatTurn{machineTrace: []PersistedToolCall{step}}
	got := turn.withMachineTrace([]PersistedToolCall{own})
	if len(got) != 2 || got[0].Name != "machine_step" || got[1].Name != "web_search" {
		t.Fatalf("the machine's steps first, then the model's calls: %+v", got)
	}
	if bare := (&chatTurn{}).withMachineTrace([]PersistedToolCall{own}); len(bare) != 1 {
		t.Errorf("no machine, no change: %+v", bare)
	}
	src, err := os.ReadFile("runner_http.go")
	if err != nil {
		t.Skip("source unavailable")
	}
	if n := strings.Count(string(src), "turn.withMachineTrace("); n != 3 {
		t.Errorf("the direct-reply, question and plan save paths must all store the machine's steps; found %d", n)
	}
	if runner, err := os.ReadFile("runner.go"); err == nil && !strings.Contains(string(runner), "t.emitMachineTrace()") {
		t.Error("the web turn must show the machine's steps live")
	}
}

// A delegated run with a thread resumes its walk there; one that starts fresh
// walks from the first step and leaves nothing stored behind it.
func TestADispatchedRunEntersTheMachine(t *testing.T) {
	turn, _ := machineTurnFixture(t, residentMachine())
	turn.session = nil
	sys := "persona"
	tools := []AgentToolDef{{Tool: Tool{Name: "web_search"}}}
	thread := &ChatSession{ID: "external-dispatch:u:a1", AgentID: "a1"}
	m := turn.enterDispatchMachine(thread, true, "hello", &sys, &tools, "delegation")
	if !m.on || m.Name() != "answer" {
		t.Fatalf("the delegated agent should run its machine, got on=%v step=%q", m.on, m.Name())
	}
	if sys == "persona" {
		t.Error("the step's block should be added to the prompt")
	}
	if _, stored := loadChatSession(turn.udb, "a1", thread.ID); stored {
		t.Error("a run that starts fresh must not leave its machine position stored")
	}
	if turn.machineToolFilter() == nil {
		t.Error("a running machine keeps its deny in force")
	}
}

// Two agents whose machines delegate to each other would never stop: past the
// limit the agent answers plainly, and says so.
func TestMachineDelegationIsBounded(t *testing.T) {
	turn, _ := machineTurnFixture(t, residentMachine())
	ctx := context.Background()
	for i := 0; i <= maxMachineDelegation; i++ {
		ctx = withMachineDelegation(ctx)
	}
	turn.ctx = ctx
	if m := turn.enterMachine("hello"); m.on {
		t.Fatal("past the delegation limit the agent should run without its machine")
	}
	turn.ctx = withMachineDelegation(context.Background())
	if m := turn.enterMachine("hello"); !m.on {
		t.Error("one delegation deep is the ordinary router-to-agent case and must still run the machine")
	}
}
