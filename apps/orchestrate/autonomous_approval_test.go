package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A sub-agent inherits its parent's autonomous tool authorizations down the
// OwnedBy chain, so the owner approves once at the parent instead of per child.
func TestAutonomousApprovedSetInherits(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	mk := func(id, name, ownedBy string, tools []string) {
		if _, err := saveAgent(db, AgentRecord{ID: id, Owner: "u", Name: name, OrchestratorPrompt: "p", OwnedBy: ownedBy, AutoApproveTools: tools}); err != nil {
			t.Fatalf("saveAgent %s: %v", id, err)
		}
	}
	mk("parent", "Parent", "", []string{"fetch_url_x", "message_contact"})
	mk("sub", "Sub", "parent", nil)
	mk("grand", "Grand", "sub", []string{"own_tool"})
	mk("solo", "Solo", "", nil)

	sub := autonomousApprovedSet(db, "sub")
	if !sub["fetch_url_x"] || !sub["message_contact"] {
		t.Errorf("sub-agent did not inherit parent's tools: %v", sub)
	}
	grand := autonomousApprovedSet(db, "grand")
	if !grand["fetch_url_x"] || !grand["own_tool"] {
		t.Errorf("grandchild did not union the whole chain: %v", grand)
	}
	if len(autonomousApprovedSet(db, "solo")) != 0 {
		t.Errorf("top-level agent with no grants should be empty, got %v", autonomousApprovedSet(db, "solo"))
	}
}

func TestRemoveAutoApproveTool(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	if _, err := saveAgent(db, AgentRecord{ID: "p", Owner: "u", Name: "P", OrchestratorPrompt: "x", AutoApproveTools: []string{"a", "b", "c"}}); err != nil {
		t.Fatal(err)
	}
	removeAutoApproveTool(db, "p", "b")
	rec, _ := loadAgent(db, "p")
	if len(rec.AutoApproveTools) != 2 || rec.AutoApproveTools[0] != "a" || rec.AutoApproveTools[1] != "c" {
		t.Errorf("after revoke: %v, want [a c]", rec.AutoApproveTools)
	}
	// Removing a non-existent tool is a no-op (no save, no panic).
	removeAutoApproveTool(db, "p", "zzz")
	removeAutoApproveTool(db, "missing", "a")
}

// unattendedGateFixture is an owner with a credential that asks before every call, a
// parent agent and its sub-agent, and the stores a refusal is queued in.
func unattendedGateFixture(t *testing.T) (*OrchestrateApp, Database, *ToolSession) {
	t.Helper()
	store := &DBase{Store: kvlite.MemStore()}
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return store }
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })
	if err := Secure().Save(SecureCredential{Name: "bank", Type: SecureCredBearer, BaseURL: "https://bank.example", RequiresConfirm: true}, "k"); err != nil {
		t.Fatal(err)
	}
	app := &OrchestrateApp{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	udb := UserDB(app.DB, "alice")
	for _, a := range []AgentRecord{
		{ID: "p", Owner: "alice", Name: "Parent", OrchestratorPrompt: "p"},
		{ID: "c", Owner: "alice", Name: "Child", OrchestratorPrompt: "c", OwnedBy: "p"},
	} {
		if _, err := saveAgent(udb, a); err != nil {
			t.Fatal(err)
		}
	}
	sess := &ToolSession{Username: "alice"}
	sess.TempTools = []*TempTool{{Name: "asks", Mode: TempToolModeShell, ConfirmInChat: true}, {Name: "plain", Mode: TempToolModeShell}}
	return app, udb, sess
}

// autonomousQueuedFor is the autonomous-tool approvals waiting on alice for agentID.
func autonomousQueuedFor(agentID string) []string {
	var out []string
	for _, a := range ListAuthorizations(RootDB, "alice") {
		if a.Action == "autonomous_tool" && a.Agent == agentID {
			out = append(out, a.Brief)
		}
	}
	return out
}

