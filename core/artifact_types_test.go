package core

import (
	"testing"

	"github.com/cmcoffee/gohort/core/sourcehooks"
	"github.com/cmcoffee/snugforge/kvlite"
)

// researchSourceTestDB pins RootDB to a fresh mem store and stands in the
// research sources (search connectors put to use under Research Sources,
// which core/service supplies and core tests do not link) for the test.
func researchSourceTestDB(t *testing.T, sources ...SourceHook) Database {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	savedRoot, savedSources := RootDB, sourcehooks.ConnectorSources
	RootDB = db
	sourcehooks.ConnectorSources = func() []SourceHook { return sources }
	t.Cleanup(func() { RootDB, sourcehooks.ConnectorSources = savedRoot, savedSources })
	return db
}

// A skill that allowlists a research source's tool name depends on that
// source's search connector, so exporting the skill carries the connector.
func TestASkillDependsOnItsSourcesConnector(t *testing.T) {
	db := researchSourceTestDB(t, SourceHook{Name: "pubmed-search", Type: HookTypeAPI, Connector: "pubmed-search",
		ExposeToLLM: true, ToolName: "pubmed_search"})
	if _, err := SaveSkill(db, "alice", SkillRecord{
		Name: "clinical", Description: "Use for clinical questions.",
		AllowedTools: []string{"pubmed_search"},
	}); err != nil {
		t.Fatalf("save skill: %v", err)
	}
	var found bool
	for _, d := range (skillArtifact{}).Dependencies(db, "clinical", "alice") {
		if d.Type == "connector" && d.Name == "pubmed-search" {
			found = true
		}
		if d.Type == "source_hook" {
			t.Errorf("a source hook dependency: %+v", d)
		}
	}
	if !found {
		t.Error("the skill does not carry its source's connector")
	}
}
