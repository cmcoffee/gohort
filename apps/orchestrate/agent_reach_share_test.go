package orchestrate

// One action for one request, and the ability to take it back without touching
// anything the owner did on purpose.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func sharedAgentFixture(t *testing.T) (Database, AgentRecord) {
	t.Helper()
	udb := reachFixture(t)
	SaveSkill(RootDB, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess."})
	a := AgentRecord{ID: "a1", Name: "Troubleshooter", Owner: "alice",
		OrchestratorPrompt: "help",
		AllowedUsers:       []string{"bob", "carol"}, AllowedSkills: []string{"s1"}}
	return udb, a
}

// The point of the whole thing: one action, and the people who have the agent
// have what it uses.
func TestTheFanOutGivesDependenciesTheAgentsRecipients(t *testing.T) {
	udb, a := sharedAgentFixture(t)
	if got := agentReachOf(udb, "alice", a).Gaps; got != 1 {
		t.Fatalf("expected one gap to start with, got %d", got)
	}

	lines := fanOutAgentShare(udb, "alice", a)
	if len(lines) != 1 || !strings.Contains(lines[0], "Triage") {
		t.Fatalf("the report does not say what it did: %v", lines)
	}
	if got := agentReachOf(udb, "alice", a).Gaps; got != 0 {
		t.Errorf("the gap survived the fan-out: %+v", agentReachOf(udb, "alice", a).Items)
	}
	// Through the skill's OWN list, which is the only grant model there is.
	for _, s := range LoadSkills(udb, "alice") {
		if s.ID == "s1" && (len(s.AllowedUsers) != 2 || !namedIn(s.AllowedUsers, "bob")) {
			t.Errorf("the skill's own recipient list is wrong: %+v", s.AllowedUsers)
		}
	}
}

// Running it twice is not two grants, and says so rather than reporting work
// it did not do.
func TestASecondFanOutHasNothingToDo(t *testing.T) {
	udb, a := sharedAgentFixture(t)
	fanOutAgentShare(udb, "alice", a)
	lines := fanOutAgentShare(udb, "alice", a)
	if len(lines) != 1 || !strings.Contains(lines[0], "Nothing to do") {
		t.Errorf("a repeat reports work: %v", lines)
	}
}

// Taking somebody off the agent takes back what the fan-out gave THEM, and
// leaves everybody else's grant alone.
func TestDroppingARecipientTakesBackWhatTheShareGave(t *testing.T) {
	udb, a := sharedAgentFixture(t)
	saveAgent(udb, a)
	fanOutAgentShare(udb, "alice", a)

	a.AllowedUsers = []string{"carol"}
	if _, err := saveAgent(udb, a); err != nil {
		t.Fatalf("save: %v", err)
	}
	for _, s := range LoadSkills(udb, "alice") {
		if s.ID != "s1" {
			continue
		}
		if namedIn(s.AllowedUsers, "bob") {
			t.Errorf("bob left the agent and kept the skill: %+v", s.AllowedUsers)
		}
		if !namedIn(s.AllowedUsers, "carol") {
			t.Errorf("carol was still on the agent and lost the skill: %+v", s.AllowedUsers)
		}
	}
}

// The distinction the ledger exists for. A share the owner made by hand, for
// their own reasons, survives an un-share of some agent that happens to use the
// same record — otherwise one convenient click makes every later removal a
// thing to be afraid of.
func TestAHandMadeShareIsNeverClawedBack(t *testing.T) {
	udb := reachFixture(t)
	SaveSkill(RootDB, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: ".",
		AllowedUsers: []string{"bob"}})
	a := AgentRecord{ID: "a1", Name: "Troubleshooter", Owner: "alice",
		OrchestratorPrompt: "help",
		AllowedUsers:       []string{"bob"}, AllowedSkills: []string{"s1"}}
	saveAgent(udb, a)
	// Nothing for the fan-out to do: bob already had the skill, by hand.
	fanOutAgentShare(udb, "alice", a)

	a.AllowedUsers = nil
	saveAgent(udb, a)
	for _, s := range LoadSkills(udb, "alice") {
		if s.ID == "s1" && !namedIn(s.AllowedUsers, "bob") {
			t.Error("a share the owner made by hand was taken back by an unrelated un-share")
		}
	}
}

// A tool is the one gap the owner cannot close, and the report has to say so.
// Listing only successes is how somebody concludes their team has a working
// agent while it is still missing a tool.
func TestTheReportNamesWhatItCouldNotDo(t *testing.T) {
	udb := reachFixture(t)
	if err := AdminPersistTempTool(AuthDB(), "alice", TempTool{Name: "ssh_run"}); err != nil {
		t.Skipf("no persistent tool store in this configuration: %v", err)
	}
	a := AgentRecord{ID: "a1", Name: "Troubleshooter", Owner: "alice",
		AllowedUsers: []string{"bob"}, AllowedTools: []string{"ssh_run"}}

	lines := fanOutAgentShare(udb, "alice", a)
	joined := strings.Join(lines, " | ")
	if !strings.Contains(joined, "ssh_run") || !strings.Contains(joined, "admin") {
		t.Errorf("the report does not name the tool it could not share: %v", lines)
	}
}

// A private agent has nobody to share with, and a published one needs the
// deployment rung, which is not the owner's to grant. Neither is an error and
// neither silently does nothing.
func TestTheFanOutRefusesTheTwoCasesItCannotServe(t *testing.T) {
	udb := reachFixture(t)
	SaveSkill(RootDB, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "."})

	priv := AgentRecord{ID: "a1", Owner: "alice", AllowedSkills: []string{"s1"}}
	if lines := fanOutAgentShare(udb, "alice", priv); !strings.Contains(lines[0], "private") {
		t.Errorf("a private agent: %v", lines)
	}
	pub := AgentRecord{ID: "a2", Owner: "alice", Exposed: true, AllowedSkills: []string{"s1"}}
	if lines := fanOutAgentShare(udb, "alice", pub); !strings.Contains(lines[0], "admin") {
		t.Errorf("a published agent: %v", lines)
	}
}
