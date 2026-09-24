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

// hooks swaps the three wiring points for the length of a test.
func hooks(t *testing.T, find func(kind, owner, id string, users []string) []Dependent) *[]string {
	t.Helper()
	savedFind, savedNotify, savedGone := FindDependents, NotifyRecipient, OnWithdrawn
	t.Cleanup(func() { FindDependents, NotifyRecipient, OnWithdrawn = savedFind, savedNotify, savedGone })
	FindDependents = find
	OnWithdrawn = nil
	var sent []string
	NotifyRecipient = func(recipient, title, intro string, needs []string) {
		sent = append(sent, recipient+"|"+title+"|"+intro+"|"+strings.Join(needs, " "))
	}
	return &sent
}

// The owner sees who relies on a grant before taking it back: filled on their
// side, for named recipients and for everybody-at-once alike.
func TestTheOwnersViewNamesWhoReliesOnEachGrant(t *testing.T) {
	reset(t)
	var asked [][]string
	hooks(t, func(kind, owner, id string, users []string) []Dependent {
		asked = append(asked, users)
		if id == "s1" {
			return []Dependent{{User: "bob", Uses: []string{"Triage"}}}
		}
		return nil
	})
	Register("skill", Provider{
		Label: "Skill",
		Mine: func(owner string) []Grant {
			return []Grant{
				{ID: "s1", Name: "Runbook", Recipients: []string{"bob", "carol"}},
				{ID: "s2", Name: "Tone", Wide: true},
			}
		},
		ToMe: func(user string) []Grant { return []Grant{{ID: "s9", Name: "Theirs", Owner: "dana"}} },
	})
	mine := Mine("alice")
	if len(mine[0].Dependents) != 1 || mine[0].Dependents[0].User != "bob" {
		t.Errorf("the grant does not say bob's Triage relies on it: %+v", mine[0].Dependents)
	}
	// Named recipients are asked about by name; a deployment-wide grant is
	// asked about everybody (nil).
	if len(asked) != 2 || strings.Join(asked[0], ",") != "bob,carol" || asked[1] != nil {
		t.Errorf("wrong people asked about: %#v", asked)
	}
	// The recipient's side is not the one deciding anything.
	if got := ToMe("bob"); got[0].Dependents != nil {
		t.Errorf("the recipient's view should carry no dependents: %+v", got[0])
	}
}

// A kind referenced some other way narrows the question itself.
func TestAKindCanNarrowItsDependents(t *testing.T) {
	reset(t)
	hooks(t, func(kind, owner, id string, users []string) []Dependent {
		t.Error("the generic finder was asked although the kind answers for itself")
		return nil
	})
	Register("tool", Provider{
		Label: "Tool",
		Dependents: func(owner, id string, users []string) []Dependent {
			return []Dependent{{User: "bob", Uses: []string{"Helper"}}}
		},
	})
	if got := DependentsOf("tool", "alice", "wiki", []string{"bob"}); len(got) != 1 || got[0].Uses[0] != "Helper" {
		t.Errorf("DependentsOf = %+v", got)
	}
}

// Everybody who lost it is told; whoever had something relying on it is told
// which of their things, and that is what makes the notice wait on them.
func TestWithdrawnTellsEachPersonAndNamesTheirAgents(t *testing.T) {
	reset(t)
	sent := hooks(t, func(kind, owner, id string, users []string) []Dependent {
		return []Dependent{{User: "bob", Uses: []string{"Triage", "Intake"}}}
	})
	var remembered string
	OnWithdrawn = func(kind, owner, id, name string, deps []Dependent) { remembered = kind + ":" + id + "=" + name }
	Register("collection", Provider{Label: "Knowledge"})

	Withdrawn("collection", "alice", "c1", "Runbooks", []string{"bob", "carol"})
	if len(*sent) != 2 {
		t.Fatalf("want one notice per person who lost it, got %v", *sent)
	}
	bob, carol := (*sent)[0], (*sent)[1]
	if !strings.HasPrefix(bob, "bob|\"Runbooks\" (knowledge) is no longer available|") ||
		!strings.Contains(bob, "\"Triage\", \"Intake\" use it") || !strings.Contains(bob, "ask alice") {
		t.Errorf("bob's notice: %s", bob)
	}
	if !strings.HasPrefix(carol, "carol|") || !strings.HasSuffix(carol, "|") {
		t.Errorf("carol relied on nothing, so her notice asks nothing of her: %s", carol)
	}
	if remembered != "collection:c1=Runbooks" {
		t.Errorf("the last name was not handed on: %q", remembered)
	}
	if strings.Contains(strings.Join(*sent, ""), "—") {
		t.Error("user-facing text carries an em-dash")
	}
}

// A record that reached everybody is not announced to everybody: only to the
// people who were actually relying on it.
func TestWithdrawnFromEverybodyTellsOnlyItsDependents(t *testing.T) {
	reset(t)
	sent := hooks(t, func(kind, owner, id string, users []string) []Dependent {
		if users != nil {
			t.Errorf("a deployment-wide record should ask about everybody, got %v", users)
		}
		return []Dependent{{User: "bob", Uses: []string{"Triage"}}}
	})
	Register("skill", Provider{Label: "Skill"})
	WithdrawnFromEverybody("skill", "alice", "s1", "Runbook")
	if len(*sent) != 1 || !strings.HasPrefix((*sent)[0], "bob|") {
		t.Errorf("want bob alone told, got %v", *sent)
	}
	// Nobody lost it: nothing is said, and no finder is asked.
	*sent = nil
	Withdrawn("skill", "alice", "s1", "Runbook", nil)
	if len(*sent) != 0 {
		t.Errorf("an empty take-back said something: %v", *sent)
	}
}
