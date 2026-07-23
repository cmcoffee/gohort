package core

import (
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// A global credential and a user-owned credential of the SAME NAME must keep
// separate audit ledgers. Before the owner-aware keying, both wrote to the bare
// name, so one user's dispatch history showed another namespace's calls and the
// daily-cap counter counted across users.
func TestAuditRingIsPerOwner(t *testing.T) {
	s := &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}

	s.recordAudit(SecureAPIAuditEntry{CredentialName: "vapi", Owner: "", URL: "https://global", Status: 200})
	s.recordAudit(SecureAPIAuditEntry{CredentialName: "vapi", Owner: "alice", URL: "https://alice", Status: 200})
	s.recordAudit(SecureAPIAuditEntry{CredentialName: "vapi", Owner: "alice", URL: "https://alice2", Status: 200})

	global := s.LoadAudit("", "vapi")
	if len(global) != 1 || global[0].URL != "https://global" {
		t.Fatalf("global ledger leaked or lost entries: %+v", global)
	}
	alice := s.LoadAudit("alice", "vapi")
	if len(alice) != 2 {
		t.Fatalf("alice's ledger should hold exactly her 2 calls, got %d", len(alice))
	}
	for _, e := range alice {
		if e.URL == "https://global" {
			t.Error("alice's ledger contains a global-namespace call — cross-user leak")
		}
	}
	// The global read must not see alice's calls either.
	for _, e := range global {
		if e.Owner == "alice" {
			t.Error("global ledger contains alice's call — cross-user leak")
		}
	}
}

// Global credentials (Owner "") keep the bare-name key, so ledgers recorded
// before the owner-aware change remain readable — no history is orphaned.
func TestAuditGlobalKeyUnchanged(t *testing.T) {
	s := &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}
	// Simulate a pre-change entry written under the bare name.
	s.db.Set(secureAPIAuditTable, "legacy", []SecureAPIAuditEntry{{CredentialName: "legacy", URL: "https://old"}})
	got := s.LoadAudit("", "legacy")
	if len(got) != 1 || got[0].URL != "https://old" {
		t.Fatalf("pre-change global ledger not readable: %+v", got)
	}
}
