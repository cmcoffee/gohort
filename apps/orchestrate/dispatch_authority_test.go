package orchestrate

import (
	"testing"
)

func agentFixture(id, name string) AgentRecord {
	return AgentRecord{ID: id, Name: name, Owner: "alice"}
}

// TestDispatchAuthorityAllows pins the policy semantics the transitive check
// leans on. dispatchAll must stay permissive: an unrestricted originator has to
// constrain nothing, or every existing chain breaks.
func TestDispatchAuthorityAllows(t *testing.T) {
	osint := agentFixture("osint", "OSINT Investigator")

	cases := []struct {
		name string
		auth dispatchAuthority
		want bool
	}{
		{"none blocks everything", dispatchAuthority{Mode: dispatchNone}, false},
		{"only + listed", dispatchAuthority{Mode: dispatchOnly, Targets: []string{"osint"}}, true},
		{"only + unlisted", dispatchAuthority{Mode: dispatchOnly, Targets: []string{"other"}}, false},
		{"only + empty list reaches nothing", dispatchAuthority{Mode: dispatchOnly}, false},
		{"except + listed is blocked", dispatchAuthority{Mode: dispatchExcept, Targets: []string{"osint"}}, false},
		{"except + unlisted is allowed", dispatchAuthority{Mode: dispatchExcept, Targets: []string{"other"}}, true},
		{"all allows", dispatchAuthority{Mode: dispatchAll}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.auth.allows(osint); got != tc.want {
				t.Fatalf("allows() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestOriginAuthorityRootSnapshotsSelf — a root turn IS the origin, so it
// snapshots its own policy; that's what gets stamped onto the first hop.
func TestOriginAuthorityRootSnapshotsSelf(t *testing.T) {
	caller := agentFixture("moltbook", "Moltbook Agent")
	caller.DispatchMode = dispatchOnly
	caller.AllowedDispatchTargets = []string{"helper"}
	turn := &chatTurn{agent: caller}

	got := turn.originAuthority()
	if got.AgentID != "moltbook" || got.Mode != dispatchOnly || len(got.Targets) != 1 || got.Targets[0] != "helper" {
		t.Fatalf("root turn should snapshot its own authority; got %+v", got)
	}
}

// TestOriginAuthorityInheritsUnchanged — the crux. A sub-run must report the
// ORIGINATOR's authority, not its own, or the chain re-widens at every hop and
// the whole mechanism is decorative.
func TestOriginAuthorityInheritsUnchanged(t *testing.T) {
	origin := &dispatchAuthority{AgentID: "moltbook", AgentName: "Moltbook Agent", Mode: dispatchOnly, Targets: []string{"helper"}}
	// A sub-run whose OWN policy is wide open.
	wideOpen := agentFixture("helper", "Helper")
	wideOpen.DispatchMode = dispatchAll
	sub := &chatTurn{agent: wideOpen, dispatchOrigin: origin}

	got := sub.originAuthority()
	if got.Mode != dispatchOnly || got.AgentID != "moltbook" {
		t.Fatalf("sub-run must inherit the originator's authority unchanged, not adopt its own; got %+v", got)
	}
	if got.allows(agentFixture("osint", "OSINT Investigator")) {
		t.Fatal("inherited authority must still refuse an agent the originator could not reach")
	}
}

// TestDispatchAuthoritySnapshotIsIsolated — the authority is a snapshot, so a
// later edit to the originator's record cannot widen a chain already running.
func TestDispatchAuthoritySnapshotIsIsolated(t *testing.T) {
	caller := agentFixture("moltbook", "Moltbook Agent")
	caller.DispatchMode = dispatchOnly
	caller.AllowedDispatchTargets = []string{"helper"}
	turn := &chatTurn{agent: caller}

	auth := turn.originAuthority()
	// Mutate the record's slice the way an in-place edit would.
	caller.AllowedDispatchTargets = append(caller.AllowedDispatchTargets, "osint")
	turn.agent = caller

	if auth.allows(agentFixture("osint", "OSINT Investigator")) {
		t.Fatal("snapshot aliased the record's slice — a mid-chain edit could widen a running chain")
	}
}

// TestTransitiveEscalationIsBlocked walks the actual attack: Moltbook may reach
// only Helper; Helper may reach OSINT. Before this change every check ran
// against the immediate caller, so Helper's own wide policy let the chain reach
// OSINT and Moltbook's allow-list meant nothing past one hop.
func TestTransitiveEscalationIsBlocked(t *testing.T) {
	moltbook := agentFixture("moltbook", "Moltbook Agent")
	moltbook.DispatchMode = dispatchOnly
	moltbook.AllowedDispatchTargets = []string{"helper"}

	helper := agentFixture("helper", "Helper")
	helper.DispatchMode = dispatchAll // helper itself is unrestricted

	osint := agentFixture("osint", "OSINT Investigator")

	// Hop 1: Moltbook → Helper. Root turn stamps the origin onto the sub-turn.
	root := &chatTurn{agent: moltbook}
	subTurn := &chatTurn{agent: helper, dispatchOrigin: root.originAuthority()}

	// Hop 2: Helper → OSINT. Helper's OWN policy permits it...
	helperOwn := dispatchAuthority{Mode: effectiveDispatchMode(helper), Targets: helper.AllowedDispatchTargets}
	if !helperOwn.allows(osint) {
		t.Fatal("precondition: helper's own policy should permit osint")
	}
	// ...but the originator's does not, and that is what must govern.
	if subTurn.dispatchOrigin.allows(osint) {
		t.Fatal("ESCALATION: a chain rooted at Moltbook reached OSINT, which Moltbook could not reach directly")
	}

	// Hop 2 to an agent the ORIGINATOR did allow still works.
	if !subTurn.dispatchOrigin.allows(agentFixture("helper", "Helper")) {
		t.Fatal("originator-permitted target was wrongly blocked — the chain must narrow, not seize up")
	}
}

// TestOwnedSubAgentExemptionShape documents the exemption the check applies:
// dispatching to your OWN sub-agent is internal composition, not lateral
// movement, so it is not bound by the originator's list. Without it, a
// dispatched specialist could not call its own sub-agents and would be inert.
func TestOwnedSubAgentExemptionShape(t *testing.T) {
	osint := agentFixture("osint", "OSINT Investigator")
	subAgent := agentFixture("social", "Social Media")
	subAgent.OwnedBy = "osint"
	subAgent.Hidden = true

	// The originator could never reach the sub-agent directly.
	origin := &dispatchAuthority{AgentID: "moltbook", AgentName: "Moltbook Agent", Mode: dispatchOnly, Targets: []string{"osint"}}
	if origin.allows(subAgent) {
		t.Fatal("precondition: the originator should not be able to reach the sub-agent directly")
	}
	// The exemption is keyed on ownership by the CALLER, which is what lets the
	// specialist keep working (mirrors the guard in agentsRunAction).
	turn := &chatTurn{agent: osint, dispatchOrigin: origin}
	if subAgent.OwnedBy != turn.agent.ID {
		t.Fatal("exemption must key on the sub-agent being owned by the dispatching caller")
	}
}