// A sub-agent is held to the gate as its parent is. It used to pass every
// check but the never-unattended mark on its parent's authority, and a model
// can create a sub-agent, so that was how to run a credential that asks
// before every call, or a tool the owner asked to be asked about, with nobody
// asked. What the parent was granted, the child may do.
func TestASubAgentIsHeldToTheGate(t *testing.T) {
	app, udb, sess := unattendedGateFixture(t)
	g := app.newAutonomousGate("alice", "c", sess)
	if g.confirm("fetch_url_bank", "url: https://bank.example/pay") {
		t.Error("a sub-agent ran a call its credential asks about, with nobody asked")
	}
	if g.confirm("asks", "") {
		t.Error("a sub-agent ran a tool that asks first, with nobody asked")
	}
	if !g.confirm("plain", "") {
		t.Error("an ordinary tool was stopped: the default is still allow")
	}
	if q := autonomousQueuedFor("c"); len(q) != 2 {
		t.Errorf("the refusals were not queued for the owner: %v", q)
	}

	// Granted to the parent, they run for the child.
	parent, _ := loadAgent(udb, "p")
	parent.AutoApproveTools = []string{"fetch_url_bank", "asks"}
	if _, err := saveAgent(udb, parent); err != nil {
		t.Fatal(err)
	}
	g = app.newAutonomousGate("alice", "c", sess)
	if !g.confirm("fetch_url_bank", "url: https://bank.example/pay") || !g.confirm("asks", "") {
		t.Error("a grant on the parent did not reach its sub-agent")
	}
}

// A run with no agent behind it (a scheduled machine) refuses what asks
// first and queues nothing: an approval would be a grant on no agent, which
// approving could not turn into anything.
func TestARunWithNoAgentRefusesWithoutQueueing(t *testing.T) {
	app, _, sess := unattendedGateFixture(t)
	g := app.newAutonomousGate("alice", "", sess)
	if g.confirm("fetch_url_bank", "url: https://bank.example/pay") {
		t.Error("an agentless run ran a call its credential asks about")
	}
	if q := autonomousQueuedFor(""); len(q) != 0 {
		t.Errorf("an approval nothing could grant was queued: %v", q)
	}
	if g.blocked() != "fetch_url_bank" {
		t.Errorf("the refusal left no trace for the run's record: %q", g.blocked())
	}
}

// On an unattended run the turn sends a never-unattended tool to the gate,
// whether or not the tool asks on its own; with a person there it does not.
func TestTheNeverUnattendedMarkReachesTheGate(t *testing.T) {
	app, udb, _ := unattendedGateFixture(t)
	rec, _ := loadAgent(udb, "p")
	rec.NoUnattendedTools = []string{"web_search"}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	g := app.newAutonomousGate("alice", "c", nil)
	turn := &chatTurn{user: "alice", agent: AgentRecord{ID: "c"}}
	if turn.asksFirst("web_search") {
		t.Fatal("a turn with a person there asked about an unmarked-for-chat tool")
	}
	turn.neverUnattended = g.neverUnattended
	if !turn.asksFirst("web_search") {
		t.Error("an unattended turn let a never-unattended tool past the gate")
	}
	if turn.asksFirst("fetch_url") {
		t.Error("an unmarked tool was sent to the gate")
	}
	if g.confirm("web_search", "") {
		t.Error("the gate ran a tool marked never unattended")
	}
}

func TestChannelsAccessibleToAgent(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	savedRoot := RootDB
	RootDB = db // channelsAccessibleToAgent reads channels from RootDB
	t.Cleanup(func() { RootDB = savedRoot })

	mk := func(id, ownedBy string) {
		if _, err := saveAgent(db, AgentRecord{ID: id, Owner: "u", Name: id, OrchestratorPrompt: "x", OwnedBy: ownedBy}); err != nil {
			t.Fatal(err)
		}
	}
	mk("parent", "")
	mk("sub", "parent")
	mk("other", "")

	SaveChannel(db, Channel{ID: "c1", Owner: "u", AgentID: "parent", Service: "imessage", Address: "chatP"})
	SaveChannel(db, Channel{ID: "c2", Owner: "u", AgentID: "other", Service: "imessage", Address: "chatG", AuthorizedSenders: []string{"sub"}})
	SaveChannel(db, Channel{ID: "c3", Owner: "u", AgentID: "other", Service: "imessage", Address: "chatO"})

	ids := func(chs []Channel) map[string]bool {
		m := map[string]bool{}
		for _, c := range chs {
			m[c.ID] = true
		}
		return m
	}

	// Parent sees only its own bound channel.
	if got := ids(channelsAccessibleToAgent(db, "u", "parent")); !got["c1"] || len(got) != 1 {
		t.Errorf("parent access = %v, want {c1}", got)
	}
	// Sub inherits the parent's bound channel (c1) AND its explicit grant (c2); not c3.
	sub := ids(channelsAccessibleToAgent(db, "u", "sub"))
	if !sub["c1"] || !sub["c2"] || sub["c3"] || len(sub) != 2 {
		t.Errorf("sub access = %v, want {c1 inherited, c2 granted}", sub)
	}
	// Unrelated agent sees only its own channels.
	if got := ids(channelsAccessibleToAgent(db, "u", "other")); !got["c2"] || !got["c3"] || got["c1"] {
		t.Errorf("other access = %v, want {c2,c3}", got)
	}
}

