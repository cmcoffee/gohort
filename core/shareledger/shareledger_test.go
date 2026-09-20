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

// The half that was missing: telling the person on the receiving end.
func TestASharedThingTellsItsRecipient(t *testing.T) {
	reset(t)
	type told struct {
		who, title, intro string
		needs             []string
	}
	var sent []told
	saved := NotifyRecipient
	NotifyRecipient = func(who, title, intro string, needs []string) {
		sent = append(sent, told{who, title, intro, needs})
	}
	t.Cleanup(func() { NotifyRecipient = saved })

	Register("agent", Provider{
		Label:      "Agent",
		Candidates: func(string) []Grant { return []Grant{{ID: "a1", Name: "Troubleshooter"}} },
		Share:      func(string, string, []string, map[string]string) []string { return []string{"done"} },
		Manifest: func(owner, id, recipient string) []string {
			if recipient == "bob" {
				return []string{"Add a credential named wiki."}
			}
			return nil
		},
	})

	if _, err := Share("agent", "alice", "a1", []string{"bob", "carol"}, nil); err != nil {
		t.Fatalf("share: %v", err)
	}
	if len(sent) != 2 {
		t.Fatalf("told %d people", len(sent))
	}
	// By NAME, not by id: a notice quoting "a1" at somebody who has never
	// seen it tells them nothing.
	if !strings.Contains(sent[0].title, "Troubleshooter") {
		t.Errorf("the notice does not name the thing: %q", sent[0].title)
	}
	// Per person. One colleague needing something says nothing about another.
	if len(sent[0].needs) != 1 || len(sent[1].needs) != 0 {
		t.Errorf("the manifest is not per recipient: %+v", sent)
	}
}

// Unwired, a share is silent — which is what it was before any of this. A
// package that assumed a notification store would make itself unusable
// anywhere that has none.
func TestASharedThingIsQuietWithNowhereToTell(t *testing.T) {
	reset(t)
	saved := NotifyRecipient
	NotifyRecipient = nil
	t.Cleanup(func() { NotifyRecipient = saved })

	Register("agent", Provider{
		Label: "Agent",
		Share: func(string, string, []string, map[string]string) []string { return []string{"done"} },
	})
	if _, err := Share("agent", "alice", "a1", []string{"bob"}, nil); err != nil {
		t.Errorf("share: %v", err)
	}
}

// A kind with no manifest asks nothing, rather than asking about nothing.
func TestAKindWithNoManifestAsksNothing(t *testing.T) {
	reset(t)
	Register("skill", Provider{Label: "Skill"})
	if got := Manifest("skill", "alice", "s1", "bob"); len(got) != 0 {
		t.Errorf("manifest = %v", got)
	}
	if got := Manifest("nothing", "alice", "x", "bob"); len(got) != 0 {
		t.Errorf("an unregistered kind returned %v", got)
	}
}
