package shareledger

// The registry's whole job is to know nothing. These pin that: rows come back
// stamped with the kind they were registered under, a kind that offers no view
// on one side is distinguishable from one with nothing to show, and a revoke
// reaches the kind that owns the record or says plainly that it cannot.

import (
	"strings"
	"testing"
)

func reset(t *testing.T) {
	t.Helper()
	mu.Lock()
	saved := providers
	providers = map[string]Provider{}
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		providers = saved
		mu.Unlock()
	})
}

func TestBothViewsCollectEveryRegisteredKind(t *testing.T) {
	reset(t)
	Register("skill", Provider{
		Label: "Skill",
		Mine:  func(owner string) []Grant { return []Grant{{ID: "s1", Name: "Triage"}} },
		ToMe:  func(user string) []Grant { return []Grant{{ID: "s2", Name: "Handed over"}} },
	})
	Register("agent", Provider{
		Label: "Agent",
		Mine:  func(owner string) []Grant { return []Grant{{ID: "a1", Name: "Wren"}} },
	})

	mine := Mine("alice")
	if len(mine) != 2 {
		t.Fatalf("mine = %+v", mine)
	}
	// Sorted by kind label, so two kinds never interleave and the same list
	// reads the same twice.
	if mine[0].Label != "Agent" || mine[1].Label != "Skill" {
		t.Errorf("not grouped by kind: %+v", mine)
	}
	// The REGISTRY stamps kind and label. A provider that disagreed with the
	// key it registered under would produce a row no revoke could route.
	if mine[0].Kind != "agent" || mine[1].Kind != "skill" {
		t.Errorf("rows are not stamped with their kind: %+v", mine)
	}
	// A kind with no recipient view contributes nothing there, rather than an
	// empty row.
	if got := ToMe("bob"); len(got) != 1 || got[0].Name != "Handed over" {
		t.Errorf("to-me = %+v", got)
	}
}

// A provider that lies about its own key cannot misroute a revoke.
func TestAProviderCannotRenameItsOwnRows(t *testing.T) {
	reset(t)
	Register("skill", Provider{
		Label: "Skill",
		Mine:  func(string) []Grant { return []Grant{{Kind: "agent", Label: "Agent", ID: "s1"}} },
	})
	got := Mine("alice")
	if len(got) != 1 || got[0].Kind != "skill" || got[0].Label != "Skill" {
		t.Errorf("a provider overrode its registration: %+v", got)
	}
}

func TestRevokeReachesTheKindThatOwnsTheRecord(t *testing.T) {
	reset(t)
	var gotOwner, gotID, gotRecipient string
	Register("skill", Provider{
		Label: "Skill",
		Revoke: func(owner, id, recipient string) error {
			gotOwner, gotID, gotRecipient = owner, id, recipient
			return nil
		},
	})
	if err := Revoke("skill", "alice", "s1", "bob"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if gotOwner != "alice" || gotID != "s1" || gotRecipient != "bob" {
		t.Errorf("revoke got (%q, %q, %q)", gotOwner, gotID, gotRecipient)
	}
}

// A kind with no revoke says so rather than reporting success. A take-back
// that silently does nothing is worse than one that refuses.
func TestAKindThatCannotRevokeSaysSo(t *testing.T) {
	reset(t)
	Register("skill", Provider{Label: "Skill"})
	err := Revoke("skill", "alice", "s1", "bob")
	if err == nil {
		t.Fatal("a kind with no revoke reported success")
	}
	if !strings.Contains(err.Error(), "skill") {
		t.Errorf("the refusal does not name the kind: %v", err)
	}
	if err := Revoke("nothing", "alice", "x", ""); err == nil {
		t.Error("an unregistered kind reported success")
	}
}

// Nobody is not everybody: an empty user gets an empty list, not the whole
// deployment's.
func TestAnEmptyUserCollectsNothing(t *testing.T) {
	reset(t)
	Register("skill", Provider{
		Label: "Skill",
		Mine:  func(string) []Grant { return []Grant{{ID: "s1"}} },
		ToMe:  func(string) []Grant { return []Grant{{ID: "s2"}} },
	})
	if got := Mine("  "); len(got) != 0 {
		t.Errorf("mine = %+v", got)
	}
	if got := ToMe(""); len(got) != 0 {
		t.Errorf("to-me = %+v", got)
	}
}

// A kind registered with no label is not a kind: it would render as a blank
// group somebody cannot act on.
func TestAnUnlabelledKindIsNotRegistered(t *testing.T) {
	reset(t)
	Register("skill", Provider{Mine: func(string) []Grant { return []Grant{{ID: "s1"}} }})
	if got := Kinds(); len(got) != 0 {
		t.Errorf("kinds = %v", got)
	}
}