// TestChannelsAccessibleViaDispatchChain covers the dispatch-chain inheritance:
// an agent handed a task by a parent can reach the parent's channels, so a
// specialist asked to "post the summary to the team thread" addresses it
// directly instead of handing text back up to be relayed.
func TestChannelsAccessibleViaDispatchChain(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	savedRoot := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = savedRoot })

	mk := func(id, ownedBy string) {
		if _, err := saveAgent(db, AgentRecord{ID: id, Owner: "u", Name: id, OrchestratorPrompt: "x", OwnedBy: ownedBy}); err != nil {
			t.Fatal(err)
		}
	}
	mk("chat", "")     // dispatching parent, bound to the team thread
	mk("research", "") // top-level specialist — NOT owned by chat
	mk("helper", "research")
	mk("stranger", "")

	SaveChannel(db, Channel{ID: "team", Owner: "u", AgentID: "chat", Service: "imessage", Address: "chatTeam"})
	SaveChannel(db, Channel{ID: "priv", Owner: "u", AgentID: "stranger", Service: "imessage", Address: "chatPriv"})

	ids := func(chs []Channel) map[string]bool {
		m := map[string]bool{}
		for _, c := range chs {
			m[c.ID] = true
		}
		return m
	}

	// Baseline: with no dispatch chain, the specialist sees nothing — it is not
	// owned by chat, so the OwnedBy walk alone never reaches the team channel.
	if got := ids(channelsAccessibleToAgent(db, "u", "research")); len(got) != 0 {
		t.Fatalf("undispatched specialist access = %v, want none", got)
	}
	// Dispatched BY chat: it can now reach chat's channel.
	got := ids(channelsAccessibleToAgent(db, "u", "research", "chat"))
	if !got["team"] || got["priv"] || len(got) != 1 {
		t.Fatalf("dispatched specialist access = %v, want {team}", got)
	}
	// Two hops (chat → research → helper): the chain carries, and still only
	// covers what the dispatchers themselves could reach.
	deep := ids(channelsAccessibleToAgent(db, "u", "helper", "chat", "research"))
	if !deep["team"] || deep["priv"] || len(deep) != 1 {
		t.Fatalf("two-hop access = %v, want {team}", deep)
	}
	// A dispatch never reaches an unrelated agent's channel.
	if got := ids(channelsAccessibleToAgent(db, "u", "research", "stranger")); !got["priv"] || got["team"] {
		t.Fatalf("chain must grant exactly the dispatcher's channels, got %v", got)
	}
}

