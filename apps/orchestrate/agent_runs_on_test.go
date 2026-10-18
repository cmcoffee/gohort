package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Reading an agent shows what runs as it, with what each one does: a post cap
// written into a schedule's mission is part of how the agent behaves, and
// reading only the record hid it.
func TestReadingAnAgentShowsWhatRunsAsIt(t *testing.T) {
	depStores(t, "u")
	turn, target := newRunGateTurn(t, "")
	turn.udb = UserDB(orchestrateBaseDB, "u")
	poster, err := saveAgent(turn.udb, AgentRecord{Name: "Poster", Owner: "u", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := saveAgent(turn.udb, AgentRecord{Name: "Other", Owner: "u", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	_ = target
	SaveStandingAgent(RootDB, StandingAgent{Owner: "u", Name: "Daily engagement", AgentID: poster.ID,
		Mission: "1. Count today's posts. 2. If post count >= 5, skip posting."})
	SaveStandingAgent(RootDB, StandingAgent{Owner: "u", Name: "Someone else's run", AgentID: other.ID, Mission: "unrelated"})

	for _, full := range []bool{false, true} {
		out, err := turn.agentsGetAction(map[string]any{"id": poster.ID, "full": full})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, `"runs_on":`) || !strings.Contains(out, "Daily engagement") || !strings.Contains(out, "skip posting") {
			t.Errorf("full=%v: the schedule that runs as the agent, and what it does, are missing:\n%s", full, out)
		}
		if strings.Contains(out, "Someone else's run") {
			t.Errorf("full=%v: another agent's schedule was listed", full)
		}
		if !strings.Contains(out, `"_runs_on_note":`) {
			t.Errorf("full=%v: nothing says the missions are instructions too", full)
		}
	}
	// Nothing runs as it: the view is as it was.
	if out, _ := turn.agentsGetAction(map[string]any{"id": target.ID}); strings.Contains(out, "runs_on") {
		t.Errorf("an agent nothing runs as got a runs_on field: %s", out)
	}
}

// An approval card says what an edit changes, for every kind of edit.
func TestAScheduleEditCardShowsTheChange(t *testing.T) {
	for _, tc := range []struct {
		change missionChange
		want   string
	}{
		{missionChange{Find: "Max 5 replies per cycle. "}, `Remove from what it does: "Max 5 replies per cycle."`},
		{missionChange{Find: "5", Replace: "10"}, `In what it does, replace "5" with "10"`},
		{missionChange{Append: "Then post a summary."}, "Add to what it does: Then post a summary."},
		{missionChange{Then: "Post daily."}, "Change what it does: Post daily."},
	} {
		if got := tc.change.describe(); got != tc.want {
			t.Errorf("%+v described as %q, want %q", tc.change, got, tc.want)
		}
	}
}
