package orchestrate

// Every agent keeps a cortex, the record of what reached it. Whether the agent
// READS it is what the Cortex setting decides: one that does not still has
// its record written, and never sees it in a prompt.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func cortexLines(t *testing.T, db Database, agentID string) string {
	t.Helper()
	s, _ := loadChatSession(db, agentID, cortexSessionID(agentID))
	var out []string
	for _, m := range s.Messages {
		out = append(out, m.ReportFrom+": "+m.Content)
	}
	return strings.Join(out, "\n")
}

func TestAnAgentThatDoesNotReadItsCortexStillKeepsOne(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	a, err := saveAgent(db, AgentRecord{Name: "Helper", Owner: "u", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	appendCortexObs(db, a.ID, "Lead", cortexKindRequest, "Look up the release notes")
	if got := cortexLines(t, db, a.ID); !strings.Contains(got, "Lead: Look up the release notes") {
		t.Fatalf("the request should be recorded: %q", got)
	}
	// Recorded, never read: nothing of it rides into a dispatch prompt.
	if lines := dispatchPriorReports(a, "dispatch:x", db); len(lines) != 0 {
		t.Errorf("an agent that does not read its cortex got its lines anyway: %v", lines)
	}
}

func TestARunThatReportedElsewhereLeavesAPointer(t *testing.T) {
	root := depStores(t, "u")
	app := orchRef
	stubDelegationRunner(t, app)
	udb := UserDB(root, "u")
	a, _ := saveAgent(udb, AgentRecord{Name: "Reporter", Owner: "u", OrchestratorPrompt: "p"})
	sid := "s-home-" + UUIDv4()

	RunDelegation(context.Background(), RootDB, "u", a.ID, "summarize the night", "")
	// Reported into its own cortex already: no second line.
	before := strings.Count(cortexLines(t, udb, a.ID), "\n")

	sa := StandingAgent{Owner: "u", Name: "nightly", AgentID: a.ID, Mission: "summarize the night",
		ReportAgentID: a.ID, ReportSessionID: sid, Surface: "session"}
	SaveStandingAgent(RootDB, sa)
	if _, err := RunStandingAgentNow(context.Background(), RootDB, "u", "nightly"); err != nil {
		t.Fatal(err)
	}
	got := cortexLines(t, udb, a.ID)
	if strings.Count(got, "\n") != before+1 || !strings.Contains(got, "nightly: FULL RESULT") || !strings.Contains(got, "full report in the session") {
		t.Errorf("a report delivered to a session should leave one pointer in the cortex:\n%s", got)
	}
	if s, ok := loadChatSession(udb, a.ID, sid); !ok || len(s.Messages) == 0 {
		t.Error("the full report still goes to its session")
	}

	// An approved delegation whose result went back to the asker is still a
	// request that reached this agent.
	app.runApprovedDelegation(Authorization{Owner: "u", Agent: a.ID, Brief: "check the queue",
		FromAgent: "lead", FromSession: "s-ask-" + UUIDv4()}, a.ID)
	if got := cortexLines(t, udb, a.ID); !strings.Contains(got, "Asked: check the queue") || !strings.Contains(got, "went back to the conversation that asked") {
		t.Errorf("the delegation should be on the target's record:\n%s", got)
	}
}

func TestANewConversationIsRecordedByItsTitle(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	a, _ := saveAgent(db, AgentRecord{Name: "Helper", Owner: "u", OrchestratorPrompt: "p"})
	start := func(incognito bool) {
		s := ChatSession{ID: UUIDv4(), AgentID: a.ID, Incognito: incognito,
			Messages: []ChatMessage{{Role: "user", Content: "what's the weather?"}, {Role: "assistant", Content: "Sunny."}}}
		saveChatSession(db, s)
		app := &OrchestrateApp{}
		app.LLM = &FakeLLM{Turns: []FakeTurn{{Content: "Weather check", Repeat: true}}}
		ct := &chatTurn{app: app, udb: db, agent: a, user: "u", session: &s, isNewSession: true}
		ct.titleAfterFirstTurn()
	}
	start(false)
	start(true)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(cortexLines(t, db, a.ID), "Started") {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // let the incognito one finish too, if it were going to write
	got := cortexLines(t, db, a.ID)
	if strings.Count(got, "Started:") != 1 || !strings.Contains(got, "Conversation: Started: Weather check") {
		t.Errorf("one line per conversation, by its title, and nothing for a clean-room session:\n%s", got)
	}
}

// An agent created after the page loaded (by Builder, mid-conversation) gets
// its Cortex row from the picker refresh, which now carries both maps.
func TestAPickerRefreshCarriesEveryAgentsCortex(t *testing.T) {
	app, req, udb := authedApp(t)
	reader, _ := saveAgent(udb, AgentRecord{Name: "Reader", Owner: "alice", OrchestratorPrompt: "p", Cortex: true})
	fresh, _ := saveAgent(udb, AgentRecord{Name: "Fresh", Owner: "alice", OrchestratorPrompt: "p"})
	w := httptest.NewRecorder()
	app.handleAgentPickerOptions(w, req("GET", "/api/agent-options", nil))
	var got struct {
		Cortex  map[string]string `json:"cortex_agents"`
		Records map[string]string `json:"record_agents"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if got.Cortex[reader.ID] != cortexSessionID(reader.ID) || got.Records[reader.ID] != "" {
		t.Errorf("an agent that reads its Cortex is in the cortex map only: %+v", got)
	}
	if got.Records[fresh.ID] != cortexSessionID(fresh.ID) || got.Cortex[fresh.ID] != "" {
		t.Errorf("an agent that does not read it keeps it as a record: %+v", got)
	}
}
