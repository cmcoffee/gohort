package servitor

// Always used to answer exactly as Allow did: nothing was remembered, so the
// same command asked again on every call. It now remembers that exact command
// on that appliance, and nothing wider.

import (
	"context"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestAlwaysRemembersTheCommandOnThatApplianceOnly(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	const cmd = "systemctl restart web"
	remembered := func(appliance string) bool {
		var ok bool
		return udb.Get(alwaysAllowTable, alwaysAllowKey(appliance, cmd), &ok) && ok
	}
	answer := func(sid, who string, allow, always bool) {
		t.Helper()
		ch, card := register(t, sid, "alice", true)
		sessionAppliances.Store(sid, "box-1")
		t.Cleanup(func() { sessionAppliances.Delete(sid) })
		var remember func(sid, cmd string)
		if always {
			remember = func(sid, cmd string) { rememberAlwaysAllow(udb, who, sid, cmd) }
		}
		deliverConfirmWith(who, card, allow, remember)
		select {
		case <-ch:
		default:
		}
	}

	answer("s-allow", "alice", true, false)
	if remembered("box-1") {
		t.Fatal("a plain Allow was remembered")
	}
	answer("s-deny", "alice", false, true)
	if remembered("box-1") {
		t.Fatal("a denial was remembered as always-allow")
	}
	answer("s-other", "bob", true, true)
	if remembered("box-1") {
		t.Fatal("someone else's Always on alice's session was remembered")
	}
	answer("s-always", "alice", true, true)
	if !remembered("box-1") {
		t.Fatal("Always was not remembered")
	}
	if remembered("box-2") {
		t.Fatal("Always on box-1 reached box-2")
	}

	// The gate finds it: the same command on box-1 runs without asking, and
	// on box-2 it still asks (an acting agent's refusal stands in for the
	// card, which would block).
	hits := []risk_hit{{cat: RiskSysControl, reason: "restarts a service"}}
	on := func(appliance string) error {
		ctx := WithActingAgent(context.Background(), "agent-1")
		pr := &probeRun{ctx: ctx, id: "s-gate", appliance: Appliance{ID: appliance}, udb: udb}
		return pr.gateHits(cmd, hits)
	}
	if err := on("box-1"); err != nil {
		t.Fatalf("an always-allowed command asked again: %v", err)
	}
	if err := on("box-2"); err == nil {
		t.Fatal("the command ran on box-2 without asking")
	}
}

// Without a known appliance the answer allows the command once and is not
// remembered anywhere.
func TestAlwaysWithNoKnownApplianceIsNotRemembered(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	rememberAlwaysAllow(udb, "alice", "s-unknown", "ls")
	if keys := udb.Keys(alwaysAllowTable); len(keys) != 0 {
		t.Fatalf("remembered with no appliance: %v", keys)
	}
}

// The Permissions dialog lists each Always answer under its appliance's name,
// leaves out keys from before an answer named its appliance (the gate no
// longer reads them), and a removed one asks again.
func TestAlwaysAnswersAreListedAndRemovable(t *testing.T) {
	T := &Servitor{}
	T.DB = &DBase{Store: kvlite.MemStore()}
	udb := &DBase{Store: kvlite.MemStore()}
	udb.Set(applianceTable, "box-1", Appliance{ID: "box-1", Name: "web-01"})
	udb.Set(alwaysAllowTable, alwaysAllowKey("box-1", "systemctl restart web"), true)
	udb.Set(alwaysAllowTable, alwaysAllowKey("gone-9", "ls /srv"), true)
	udb.Set(alwaysAllowTable, "legacy command", true)

	got := T.listAlwaysAllowed("alice", udb)
	if len(got) != 2 || got[0].Appliance != "gone-9" || got[1].Appliance != "web-01" || got[1].Command != "systemctl restart web" {
		t.Fatalf("listed %+v", got)
	}
	udb.Unset(alwaysAllowTable, alwaysAllowKey(got[1].ApplianceID, got[1].Command))
	if got := T.listAlwaysAllowed("alice", udb); len(got) != 1 || got[0].ApplianceID != "gone-9" {
		t.Fatalf("after removing: %+v", got)
	}
}
