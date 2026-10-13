package core

// Sharing an agent, skill or collection with a group: the group rides in the
// recipient list as "group:<id>", reaches its members while its administrator
// lets members share with it and the owner is still in it, and membership is
// read at the check.

import (
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// groupShareFixture is alice and bob in the "Ops" group (members may share),
// carol in no group, and dana in "Bundle" (members may not share). It returns
// the auth store and the Ops group id.
func groupShareFixture(t *testing.T) (Database, string) {
	t.Helper()
	adb := &DBase{Store: kvlite.MemStore()}
	prevAuth, prevRoot, prevVec := AuthDB, RootDB, VectorDB
	AuthDB = func() Database { return adb }
	RootDB, VectorDB = &DBase{Store: kvlite.MemStore()}, &DBase{Store: kvlite.MemStore()}
	ops, _ := SaveAppGroup(adb, AppGroup{ID: "g-ops", Name: "Ops", Shareable: true})
	SaveAppGroup(adb, AppGroup{ID: "g-bundle", Name: "Bundle"})
	t.Cleanup(func() {
		DeleteAppGroup(adb, "g-ops")
		DeleteAppGroup(adb, "g-bundle")
		AuthDB, RootDB, VectorDB = prevAuth, prevRoot, prevVec
	})
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice", Groups: []string{"g-ops"}})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob", Groups: []string{"g-ops"}})
	adb.Set(AuthTable, "user:carol", AuthUser{Username: "carol"})
	adb.Set(AuthTable, "user:dana", AuthUser{Username: "dana", Groups: []string{"g-bundle"}})
	return adb, ops.ID
}

func TestAGroupEntryReachesItsMembers(t *testing.T) {
	adb, ops := groupShareFixture(t)
	list := []string{"group:" + ops}
	if !SharedWithUser(list, "alice", "bob") {
		t.Error("a member of the group alice shared with does not have it")
	}
	if SharedWithUser(list, "alice", "carol") {
		t.Error("somebody outside the group has it")
	}
	if !SharedWithUser([]string{"carol"}, "alice", "carol") {
		t.Error("a name on the list stopped working")
	}
	// A group alice is not in reaches nobody through her.
	if SharedWithUser([]string{"group:g-bundle"}, "alice", "dana") {
		t.Error("a group the owner is not in (and that does not share) carried her share")
	}
	if got := ShareRecipientKeys("bob"); len(got) != 2 || got[0] != "bob" || got[1] != "group:"+ops {
		t.Errorf("bob's keys = %v", got)
	}
	if got := ShareRecipientKeys("dana"); len(got) != 1 {
		t.Errorf("a group members may not share with is no key: %v", got)
	}
	if got := ShareMembers(append(list, "carol"), "alice"); strings.Join(got, ",") != "bob,carol" {
		t.Errorf("members = %v, want bob and carol (the owner left out)", got)
	}
	if got := ShareRecipientsText([]string{"carol", "group:" + ops, "group:gone"}); got != "carol, the Ops group, a removed group" {
		t.Errorf("text = %q", got)
	}

	// The owner leaving takes the share with her; so does the administrator
	// turning sharing off for the group.
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	if SharedWithUser(list, "alice", "bob") {
		t.Error("the share outlived its owner leaving the group")
	}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice", Groups: []string{"g-ops"}})
	SaveAppGroup(adb, AppGroup{ID: ops, Name: "Ops"})
	if SharedWithUser(list, "alice", "bob") {
		t.Error("the share outlived the group's sharing being turned off")
	}
}

// The picker offers alice's shareable groups after the people, and only when
// the kind asked for them.
func TestThePickerOffersTheOwnersShareableGroups(t *testing.T) {
	groupShareFixture(t)
	with := string(ShareCandidatesJSON("alice", true))
	if !strings.Contains(with, `"value":"group:g-ops"`) || !strings.Contains(with, "Group: Ops") || !strings.Contains(with, `"desc":"1 other member"`) {
		t.Errorf("alice's shareable group is missing: %s", with)
	}
	if strings.Contains(with, "g-bundle") || strings.Contains(with, `"value":"alice"`) {
		t.Errorf("offered a group she may not share with, or herself: %s", with)
	}
	if without := string(ShareCandidatesJSON("alice", false)); strings.Contains(without, "group:") || !strings.Contains(without, `"value":"bob"`) {
		t.Errorf("a picker that did not ask for groups: %s", without)
	}
}

func TestASkillSharedWithAGroupReachesItsMembers(t *testing.T) {
	_, ops := groupShareFixture(t)
	if _, err := SaveSkill(RootDB, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess.",
		AllowedUsers: []string{"group:" + ops}}); err != nil {
		t.Fatal(err)
	}
	if got := sharedSkillsFor(RootDB, "bob"); len(got) != 1 || got[0].SharedFrom != "alice" {
		t.Fatalf("bob, in Ops, does not have it: %+v", got)
	}
	if got := sharedSkillsFor(RootDB, "carol"); len(got) != 0 {
		t.Errorf("carol, outside Ops, has it: %+v", got)
	}
}

func TestACollectionSharedWithAGroupReachesItsMembers(t *testing.T) {
	_, ops := groupShareFixture(t)
	owner := UserDB(CollectionsDB(), "alice")
	if owner == nil {
		t.Skip("no per-user store in this configuration")
	}
	SaveCollection(owner, Collection{ID: "col-1", Owner: "alice", Name: "Runbooks",
		AllowedUsers: []string{"group:" + ops}, Contributors: []string{"group:" + ops}})
	if got := sharedCollectionsFor("bob"); len(got) != 1 {
		t.Fatalf("bob, in Ops, does not have it: %+v", got)
	}
	if _, ok := LoadCollection(UserDB(CollectionsDB(), "bob"), "bob", "col-1"); !ok {
		t.Error("bob cannot resolve it by id")
	}
	if got := sharedCollectionsFor("carol"); len(got) != 0 {
		t.Errorf("carol, outside Ops, has it: %+v", got)
	}
	c, _ := LoadCollection(owner, "alice", "col-1")
	if !CollectionContributor(c, "bob") || CollectionContributor(c, "carol") {
		t.Error("a contributing group should let its members add, and nobody else")
	}
}

func TestAToolSharedWithAGroupReachesItsMembers(t *testing.T) {
	_, ops := groupShareFixture(t)
	if err := AdminPersistTempTool(RootDB, "alice", TempTool{Name: "ssh_run"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := SetPersistentTempToolSharedWith(RootDB, "alice", "ssh_run", []string{"group:" + ops}); err != nil {
		t.Fatalf("share: %v", err)
	}
	if got := PeerSharedToolsFor(RootDB, "bob"); len(got) != 1 || got[0].Owner != "alice" {
		t.Fatalf("bob, in Ops, is not offered it: %+v", got)
	}
	if got := PeerSharedToolsFor(RootDB, "carol"); len(got) != 0 {
		t.Errorf("carol, outside Ops, is offered it: %+v", got)
	}
	if !toolStillOffered("alice", "ssh_run", "bob") || toolStillOffered("alice", "ssh_run", "carol") {
		t.Error("a tool bob took through the group should stay his, and carol never had it")
	}
}
