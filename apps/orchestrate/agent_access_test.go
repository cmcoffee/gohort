package orchestrate

import (
	"os"
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
	builder := AgentRecord{ID: "seed-builder", Name: "Builder", Hidden: true}
	granted := func(mode string) AgentRecord {
		c := caller(mode)
		c.AllowBuilderDispatch = true
		return c
	}
	fleet := func(mode string) AgentRecord {
		c := caller(mode)
		c.Fleet = true
		return c
	}

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
		// Builder answers to its grant, not the policy switch (it is Hidden by
		// seed posture, which the switch would read as unreachable).
		{"a granted caller reaches Builder", granted(dispatchAll), builder, true},
		{"the grant holds under Allow none", granted(dispatchNone), builder, true},
		{"a Fleet caller reaches Builder", fleet(dispatchAll), builder, true},
		{"Fleet alone stops at Allow none", fleet(dispatchNone), builder, false},
		{"a plain caller does not reach Builder", caller(dispatchAll), builder, false},
		{"the grant reopens nothing else under Allow none", granted(dispatchNone), open, false},
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
	// Allow-none is ABSOLUTE, own sub-agents included. The gate has always
	// refused this - the observed failure was a dispatch-disabled agent
	// dispatching its own sub-agent a hundred times in one autonomous turn -
	// and the predicate had the ownership bypass without the guard in front of
	// it, so the listing said reachable about a call that never happens.
	shutParent := AgentRecord{ID: "parent", Name: "Parent", DispatchMode: dispatchNone}
	if dispatchReachable(shutParent, sub) {
		t.Error("an agent with Allow none reached its own sub-agent, which the gate refuses")
	}
}

// The reach list answers what an agent can ACTUALLY hand work to, so a target
// the user has Blocked is not in it. Policy reach alone overstates, and on the
// page built to answer that question overstating is the direction that matters.
func TestTheReachListDropsABlockedTarget(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	caller := AgentRecord{ID: "caller", Name: "Caller", Owner: "alice", OrchestratorPrompt: "p"}
	for _, rec := range []AgentRecord{
		caller,
		{ID: "reachable", Name: "Reachable", Owner: "alice", OrchestratorPrompt: "p"},
		{ID: "blocked", Name: "Blocked One", Owner: "alice", OrchestratorPrompt: "p"},
	} {
		if _, err := saveAgent(udb, rec); err != nil {
			t.Fatal(err)
		}
	}
	named := func() map[string]bool {
		got := map[string]bool{}
		for _, r := range agentReach(udb, "alice", caller) {
			got[r.Name] = true
		}
		return got
	}
	if got := named(); !got["Reachable"] || !got["Blocked One"] {
		t.Fatalf("both agents should start reachable: %v", got)
	}
	SetDelegationPolicy(RootDB, "alice", caller.ID, "Blocked One", PolicyBlock)
	got := named()
	if got["Blocked One"] {
		t.Error("a BLOCKED target is still listed as something this agent can reach")
	}
	if !got["Reachable"] {
		t.Error("blocking one target took the others with it")
	}
}

