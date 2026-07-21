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
