package core

// Every import lands inert. An imported file is somebody else's decision, so
// nothing in it may assert a grant, an approval, or a share on this install:
// the importer (or an admin) makes those here, through the normal doors.

import (
	"encoding/json"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

func TestImportedPipelineAndMachineArriveUnpublished(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	p, err := ImportPipeline(udb, "alice", PipelineDef{
		Name: "p", Published: true, Global: true, AllowedUsers: []string{"bob"},
		Stages: StarterPipeline().Stages,
	})
	if err != nil {
		t.Fatalf("import pipeline: %v", err)
	}
	if p.Published || p.Global || len(p.AllowedUsers) != 0 || p.Owner != "alice" {
		t.Errorf("pipeline kept a grant it never got here: published=%v global=%v users=%v owner=%q",
			p.Published, p.Global, p.AllowedUsers, p.Owner)
	}
	m := StarterMachine()
	m.Published, m.AllowedUsers = true, []string{"bob"}
	got, err := ImportMachine(udb, "alice", m)
	if err != nil {
		t.Fatalf("import machine: %v", err)
	}
	if got.Published || len(got.AllowedUsers) != 0 || got.Owner != "alice" {
		t.Errorf("machine kept a grant: published=%v users=%v owner=%q", got.Published, got.AllowedUsers, got.Owner)
	}
}

// stubAutoConnector is a kind that goes live on create, the way rest_poll does.
type stubAutoConnector struct{}

func (stubAutoConnector) Validate(Connector) error    { return nil }
func (stubAutoConnector) Materialize(Connector) error { return nil }
func (stubAutoConnector) Teardown(Connector) error    { return nil }
func (stubAutoConnector) Summary(Connector) string    { return "stub" }
func (stubAutoConnector) AutoApprove() bool           { return true }

func TestImportedConnectorWaitsForApprovalEvenWhenItsKindAutoApproves(t *testing.T) {
	RegisterConnectorKind("test_auto_import", stubAutoConnector{})
	db := &DBase{Store: kvlite.MemStore()}

	// Created here: live at once, which is what the kind is for.
	if err := SaveConnector(db, Connector{Name: "made-here", Kind: "test_auto_import"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := GetConnector(db, "made-here"); !c.Approved {
		t.Fatal("an auto-approving kind created here should be live")
	}

	// Imported, by both doors: a draft.
	recipe, _ := json.Marshal(PortableConnector{Name: "via-bundle", Kind: "test_auto_import"})
	if _, _, err := (connectorArtifact{}).ImportArtifact(db, recipe, "admin"); err != nil {
		t.Fatalf("bundle import: %v", err)
	}
	pack, _ := json.Marshal(PortableConnector{Name: "via-pack", Kind: "test_auto_import"})
	if _, err := ImportConnectorPack(db, pack, "bob"); err != nil {
		t.Fatalf("pack import: %v", err)
	}
	for _, name := range []string{"via-bundle", "via-pack"} {
		c, ok := GetConnector(db, name)
		if !ok {
			t.Fatalf("%s was not saved", name)
		}
		if c.Approved {
			t.Errorf("%s went live on import; it should wait for an admin", name)
		}
	}
}

func TestImportedCredentialIsGlobalAndCarriesNoGrants(t *testing.T) {
	secureAPITestStore(t)
	recipe, _ := json.Marshal(SecureCredential{
		Name: "crm", Type: SecureCredHeader, ParamName: "X-Key", BaseURL: "https://crm.example",
		Owner:                "victim",
		SharedReadOnly:       []string{"mallory"},
		SharedReadWrite:      []string{"mallory"},
		Lending:              "anyone",
		ApprovedToolBindings: []string{"exfil_tool"},
		Managed:              "peer",
		InsecureSkipTLS:      true,
		AllowedUsers:         []string{"alice"},
		Secured:              true,
	})
	if _, skip, err := (credentialArtifact{}).ImportArtifact(nil, recipe, "admin"); err != nil || skip != "" {
		t.Fatalf("import: skip=%q err=%v", skip, err)
	}
	c, ok := Secure().Load("crm")
	if !ok {
		t.Fatal("the credential did not land as a global record")
	}
	if c.Owner != "" {
		t.Errorf("owner should be cleared to global, got %q", c.Owner)
	}
	if !c.Disabled {
		t.Error("an imported credential must land disabled")
	}
	if len(c.SharedReadOnly)+len(c.SharedReadWrite) != 0 || c.Lending != "" || len(c.ApprovedToolBindings) != 0 ||
		c.Managed != "" || c.InsecureSkipTLS {
		t.Errorf("grants traveled: %+v", c)
	}
	// Restrictions travel.
	if !c.Secured || len(c.AllowedUsers) != 1 {
		t.Errorf("restrictions should travel: secured=%v allowed=%v", c.Secured, c.AllowedUsers)
	}
}

func TestImportedSkillCarriesNoShareList(t *testing.T) {
	db := skillTestDB(t)
	recipe, _ := json.Marshal(SkillRecord{
		Name: "triage", Description: "d", Instructions: "i",
		AllowedUsers: []string{"bob"}, SharedFrom: "carol",
	})
	if _, _, err := (skillArtifact{}).ImportArtifact(db, recipe, "alice"); err != nil {
		t.Fatal(err)
	}
	s, ok := FindSkillByName(db, "alice", "triage")
	if !ok {
		t.Fatal("skill not imported")
	}
	if len(s.AllowedUsers) != 0 || s.SharedFrom != "" || !s.Disabled {
		t.Errorf("imported skill: users=%v sharedFrom=%q disabled=%v", s.AllowedUsers, s.SharedFrom, s.Disabled)
	}
}

// Chunks live under the global source collection:<id>. A traveled ID another
// user already holds would pour this import into their corpus.
func TestImportedCollectionRemintsAnIDSomebodyElseHolds(t *testing.T) {
	collectionTestDB(t)
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })

	seedCollection(t, "bob", "bobs-coll", "Bob's notes")
	recipe, _ := json.Marshal(PortableCollection{ID: "bobs-coll", Name: "Imported"})
	if _, skip, err := (collectionArtifact{}).ImportArtifact(nil, recipe, "alice"); err != nil || skip != "" {
		t.Fatalf("import: skip=%q err=%v", skip, err)
	}
	var mine []Collection
	for _, c := range ListCollections(UserDB(CollectionsDB(), "alice"), "alice") {
		if c.Name == "Imported" {
			mine = append(mine, c)
		}
	}
	if len(mine) != 1 {
		t.Fatalf("expected the import under alice, got %+v", mine)
	}
	if mine[0].ID == "bobs-coll" {
		t.Error("the import reused an ID bob's collection already holds")
	}

	// A free ID still travels, which is what keeps bundle wiring intact.
	free, _ := json.Marshal(PortableCollection{ID: "fresh-id", Name: "Second"})
	if _, _, err := (collectionArtifact{}).ImportArtifact(nil, free, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadCollection(UserDB(CollectionsDB(), "alice"), "alice", "fresh-id"); !ok {
		t.Error("a free traveled ID should be kept")
	}
}
