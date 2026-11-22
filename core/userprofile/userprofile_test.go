package userprofile

import (
	"encoding/json"
	"strings"
	"testing"
)

type memStore map[string]map[string][]byte

func (m memStore) Get(table, key string, out interface{}) bool {
	b, ok := m[table][key]
	return ok && json.Unmarshal(b, out) == nil
}
func (m memStore) Set(table, key string, v interface{}) {
	if m[table] == nil {
		m[table] = map[string][]byte{}
	}
	m[table][key], _ = json.Marshal(v)
}
func (m memStore) Unset(table, key string) { delete(m[table], key) }
func (m memStore) Keys(table string) []string {
	var out []string
	for k := range m[table] {
		out = append(out, k)
	}
	return out
}

// A person gets the profile that names them, else the default (the one
// naming nobody); each person gets one, and there is one default.
func TestEachPersonGetsOneProfile(t *testing.T) {
	SetStore(memStore{})
	t.Cleanup(func() { SetStore(nil) })
	if _, ok := For("alice", nil); ok {
		t.Fatal("a profile with none saved")
	}
	if err := Save(Profile{ID: "desk", Name: "Front Desk", Users: []string{"alice", " Bob "}, Apps: []string{"apps/desk"}, StartPath: "apps/desk/"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Profile{ID: "all", Name: "Company"}); err != nil {
		t.Fatal(err)
	}
	if p, ok := For("BOB", nil); !ok || p.ID != "desk" || p.StartPath != "/apps/desk/" || p.Apps[0] != "/apps/desk" {
		t.Errorf("bob's: %+v", p)
	}
	if p, ok := For("carol", nil); !ok || p.ID != "all" {
		t.Errorf("carol, named by none, gets the default: %+v", p)
	}
	if err := Save(Profile{ID: "ops", Name: "Ops", Users: []string{"alice"}}); err == nil || !strings.Contains(err.Error(), "Front Desk") {
		t.Errorf("alice on two profiles: %v", err)
	}
	if err := Save(Profile{ID: "all2", Name: "Second default"}); err == nil {
		t.Error("a second default was kept")
	}
	if err := Save(Profile{ID: "x", Name: " "}); err == nil {
		t.Error("a profile with no name was kept")
	}
	if err := Save(Profile{ID: "x", Name: "Away", Users: []string{"dan"}, StartPath: "https://elsewhere.example/"}); err == nil {
		t.Error("a start page elsewhere was kept")
	}
	Delete("desk")
	if p, _ := For("alice", nil); p.ID != "all" {
		t.Errorf("with her profile gone alice gets the default: %+v", p)
	}
}

// What a profile shows and lends: everything when it lists nothing, else
// what it lists, a listed path covering what is under it.
func TestWhatAProfileShowsAndLends(t *testing.T) {
	p := Profile{}.Normalize()
	if !p.ShowsApp("/anything") || !p.LendsTool("filesystem_read") {
		t.Error("a profile listing nothing narrows something")
	}
	p = Profile{Apps: []string{"/apps/desk/"}, Tools: []string{"contacts_search"}}.Normalize()
	if !p.ShowsApp("/apps/desk") || !p.ShowsApp("/apps/desk/x") || p.ShowsApp("/apps/desktop") || p.ShowsApp("/anvil") {
		t.Error("apps shown wrong")
	}
	if !p.LendsTool("Contacts_Search") || p.LendsTool("filesystem_read") {
		t.Error("tools lent wrong")
	}
}

// A team's profile reaches its members a profile names by name does not;
// one team is on one profile; what a profile takes away is one it knows.
func TestATeamsProfileAndWhatItTakesAway(t *testing.T) {
	SetStore(memStore{})
	t.Cleanup(func() { SetStore(nil) })
	if err := Save(Profile{ID: "desk", Name: "Front Desk", Users: []string{"alice"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Profile{ID: "ops", Name: "Ops", Teams: []string{"t-ops"}, Removes: []string{RemoveAPIKeys, FeaturePrefix + "mcp"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Profile{ID: "all", Name: "Company"}); err != nil {
		t.Fatal(err)
	}
	onOps := func(team string) bool { return team == "t-ops" }
	if p, _ := For("alice", onOps); p.ID != "desk" {
		t.Errorf("a person named by name, on a team too: %+v", p)
	}
	p, _ := For("bob", onOps)
	if p.ID != "ops" || !p.Takes(RemoveAPIKeys) || !p.TakesFeature("mcp") || p.TakesFeature("openai") || p.Takes(RemoveYourData) {
		t.Errorf("bob, on the team: %+v", p)
	}
	if p, _ := For("bob", nil); p.ID != "all" {
		t.Errorf("bob, on no team: %+v", p)
	}
	if err := Save(Profile{ID: "ops2", Name: "Ops Two", Teams: []string{"t-ops"}}); err == nil || !strings.Contains(err.Error(), "Ops") {
		t.Errorf("a team on two profiles: %v", err)
	}
	if err := Save(Profile{ID: "x", Name: "X", Users: []string{"carol"}, Removes: []string{"the_kitchen_sink"}}); err == nil {
		t.Error("a profile took away something no one knows")
	}
	if (Profile{Teams: []string{"t"}}).IsDefault() || !(Profile{}).IsDefault() {
		t.Error("a team's profile read as the default, or the default did not")
	}
}
