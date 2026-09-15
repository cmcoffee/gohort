package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The listing predicate has to agree with the gate that refuses one dispatch at
// a time. The gate keeps its own branches because each writes a different
// refusal; this is the matrix that shows up a disagreement.
func TestReachablePredicateMatchesTheGatesRules(t *testing.T) {
	caller := func(mode string, targets ...string) AgentRecord {
		return AgentRecord{ID: "caller", Name: "Caller", DispatchMode: mode, AllowedDispatchTargets: targets}
	}
	open := AgentRecord{ID: "open", Name: "Open"}
	hidden := AgentRecord{ID: "hidden", Name: "Hidden", Hidden: true}

	cases := []struct {
		name   string
		caller AgentRecord
		target AgentRecord
		want   bool
	}{
		{"all reaches an open agent", caller(dispatchAll), open, true},
		{"all does not reach a hidden one", caller(dispatchAll), hidden, false},
		{"none reaches nothing", caller(dispatchNone), open, false},
		{"only reaches what is listed", caller(dispatchOnly, "open"), open, true},
		{"only refuses what is not", caller(dispatchOnly, "other"), open, false},
		{"an explicit pick beats Hidden", caller(dispatchOnly, "hidden"), hidden, true},
		{"except refuses what is listed", caller(dispatchExcept, "open"), open, false},
		{"except still honours Hidden", caller(dispatchExcept, "other"), hidden, false},
		{"a blank list with no mode reads as all", caller(""), open, true},
		{"a blank mode WITH a list reads as only", caller("", "other"), open, false},
		{"nothing reaches itself", caller(dispatchAll), caller(dispatchAll), false},
	}
	for _, c := range cases {
		if got := dispatchReachable(c.caller, c.target); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}

	// A sub-agent is private to its owner: it runs with that parent's authority.
	sub := AgentRecord{ID: "sub", Name: "Sub", OwnedBy: "parent"}
	if dispatchReachable(caller(dispatchAll), sub) {
		t.Error("a stranger reached another agent's sub-agent")
	}
	if !dispatchReachable(AgentRecord{ID: "parent", Name: "Parent"}, sub) {
		t.Error("a parent must reach its own sub-agent")
	}
}

func accessDB(t *testing.T) Database {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	return UserDB(root, "u")
}

// saveAgent validates, so a fixture that ignores the error silently stores
// nothing and the assertions below pass over an empty fleet.
func mustSaveAgent(t *testing.T, udb Database, a AgentRecord) AgentRecord {
	t.Helper()
	a.OrchestratorPrompt = "You are " + a.Name + "."
	saved, err := saveAgent(udb, a)
	if err != nil {
		t.Fatalf("saveAgent(%s): %v", a.Name, err)
	}
	return saved
}

// The reach list is the half nothing computed before: what this agent can hand
// work to, and what that adds.
func TestReachListsTargetsAndWhatTheyAdd(t *testing.T) {
	udb := accessDB(t)
	caller := AgentRecord{ID: "research", Name: "Research", Owner: "u", AllowedTools: []string{"web_search"}}
	mustSaveAgent(t, udb, caller)
	mustSaveAgent(t, udb, AgentRecord{ID: "ops", Name: "Ops", Owner: "u", AllowedTools: []string{"run_shell"}})
	mustSaveAgent(t, udb, AgentRecord{ID: "quiet", Name: "Quiet", Owner: "u", Hidden: true})

	rows := agentReach(udb, "u", caller)
	var names []string
	for _, r := range rows {
		names = append(names, r.Name)
		if r.Name == "Ops" && !strings.Contains(r.Adds, "run_shell") {
			t.Errorf("the row must say what it adds: %+v", r)
		}
	}
	joined := "," + strings.Join(names, ",") + ","
	if !strings.Contains(joined, ",Ops,") {
		t.Errorf("a reachable agent is missing: %v", names)
	}
	if strings.Contains(joined, ",Quiet,") {
		t.Errorf("a hidden agent is not reachable under allow-all: %v", names)
	}
	if strings.Contains(joined, ",Research,") {
		t.Errorf("an agent does not reach itself: %v", names)
	}
}

// A recipe is listed only when it reaches an AGENT. A pipeline of worker stages
// cannot exceed its caller, so listing it would pad the answer with rows that
// grant nothing.
func TestOnlyRecipesThatWidenAreListed(t *testing.T) {
	udb := accessDB(t)
	caller := AgentRecord{ID: "research", Name: "Research", Owner: "u", AllowedTools: []string{"web_search"}}
	mustSaveAgent(t, udb, caller)
	SavePipelineDef(udb, PipelineDef{ID: "p1", Owner: "u", Name: "summarize", Stages: []PipelineStage{
		{Name: "draft", Kind: StageWorker},
	}})
	SavePipelineDef(udb, PipelineDef{ID: "p2", Owner: "u", Name: "delegating", Stages: []PipelineStage{
		{Name: "ask", Kind: StageAgent, Agent: "Ops"},
	}})

	var kinds []string
	for _, r := range agentReach(udb, "u", caller) {
		if r.Kind == "pipeline" {
			kinds = append(kinds, r.Name)
		}
	}
	if len(kinds) != 1 || kinds[0] != "delegating" {
		t.Errorf("only the pipeline that runs an agent should be listed: %v", kinds)
	}
}

// The summary is the answer to "how much", before the tables answer "how".
func TestTheSummaryLeadsWithHowMuch(t *testing.T) {
	full := agentAccessSummary(AgentRecord{Name: "Wide"}, []agentReachRow{{Name: "Ops"}})
	if !strings.Contains(full, "default tool pool") || !strings.Contains(full, "1 other target") {
		t.Errorf("an agent with no allowlist has the MOST access, and the summary must say so: %q", full)
	}
	narrow := agentAccessSummary(AgentRecord{Name: "Narrow", AllowedTools: []string{"web_search"}}, nil)
	if !strings.Contains(narrow, "1 tool(s)") || !strings.Contains(narrow, "hands work to nothing") {
		t.Errorf("a contained agent should read as contained: %q", narrow)
	}
	caps := agentAccessSummary(AgentRecord{Name: "Boss", Fleet: true, Author: true, AllowedTools: []string{"x"}}, nil)
	if !strings.Contains(caps, "conductor") || !strings.Contains(caps, "authoring") {
		t.Errorf("the toolsets that ride outside the allowlist must be named: %q", caps)
	}
	priv := agentAccessSummary(AgentRecord{Name: "Locked", ForcePrivate: true}, nil)
	if !strings.Contains(priv, "network OFF") {
		t.Errorf("the load-bearing containment control belongs in the summary: %q", priv)
	}
}

// The empty table is the dangerous case: rows come from the allowlist, and an
// empty allowlist means the DEFAULT POOL. "No rows" must never read as "no
// access".
func TestAnEmptyToolTableNeverReadsAsNoAccess(t *testing.T) {
	wide := agentToolsEmptyText(AgentRecord{})
	if !strings.Contains(strings.ToUpper(wide), "DEFAULT POOL") {
		t.Errorf("an agent with no allowlist has every read and network tool: %q", wide)
	}
	none := agentToolsEmptyText(AgentRecord{AllowedTools: []string{noToolsSentinel}})
	if !strings.Contains(none, "No tools at all") {
		t.Errorf("and the sentinel is the opposite case: %q", none)
	}
	if strings.EqualFold(wide, none) {
		t.Error("the widest and the narrowest agent must not read the same")
	}
}
