package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func scheduleHomeSession(t *testing.T) *ToolSession {
	t.Helper()
	return &ToolSession{Username: "craig@example.com", DB: &DBase{Store: kvlite.MemStore()}}
}

// THE BUG. Builder holds the operator toolset on purpose, so that "build me a
// tool and run it every 30 minutes" is one job rather than a build plus a
// handoff. But a schedule it created reported back to Builder, in the build
// session that created it: it ran correctly and filed its findings in a
// workshop nobody reopens, which reads exactly like the schedule going to the
// wrong agent.
func TestAnAuthorHandsAScheduleToTheAgentThatRunsIt(t *testing.T) {
	sess := scheduleHomeSession(t)
	if got := scheduleHomeAgent(sess, "seed-builder", "agent-weather"); got != "agent-weather" {
		t.Errorf("Builder kept a schedule it built for another agent: home=%q", got)
	}
	// A controller keeps its own: delegating and then reading the results is
	// what a Fleet agent is for.
	if got := scheduleHomeAgent(sess, "seed-chat", "agent-weather"); got != "seed-chat" {
		t.Errorf("a controller lost the schedule it manages: home=%q", got)
	}
	// An agent scheduling itself is unaffected, whoever it is.
	if got := scheduleHomeAgent(sess, "seed-builder", "seed-builder"); got != "seed-builder" {
		t.Errorf("self-scheduling was rerouted: home=%q", got)
	}
	// A schedule with no runner agent (a pipeline or a machine) has nowhere
	// else to go, so it stays with whoever set it up.
	if got := scheduleHomeAgent(sess, "seed-builder", ""); got != "seed-builder" {
		t.Errorf("a pipeline schedule was orphaned: home=%q", got)
	}
}

// Authoring is a capability, not an identity, so an agent granted it has the
// same problem for the same reason. Unless it is also a controller.
func TestTheHandoffFollowsTheCapabilityNotTheName(t *testing.T) {
	sess := scheduleHomeSession(t)
	author, err := saveAgent(sess.DB, AgentRecord{
		Owner: sess.Username, Name: "Author", OrchestratorPrompt: "You author.", Author: true,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := scheduleHomeAgent(sess, author.ID, "agent-weather"); got != "agent-weather" {
		t.Errorf("an authoring agent kept a schedule it built for another: home=%q", got)
	}

	both, err := saveAgent(sess.DB, AgentRecord{
		Owner: sess.Username, Name: "Conductor", OrchestratorPrompt: "You conduct.", Author: true, Fleet: true,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := scheduleHomeAgent(sess, both.ID, "agent-weather"); got != both.ID {
		t.Errorf("a fleet controller that can also author lost its schedule: home=%q", got)
	}

	// A plain agent keeps its own schedules.
	plain, _ := saveAgent(sess.DB, AgentRecord{Owner: sess.Username, Name: "Plain", OrchestratorPrompt: "You help."})
	if got := scheduleHomeAgent(sess, plain.ID, "agent-weather"); got != plain.ID {
		t.Errorf("a plain agent's schedule was rerouted: home=%q", got)
	}
}

// A monitor has no run target: its whole shape is "wake me when this happens".
// So an author has to say whose monitor it is, and is TOLD so rather than left
// to find out when the alert never arrives.
func TestAnAuthorMustSayWhichAgentAMonitorWakes(t *testing.T) {
	sess := scheduleHomeSession(t)

	_, err := resolveMonitorWakeAgent(sess, "seed-builder", "", EventNotifyChannel, "")
	if err == nil {
		t.Fatal("Builder was allowed to point a monitor at its own build session")
	}
	for _, want := range []string{"wake_agent", "nobody will see it", "notify"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}

	// Delivered straight to a person or a chat: nothing has to wake up for it
	// to work, so there is nothing to refuse.
	if _, err := resolveMonitorWakeAgent(sess, "seed-builder", "", EventNotifyDirect, ""); err != nil {
		t.Errorf("a direct-delivery monitor was refused: %v", err)
	}
	if _, err := resolveMonitorWakeAgent(sess, "seed-builder", "", EventNotifyChannel, "chat-123"); err != nil {
		t.Errorf("a monitor delivering to a chat was refused: %v", err)
	}

	// Any other agent keeps the old behavior: it wakes itself.
	if got, err := resolveMonitorWakeAgent(sess, "seed-chat", "", EventNotifyChannel, ""); err != nil || got != "seed-chat" {
		t.Errorf("a controller's own monitor changed: %q %v", got, err)
	}
}

// And naming an agent resolves it, by name as well as id, because that is what
// the model has to hand.
func TestWakeAgentResolvesByName(t *testing.T) {
	sess := scheduleHomeSession(t)
	target, err := saveAgent(sess.DB, AgentRecord{
		Owner: sess.Username, Name: "Weather", OrchestratorPrompt: "You watch the weather.",
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := resolveMonitorWakeAgent(sess, "seed-builder", "Weather", EventNotifyChannel, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != target.ID {
		t.Errorf("wake agent = %q, want %q", got, target.ID)
	}
	// A name that does not exist is an error naming the fix, not a silent
	// fallback to waking the author.
	if _, err := resolveMonitorWakeAgent(sess, "seed-builder", "Nobody", EventNotifyChannel, ""); err == nil {
		t.Error("an unknown wake_agent was accepted")
	} else if !strings.Contains(err.Error(), "create_agent") {
		t.Errorf("the error does not say what to do: %v", err)
	}
}

// recurring(schedule) puts THIS agent on a clock, with no parameter to point
// it elsewhere. Asked to schedule something, Builder would schedule ITSELF to
// replay a build prompt forever, and the agent the user was talking about
// would have nothing on it at all. Refusing and naming the right tool beats
// being wrong once a minute.
func TestAnAuthorCannotPutItselfOnAClock(t *testing.T) {
	turn := &chatTurn{agent: AgentRecord{ID: "seed-builder", Name: "Builder"}, session: &ChatSession{ID: "s1"}}
	_, err := turn.recurringSchedule(map[string]any{"prompt": "check the thing", "pattern": "hourly"})
	if err == nil {
		t.Fatal("Builder scheduled itself to replay a build prompt on a clock")
	}
	if !strings.Contains(err.Error(), "create_standing_agent") || !strings.Contains(err.Error(), "agent_id") {
		t.Errorf("the refusal does not name the tool that does this properly: %v", err)
	}

	// Every other agent keeps its own recurring tasks: "remind me in an hour"
	// is exactly this tool's job.
	plain := &chatTurn{agent: AgentRecord{ID: "a1", Name: "Helper"}, session: &ChatSession{ID: "s1"}}
	if _, err := plain.recurringSchedule(map[string]any{"prompt": "check the thing", "pattern": "hourly"}); err != nil {
		if strings.Contains(err.Error(), "create_standing_agent") {
			t.Errorf("an ordinary agent was refused its own recurring task: %v", err)
		}
	}
}
