package orchestrate

import (
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

func TestAutonomousGateSubAgentAuthority(t *testing.T) {
	// A sub-agent runs under the parent's authority — its NeedsConfirm tools
	// auto-approve without a per-sub-agent grant.
	sub := &autonomousGate{subAgent: true}
	if !sub.confirm("fetch_url_x", "") {
		t.Error("sub-agent should auto-approve a tool under the parent's authority")
	}
	// A top-level agent keeps the explicit gate: only a pre-authorized tool runs.
	top := &autonomousGate{subAgent: false, auto: map[string]bool{"granted_tool": true}}
	if !top.confirm("granted_tool", "") {
		t.Error("top-level should allow a pre-authorized tool")
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
	mk("chat", "")      // dispatching parent, bound to the team thread
	mk("research", "")  // top-level specialist — NOT owned by chat
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