// ...but the predicate itself must NOT read the Block, because the permissions
// page asks it before recording a decision and a Block is a decision recorded
// there. A control that refuses to undo what it just did is the trap this
// codebase has walked into three times.
func TestTheBlockCanStillBeUndone(t *testing.T) {
	pinRootDB(t)
	caller := AgentRecord{ID: "caller", Name: "Caller", Owner: "alice"}
	target := AgentRecord{ID: "target", Name: "Target", Owner: "alice"}
	SetDelegationPolicy(RootDB, "alice", caller.ID, target.Name, PolicyBlock)
	if !dispatchReachable(caller, target) {
		t.Error("the predicate reads the Block, so the control that set it cannot clear it")
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

// The agent being CALLED gets a say. Every other reachability rule is
// expressed from the caller's side, so "only these two may call me" could
// previously only be arranged by visiting every other agent in the fleet and
// excluding this one - which nobody does, and nothing checks held.
func TestAnAgentDecidesWhoMayCallIt(t *testing.T) {
	open := AgentRecord{ID: "target", Name: "Target"}
	caller := AgentRecord{ID: "caller", Name: "Caller", DispatchMode: string(dispatchAll)}
	other := AgentRecord{ID: "other", Name: "Other", DispatchMode: string(dispatchAll)}

	if !dispatchReachable(caller, open) {
		t.Fatal("setup: an open agent should be reachable")
	}
	// none is absolute, and the caller being wide open does not widen it.
	shut := open
	shut.InboundMode = inboundNone
	if dispatchReachable(caller, shut) {
		t.Error("an agent accepting nothing was reachable by an allow-all caller")
	}
	// only, with a list.
	picky := open
	picky.InboundMode, picky.AllowedCallers = inboundOnly, []string{"caller"}
	if !dispatchReachable(caller, picky) {
		t.Error("a listed caller was refused")
	}
	if dispatchReachable(other, picky) {
		t.Error("an unlisted caller got through")
	}
	// only with an EMPTY list means nothing reaches it, not "open by
	// accident": a mode that silently fell back to open is the dangerous
	// direction for a control whose whole point is narrowing.
	empty := open
	empty.InboundMode = inboundOnly
	if dispatchReachable(caller, empty) {
		t.Error("an empty caller list read as open")
	}
}

// A sub-agent's parent is exempt. Ownership IS the link: a sub-agent runs with
// its parent's authority and exists to be called by it, and an inbound rule
// that locked the parent out would leave the child unreachable by anything.
func TestAnInboundRuleDoesNotStrandASubAgent(t *testing.T) {
	parent := AgentRecord{ID: "parent", Name: "Parent"}
	child := AgentRecord{ID: "child", Name: "Child", OwnedBy: "parent", InboundMode: inboundNone}
	if !dispatchReachable(parent, child) {
		t.Error("a parent was locked out of its own sub-agent, which nothing else can reach")
	}
	// And it does not make the child public by the same token.
	stranger := AgentRecord{ID: "stranger", Name: "Stranger", DispatchMode: string(dispatchAll)}
	if dispatchReachable(stranger, child) {
		t.Error("the parent exemption let somebody else in")
	}
}

// The gate and the predicate are mirrors, and the gate has to refuse with
// words the model can act on: naming the agent that refused, and who can
// change it. A refusal the caller thinks it can fix is one it routes around.
func TestTheInboundRefusalTellsTheModelToStop(t *testing.T) {
	shut := AgentRecord{ID: "target", Name: "Target", InboundMode: inboundNone}
	msg := inboundRefusal(shut)
	for _, want := range []string{"Target", "no dispatches", "nothing on this side changes it"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal is missing %q: %s", want, msg)
		}
	}
	picky := AgentRecord{ID: "target", Name: "Target", InboundMode: inboundOnly}
	msg2 := inboundRefusal(picky)
	if !strings.Contains(msg2, "caller list") || !strings.Contains(msg2, "Do not retry") {
		t.Errorf("the only-mode refusal does not tell it to stop: %s", msg2)
	}
	// EVERY route that resolves an agent and dispatches to it consults the
	// target's answer, or the restriction governs whichever surface thought to
	// ask. agents(run) did; a machine's delegating step and a pipeline's agent
	// stage resolved a target by name and went straight to it, so authoring
	// either was a way around an agent that accepts no inbound dispatch - and
	// Builder can author both.
	for _, file := range []string{
		"agents_grouped_tool.go", // agents(run)
		"machine_host.go",        // a machine's delegating step
		"agent_dispatch_pipeline.go", // a pipeline's agent stage
	} {
		if !strings.Contains(mustReadOrch(t, file), "targetAcceptsDispatch(") {
			t.Errorf("%s dispatches without asking what the target accepts, so the restriction is only enforced where somebody remembered", file)
		}
	}
}

func mustReadOrch(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
