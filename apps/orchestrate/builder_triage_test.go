package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The candidates come from the session's own tool calls, with the failures
// first: a broken tool looks like a misbehaving agent from the chat, and the
// handoff used to name the agent whatever had gone wrong.
func TestTriageCandidatesComeFromTheSession(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	if err := AdminPersistTempTool(udb, "alice", TempTool{Name: "make_song", ScriptBody: "print(1)"}); err != nil {
		t.Fatal(err)
	}
	if err := AdminPersistTempTool(udb, "alice", TempTool{Name: "song_pipe", Mode: TempToolModePipeline}); err != nil {
		t.Fatal(err)
	}
	sess := ChatSession{Messages: []ChatMessage{
		{Role: "user", Content: "make a song"},
		{Role: "assistant", ToolCalls: []PersistedToolCall{
			{Name: "web_search", Result: "ok"},
			{Name: "make_song", Result: "Traceback (most recent call last):\n ImportError"},
			{Name: "make_song", Result: "done"},
			{Name: "song_pipe", Result: "ok"},
			{Name: "machine_step", Label: "triage: intake (agent) → done", Framework: true},
		}},
	}}
	cands := triageCandidates(AgentRecord{ID: "wren", Name: "Wren"}, sess, udb, "alice")
	if len(cands) != 4 {
		t.Fatalf("agent, tool, pipeline and machine expected, built-ins left out: %+v", cands)
	}
	if cands[0].Kind != "tool" || cands[0].Name != "make_song" || cands[0].Failures != 1 || !strings.Contains(cands[0].Evidence, "ImportError") {
		t.Errorf("the failing tool should lead, with its evidence: %+v", cands[0])
	}
	if cands[1].Kind != "agent" || cands[1].ID != "wren" {
		t.Errorf("the agent follows the failures: %+v", cands[1])
	}
	kinds := map[string]string{}
	for _, c := range cands {
		kinds[c.Kind] = c.Name
	}
	if kinds["pipeline"] != "song_pipe" || kinds["machine"] != "triage" {
		t.Errorf("pipelines and machines are candidates too: %+v", kinds)
	}
}

// The staged triage reaches the Builder session that receives exactly that
// brief, once.
func TestTheBriefsSessionClaimsItsTriage(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	cands := []TriageCandidate{{Kind: "agent", Name: "Wren"}}
	stageBuilderTriage(udb, "brief text\n", cands)
	if tr := claimBuilderTriage(udb, "another message"); tr != nil {
		t.Error("a different first message claims nothing")
	}
	tr := claimBuilderTriage(udb, "  brief text")
	if tr == nil || len(tr.Candidates) != 1 || !tr.pending() {
		t.Fatalf("the brief's session should claim an open triage: %+v", tr)
	}
	if claimBuilderTriage(udb, "brief text") != nil {
		t.Error("a staged triage is claimed once")
	}
}

// Editing is refused until a target is named; reads pass, and naming one
// opens it.
func TestTheGateHoldsEditsUntilATargetIsNamed(t *testing.T) {
	tr := &BuilderTriage{Candidates: []TriageCandidate{{Kind: "tool", Name: "make_song"}, {Kind: "agent", Name: "Wren", ID: "wren"}}}
	for _, c := range []struct {
		name string
		args map[string]any
	}{
		{"tool_def", map[string]any{"action": "update"}},
		{"update_agent", map[string]any{}},
		{"eval", map[string]any{"action": "add_case"}},
		{"tool_def", map[string]any{"action": "made_up"}},
	} {
		if !triageBlocks(tr, c.name, c.args) {
			t.Errorf("%s %v should be refused before a target", c.name, c.args)
		}
	}
	for _, c := range []struct {
		name string
		args map[string]any
	}{
		{"tool_def", map[string]any{"action": "get"}},
		{"tool_def", map[string]any{"action": "test"}},
		{"agents", map[string]any{"action": "get"}},
		{"web_search", map[string]any{}},
	} {
		if triageBlocks(tr, c.name, c.args) {
			t.Errorf("%s %v is a read and should pass", c.name, c.args)
		}
	}
	if triageBlocks(nil, "update_agent", nil) {
		t.Error("a session with no handoff is not gated")
	}

	turn := &chatTurn{session: &ChatSession{Triage: tr}}
	choose := turn.chooseTargetToolDef().Handler
	if _, err := choose(context.Background(), map[string]any{"kind": "script", "name": "x", "why": "y"}); err == nil {
		t.Error("an unknown kind is refused")
	}
	if _, err := choose(context.Background(), map[string]any{"kind": "tool", "name": "make_song"}); err == nil {
		t.Error("a target without its evidence is refused")
	}
	out, err := choose(context.Background(), map[string]any{"kind": "tool", "name": "MAKE_SONG", "why": "it failed with an ImportError"})
	if err != nil || !strings.Contains(out, "Editing is open") {
		t.Fatalf("naming a candidate opens editing: %q %v", out, err)
	}
	if triageBlocks(tr, "tool_def", map[string]any{"action": "update"}) {
		t.Error("with a target named, edits go through")
	}
	if tr.Targets[0].Name != "make_song" {
		t.Errorf("the candidate's own name is kept: %+v", tr.Targets)
	}
	out, _ = choose(context.Background(), map[string]any{"kind": "machine", "name": "other", "why": "the user chose it"})
	if !strings.Contains(out, "not among the candidates") || len(tr.Targets) != 2 {
		t.Errorf("a second target outside the list is taken, and flagged: %q", out)
	}
}

// The brief lists the candidates and points Builder at choose_target.
func TestTheBriefCarriesTheCandidates(t *testing.T) {
	brief := buildBuilderBrief(AgentRecord{ID: "wren", Name: "Wren"}, ChatSession{Messages: []ChatMessage{{Role: "user", Content: "hi"}}}, "", true,
		[]TriageCandidate{{Kind: "tool", Name: "make_song", Evidence: "ran 2 time(s), failed 1"}})
	for _, want := range []string{"What could be at fault", "tool `make_song`: ran 2 time(s), failed 1", "choose_target(kind, name, why)", "ask_user"} {
		if !strings.Contains(brief, want) {
			t.Errorf("the brief should carry %q", want)
		}
	}
	if strings.Index(brief, "What could be at fault") > strings.Index(brief, "session transcript to analyze") {
		t.Error("the candidates belong before the fenced transcript")
	}
}
