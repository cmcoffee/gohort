package codewriter

// A writer travels with its agents named, not numbered, and lands pointing at
// the importer's agent of that name.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// fakeAgents lists each user's agents with install-specific ids.
type fakeAgents map[string][]ReferenceItem

func (fakeAgents) Kind() string                                         { return agentRefKind }
func (fakeAgents) Label() string                                        { return "Agents" }
func (f fakeAgents) List(user string) []ReferenceItem                   { return f[user] }
func (fakeAgents) Fetch(context.Context, string, string, string) string { return "" }

func TestAWriterTravelsWithItsAgentsByName(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
	RegisterReferenceSource(fakeAgents{
		"alice": {{ID: "a-111", Name: "SQL Expert"}},
		"bob":   {{ID: "b-999", Name: "SQL Expert"}},
	})

	T := &CodeWriterAgent{}
	T.DB = &DBase{Store: kvlite.MemStore()}
	wa := &writerArtifact{app: T}
	UserDB(T.DB, "alice").Set(writerTable, "w1", WriterRecord{
		ID: "w1", Name: "Acme SQL", Lang: "sql",
		Sources:     ReferenceSelections{{Kind: agentRefKind, ItemID: "a-111"}, {Kind: "filestore", ItemID: "schemas"}},
		Collections: []string{"coll-9"},
	})

	raw, err := wa.ExportArtifact(nil, "w1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "a-111") || !strings.Contains(string(raw), "SQL Expert") {
		t.Fatalf("the agent should travel by name: %s", raw)
	}
	var kinds []string
	for _, d := range wa.Dependencies(nil, "Acme SQL", "alice") {
		kinds = append(kinds, d.Type+":"+d.Name)
	}
	if strings.Join(kinds, ",") != "agent:SQL Expert,reference_source:filestore:schemas,collection:coll-9" {
		t.Errorf("deps: %v", kinds)
	}

	if _, skip, err := wa.ImportArtifact(nil, raw, "bob"); err != nil || skip != "" {
		t.Fatalf("import: %q %v", skip, err)
	}
	got, ok := wa.find("bob", "Acme SQL")
	if !ok {
		t.Fatal("not in bob's writers")
	}
	if got.Sources[0].ItemID != "b-999" || got.Lang != "sql" || got.ID == "w1" {
		t.Errorf("imported writer: %+v", got)
	}
	var probe map[string]any
	_ = json.Unmarshal(raw, &probe)
	if _, ok := probe["id"]; ok {
		t.Error("the recipe carries the writer's id")
	}
}
