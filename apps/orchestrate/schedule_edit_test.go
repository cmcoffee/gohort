package orchestrate

// An agent asked to change "its" scheduled task: a scheduled run in a link
// group that runs as it, set up for it. It listed its schedules and was told
// "Nothing is scheduled" four times while introspect named the run, could not
// read what the run does, and could only change it by re-issuing create,
// which needs a time the link group's member does not have of its own. It
// gave up. Now list shows the runs that run as the agent, with what each
// does, and edit changes only that.

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAnAgentSeesAndEditsTheScheduledRunsThatRunAsIt(t *testing.T) {
	linkTestSetup(t)
	SaveStandingAgent(RootDB, StandingAgent{Name: "follow-up engagement", Owner: "u", AgentID: "a1",
		Mission: "Reply to new mentions and comments.", LinkGroup: "g1", Cron: "daily 09:00"})
	SaveStandingAgent(RootDB, StandingAgent{Name: "someone else's run", Owner: "u", AgentID: "b2",
		Mission: "Not a1's to change."})
	SaveEventMonitor(RootDB, EventMonitor{Name: "price-watch", Owner: "u", WakeAgent: "a1",
		Kind: EventKindHTTP, WakeBrief: "tell the user the price"})

	// A plain agent: no fleet tools, its own recurring path only.
	tool := scheduleToolDef(nil, "u", "a1", nil, &recordingRecurring{})
	call := func(args map[string]any) (string, error) { return tool.Handler(context.Background(), args) }

	out, err := call(map[string]any{"action": "list"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"follow-up engagement", "link group", "Reply to new mentions and comments."} {
		if !strings.Contains(out, want) {
			t.Errorf("list does not show %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "someone else's run") {
		t.Errorf("list shows a run that runs as another agent:\n%s", out)
	}

	if _, err := call(map[string]any{"action": "edit", "name": "follow-up engagement"}); err == nil {
		t.Error("an edit with no then was accepted")
	}
	mission := "Reply to new mentions and comments. List every reply with its full Moltbook link."
	if _, err := call(map[string]any{"action": "edit", "name": "follow-up engagement", "then": mission}); err != nil {
		t.Fatal(err)
	}
	sa, _ := GetStandingAgent(RootDB, "u", "follow-up engagement")
	if sa.Mission != mission {
		t.Errorf("the mission is %q", sa.Mission)
	}
	if sa.LinkGroup != "g1" || sa.Cron != "daily 09:00" {
		t.Errorf("the edit moved its timing: link group %q, cron %q", sa.LinkGroup, sa.Cron)
	}

	if _, err := call(map[string]any{"action": "edit", "name": "someone else's run", "then": "x"}); err == nil {
		t.Error("an agent changed a run that runs as another agent")
	}
	if cur, _ := GetStandingAgent(RootDB, "u", "someone else's run"); cur.Mission != "Not a1's to change." {
		t.Error("the other agent's run was changed")
	}

	if _, err := call(map[string]any{"action": "edit", "name": "price-watch", "then": "tell the user the price and the day's change"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := GetEventMonitor(RootDB, "u", "price-watch"); m.WakeBrief != "tell the user the price and the day's change" {
		t.Errorf("the monitor's brief is %q", m.WakeBrief)
	}
}
