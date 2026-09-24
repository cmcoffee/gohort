package core

// Every door that takes a shared tool, skill or collection away from somebody
// tells them, whichever page it was reached from, and a move that takes nothing
// away (publishing) tells nobody.

import (
	"sort"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/shareledger"
	"github.com/cmcoffee/snugforge/kvlite"
)

type withdrawal struct {
	kind, id, name string
	lost           []string
}

// catchWithdrawals records every take-back the kinds announce, through the
// same two hooks the deployment wires: who depends (asked with nil for
// everybody) and who is told.
func catchWithdrawals(t *testing.T) (*[]withdrawal, *[]string) {
	t.Helper()
	savedRoot, savedVec := RootDB, VectorDB
	RootDB, VectorDB = &DBase{Store: kvlite.MemStore()}, &DBase{Store: kvlite.MemStore()}
	savedFind, savedNotify, savedGone := shareledger.FindDependents, shareledger.NotifyRecipient, shareledger.OnWithdrawn
	t.Cleanup(func() {
		RootDB, VectorDB = savedRoot, savedVec
		shareledger.FindDependents, shareledger.NotifyRecipient, shareledger.OnWithdrawn = savedFind, savedNotify, savedGone
	})
	var got []withdrawal
	var told []string
	shareledger.FindDependents = func(kind, owner, id string, users []string) []shareledger.Dependent {
		if users == nil {
			// Everybody-at-once: say one person relied on it.
			return []shareledger.Dependent{{User: "dana", Uses: []string{"Helper"}}}
		}
		return nil
	}
	shareledger.OnWithdrawn = func(kind, owner, id, name string, deps []shareledger.Dependent) {
		got = append(got, withdrawal{kind: kind, id: id, name: name})
	}
	shareledger.NotifyRecipient = func(recipient, title, intro string, needs []string) {
		told = append(told, recipient)
		sort.Strings(told)
	}
	return &got, &told
}

