package orchestrate

import (
	"context"
	"os"
	"strings"
	"testing"

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

// The channel path has to keep calling into the machine: a behavioural test
// cannot see a surface that silently stops doing so, which is how this gap
// shipped in the first place.
func TestTheChannelPathRunsTheMachine(t *testing.T) {
	src, err := os.ReadFile("agent_dispatch.go")
	if err != nil {
		t.Skip("source unavailable")
	}
	for _, call := range []string{"subTurn.enterMachine(", "subTurn.completeMachine(", "mach.Block()", "mach.narrowCatalog("} {
		if !strings.Contains(string(src), call) {
			t.Errorf("agent_dispatch.go no longer calls %s: an agent's machine would stop running for channel messages", call)
		}
	}
}