// TestDispatchChainDoesNotGrantSendBypass pins the deliberate asymmetry:
// visibility follows the dispatch chain, the proactive-send bypass does not.
// A runtime edge grants reach, not standing authority to message people
// unprompted — a dispatched agent's proactive send still queues for approval.
func TestDispatchChainDoesNotGrantSendBypass(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	savedRoot := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = savedRoot })

	for _, id := range []string{"chat", "research"} {
		if _, err := saveAgent(db, AgentRecord{ID: id, Owner: "u", Name: id, OrchestratorPrompt: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	SaveChannel(db, Channel{ID: "team", Owner: "u", AgentID: "chat", Service: "imessage", Address: "chatTeam"})

	// The specialist can SEE the channel when dispatched by chat...
	if got := channelsAccessibleToAgent(db, "u", "research", "chat"); len(got) != 1 {
		t.Fatalf("dispatched agent should see the channel, got %v", got)
	}
	// ...but does not hold the send bypass: its proactive send queues.
	if channelSenderAuthorized(db, "u", "chatTeam", "", "research") {
		t.Fatal("a dispatch edge must not confer the proactive-send bypass")
	}
}

// TestInheritedChannelChainRespectsAllowList: the channel tools are force-added
// after the allow-list resolves, so a delegation must not hand messaging tools
// to an agent that never had any. A delegation widens what an agent can REACH,
// never what it can DO.
func TestInheritedChannelChainRespectsAllowList(t *testing.T) {
	chain := []string{"chat"}

	// No explicit allow-list = everything: takes the inherited channels.
	if got := inheritedChannelChain(AgentRecord{}, chain); len(got) != 1 {
		t.Errorf("open allow-list should inherit, got %v", got)
	}
	// Curated list naming a channel tool: takes them.
	withMsg := AgentRecord{AllowedTools: []string{"web_search", "send_message"}}
	if got := inheritedChannelChain(withMsg, chain); len(got) != 1 {
		t.Errorf("allow-list naming send_message should inherit, got %v", got)
	}
	// A retired alias still resolves (read_phantom_chat -> read_chat).
	alias := AgentRecord{AllowedTools: []string{"read_phantom_chat"}}
	if got := inheritedChannelChain(alias, chain); len(got) != 1 {
		t.Errorf("renamed channel tool should still count, got %v", got)
	}
	// Curated list with NO channel tool: no inherited channels.
	noMsg := AgentRecord{AllowedTools: []string{"web_search", "calculate"}}
	if got := inheritedChannelChain(noMsg, chain); got != nil {
		t.Errorf("curated non-messaging agent must not inherit, got %v", got)
	}
	// The no-tools sentinel inherits nothing.
	none := AgentRecord{AllowedTools: []string{noToolsSentinel}}
	if got := inheritedChannelChain(none, chain); got != nil {
		t.Errorf("no-tools agent must not inherit, got %v", got)
	}
	// Nothing to inherit stays nothing, whatever the allow-list says.
	if got := inheritedChannelChain(noMsg, nil); got != nil {
		t.Errorf("empty chain stays empty, got %v", got)
	}
}

// gateFor builds a gate with no live session — the standing-fire shape, which
// is the one that must not quietly allow everything.
func gateFor(app *OrchestrateApp, owner, agentID string) *autonomousGate {
	return app.newAutonomousGate(owner, agentID, nil)
}

// The asymmetry this closes: the interactive policy (confirmFuncFor) escalates
// ONLY on a credential whose "require confirm" toggle asks for it, while the
// unattended gate refused anything absent from AutoApproveTools. So a tool the
// owner had attached to an agent ran fine in chat and was refused the moment the
// same agent ran on a timer — nothing about the tool changed, only whether
// someone was watching.
func TestUncredentialedToolRunsUnattended(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{}
	app.DB = db
	if _, err := saveAgent(db, AgentRecord{ID: "a", Owner: "u", Name: "A", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	g := gateFor(app, "u", "a")

	// A tool with no credential has nothing configured to ask, so it runs.
	if !g.confirm("get_top_stories", "{}") {
		t.Error("a tool on the agent with no confirm requirement must run unattended")
	}
	if len(g.queued) != 0 {
		t.Errorf("nothing should have been queued for approval, got %v", g.queued)
	}
}

// A pre-authorized tool still runs, and a sub-agent still runs under its
// parent's authority — the two existing bypasses must survive the change.
func TestExistingBypassesStillHold(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{}
	app.DB = db
	if _, err := saveAgent(db, AgentRecord{ID: "p", Owner: "u", Name: "P", OrchestratorPrompt: "x", AutoApproveTools: []string{"spend"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := saveAgent(db, AgentRecord{ID: "s", Owner: "u", Name: "S", OrchestratorPrompt: "x", OwnedBy: "p"}); err != nil {
		t.Fatal(err)
	}
	if !gateFor(app, "u", "p").confirm("spend", "{}") {
		t.Error("a pre-authorized tool must still run")
	}
	if !gateFor(app, "u", "s").confirm("anything", "{}") {
		t.Error("a sub-agent must still run under its parent's authority")
	}
}

// Every door a run nobody is watching comes in by marks its context, so what
// it starts (a pipeline's stages, a machine's steps, a hand-off) is held to
// the owner's gate. Each of these is otherwise a run under which RunAgentSync
// approves every call.
func TestEveryUnattendedDoorMarksItsContext(t *testing.T) {
	for _, c := range []struct{ file, header string }{
		{"standing_runner.go", "func (T *OrchestrateApp) runStanding("},
		{"scheduled_updates.go", "func fireOrchestrateUpdate("},
		{"agent_dispatch.go", "func (T *OrchestrateApp) RunAgentSyncContinuingRich("},
	} {
		if !strings.Contains(funcBody(t, readFile(t, c.file), c.header), "withUnattended(") {
			t.Errorf("%s: %s does not mark its run unattended", c.file, c.header)
		}
	}
	poll := readFile(t, "operator_wake.go")
	i := strings.Index(poll, "RegisterEventPoller(func(")
	if i < 0 || !strings.Contains(poll[i:i+600], "withUnattended(") {
		t.Error("the poll checker does not mark its run unattended")
	}
	// And the recurring fire's loop consults the owner's ask marks, which it
	// did not, so an ask mark on a tool that asks nothing of its own meant
	// nothing on a schedule.
	if !strings.Contains(funcBody(t, readFile(t, "scheduled_updates.go"), "func (fr *fireRun) runLoop("), "AskFirst:") {
		t.Error("the recurring fire's loop has no AskFirst")
	}
}