func TestSkillTakeBacksAreAnnounced(t *testing.T) {
	got, told := catchWithdrawals(t)
	s, err := SaveSkill(nil, "alice", SkillRecord{Name: "Runbook", AllowedUsers: []string{"bob", "carol"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 {
		t.Fatalf("sharing announced a take-back: %+v", *got)
	}
	// Narrowed through the skill's own save.
	s.AllowedUsers = []string{"carol"}
	if _, err := SaveSkill(nil, "alice", s); err != nil {
		t.Fatal(err)
	}
	if strings.Join(*told, ",") != "bob" {
		t.Errorf("narrowing should tell bob alone, told %v", *told)
	}
	// Deleted: carol, who still had it.
	*told = nil
	if !DeleteSkill(nil, "alice", s.ID) {
		t.Fatal("delete failed")
	}
	if strings.Join(*told, ",") != "carol" {
		t.Errorf("deleting should tell carol, told %v", *told)
	}
	last := (*got)[len(*got)-1]
	if last.name != "Runbook" {
		t.Errorf("a deleted skill must be announced by name, got %+v", last)
	}

	// Published, then taken back from the deployment: whoever relied on it.
	p, _ := SaveSkill(nil, "alice", SkillRecord{Name: "Tone", AllowedUsers: []string{"bob"}})
	*told = nil
	if err := promoteSkillToDeployment("alice", p.ID); err != nil {
		t.Fatal(err)
	}
	if len(*told) != 0 {
		t.Errorf("publishing took nothing from anybody, yet told %v", *told)
	}
	if err := NarrowSkillToOwner(nil, "alice", p.ID); err != nil {
		t.Fatal(err)
	}
	if strings.Join(*told, ",") != "dana" {
		t.Errorf("unpublishing should tell the people relying on it, told %v", *told)
	}
}

func TestCollectionTakeBacksAreAnnounced(t *testing.T) {
	_, told := catchWithdrawals(t)
	cdb := UserDB(CollectionsDB(), "alice")
	if cdb == nil {
		t.Skip("no per-user collection store")
	}
	SaveCollection(cdb, Collection{ID: "c1", Owner: "alice", Name: "Runbooks", AllowedUsers: []string{"bob", "carol"}})
	// Through the ledger's revoke, which saves through the same door the
	// collection page does.
	if err := shareledger.Revoke("collection", "alice", "c1", "bob"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(*told, ",") != "bob" {
		t.Errorf("revoking bob should tell bob, told %v", *told)
	}
	*told = nil
	DeleteCollection(cdb, nil, "alice", "c1")
	if strings.Join(*told, ",") != "carol" {
		t.Errorf("deleting should tell carol, told %v", *told)
	}

	SaveCollection(cdb, Collection{ID: "c2", Owner: "alice", Name: "Wide"})
	if err := PromoteCollectionToDeployment("alice", "c2"); err != nil {
		t.Fatal(err)
	}
	*told = nil
	if err := NarrowCollectionToOwner("alice", "c2"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(*told, ",") != "dana" {
		t.Errorf("narrowing a deployment collection should tell whoever relied on it, told %v", *told)
	}
}

// A tool's peer share narrows through one setter reached from several pages;
// the people it drops are told, except anybody it still reaches another way.
func TestToolTakeBacksAreAnnounced(t *testing.T) {
	_, told := catchWithdrawals(t)
	if err := AdminPersistTempTool(nil, "alice", TempTool{Name: "wiki_read", Description: "d", CommandTemplate: "echo"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolSharedWith(nil, "alice", "wiki_read", []string{"bob", "carol"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolSharedWith(nil, "alice", "wiki_read", []string{"carol"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(*told, ",") != "bob" {
		t.Errorf("dropping bob should tell bob, told %v", *told)
	}
	// Published AND shared with carol: taking the share back leaves her the
	// published copy, so nothing was taken from her.
	*told = nil
	if err := SetPersistentTempToolShared(nil, "alice", "wiki_read", true); err == nil {
		if err := SetPersistentTempToolSharedWith(nil, "alice", "wiki_read", nil); err != nil {
			t.Fatal(err)
		}
		if len(*told) != 0 {
			t.Errorf("carol still reaches the published tool, yet was told she lost it: %v", *told)
		}
	}
}

// The hook the tool store's own take-backs call (unpublish, delete): with no
// list, it asks after everybody who TOOK this owner's copy.
func TestAToolWithdrawnFromEverybodyReachesItsTakers(t *testing.T) {
	_, told := catchWithdrawals(t)
	if err := AdminPersistTempTool(nil, "alice", TempTool{Name: "wiki_read", Description: "d", CommandTemplate: "echo"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolSharedWith(nil, "alice", "wiki_read", []string{"bob"}); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalToolAdopted(nil, "bob", "wiki_read", "alice", true); err != nil {
		t.Fatal(err)
	}
	if got := toolTakersOf("alice", "wiki_read"); strings.Join(got, ",") != "bob" {
		t.Fatalf("takers = %v", got)
	}
	// Still shared with him: nothing to say.
	noteToolWithdrawn("alice", "wiki_read", nil)
	if len(*told) != 0 {
		t.Errorf("bob still has it, yet was told: %v", *told)
	}
	// Gone (share dropped silently here, as a delete would leave it).
	list := LoadPersistentTempTools(nil, "alice")
	list[0].SharedWith = nil
	RootDB.Set(persistentTempToolsTable, "alice", list)
	noteToolWithdrawn("alice", "wiki_read", nil)
	if strings.Join(*told, ",") != "bob" {
		t.Errorf("bob took it and lost it, told %v", *told)
	}
}

// The published-tool doors call the hook themselves: withdrawing, deleting, and
// narrowing the adopt list away from somebody who took it.
func TestPublishedToolWithdrawalsTellWhoTookIt(t *testing.T) {
	for _, door := range []string{"withdraw", "delete", "narrow"} {
		t.Run(door, func(t *testing.T) {
			_, told := catchWithdrawals(t)
			if err := AdminPersistTempTool(nil, "alice", TempTool{Name: "wiki_read", Description: "d", CommandTemplate: "echo"}); err != nil {
				t.Fatal(err)
			}
			if err := SetPersistentTempToolShared(nil, "alice", "wiki_read", true); err != nil {
				t.Fatal(err)
			}
			if err := SetGlobalToolAdopted(nil, "bob", "wiki_read", "alice", true); err != nil {
				t.Fatal(err)
			}
			switch door {
			case "withdraw":
				_ = SetPersistentTempToolShared(nil, "alice", "wiki_read", false)
			case "delete":
				_ = DeletePersistentTempTool(nil, "alice", "wiki_read")
			case "narrow":
				_ = SetPersistentTempToolAllowedUsers(nil, "alice", "wiki_read", []string{"carol"})
			}
			if strings.Join(*told, ",") != "bob" {
				t.Errorf("%s: bob took it and lost it, told %v", door, *told)
			}
		})
	}
}
