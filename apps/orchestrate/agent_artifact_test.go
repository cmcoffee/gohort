package orchestrate

// Tests for the agent artifact's dependency closure over AllowedSkills: an
// exported agent folds in the skills it allowlists (by ID — the skill recipe
// preserves it), the preview matches the ID-based reference against the
// traveling recipe, and import lands both with the wiring intact.

import (
	"encoding/json"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// pinRootDB points core's RootDB at a fresh mem store for the test's duration
// — the skill store prefers RootDB, so without the pin skill writes would land
// somewhere no artifact probe reads.
func pinRootDB(t *testing.T) Database {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })
	return db
}

func TestAgentClosure_CarriesAllowedSkill(t *testing.T) {
	app := pipelineArtifactApp(t)
	root := pinRootDB(t)
	skill, err := SaveSkill(root, "alice", SkillRecord{
		Name: "triage", Description: "Use for triage.", Instructions: "Assess first.",
	})
	if err != nil {
		t.Fatalf("save skill: %v", err)
	}
	udb := UserDB(app.DB, "alice")
	if _, err := saveAgent(udb, AgentRecord{
		Owner: "alice", Name: "medic", OrchestratorPrompt: "m",
		AllowedSkills: []string{skill.ID},
	}); err != nil {
		t.Fatalf("save agent: %v", err)
	}

	b, err := ExportArtifactBundle(root, []ArtifactSel{{Type: "agent", Name: "medic", Owner: "alice"}})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(b.Artifacts) != 2 || b.Artifacts[0].Type != "agent" || b.Artifacts[1].Type != "skill" {
		t.Fatalf("expected [agent, skill], got %+v", b.Artifacts)
	}
	var sr SkillRecord
	if err := json.Unmarshal(b.Artifacts[1].Recipe, &sr); err != nil {
		t.Fatalf("skill recipe: %v", err)
	}
	if sr.ID != skill.ID {
		t.Fatalf("skill must travel with preserved ID (the agent's reference key): %+v", sr)
	}

	// On a fresh install, preview must see the agent's ID-based skill
	// reference satisfied by the bundle itself, and import must land both
	// with the wiring intact (skill disabled for review, as always).
	fresh := pipelineArtifactApp(t)
	freshRoot := pinRootDB(t)
	data, _ := json.Marshal(b)
	res, err := PreviewArtifactBundle(freshRoot, data, "bob")
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if res.WouldImport != 2 || len(res.Warnings) != 0 {
		t.Fatalf("both should import with no warnings: %+v", res)
	}
	ires, err := ImportArtifactBundle(freshRoot, data, "bob")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if ires.Imported != 2 || len(ires.Warnings) != 0 {
		t.Fatalf("expected 2 imports with no warnings: %+v", ires)
	}
	landed, ok := FindSkillByName(freshRoot, "bob", "triage")
	if !ok || landed.ID != skill.ID || !landed.Disabled {
		t.Fatalf("imported skill must keep the traveled ID and land disabled: %+v", landed)
	}
	imported, ok := findAgentByNameOrID(UserDB(fresh.DB, "bob"), "bob", "medic")
	if !ok || len(imported.AllowedSkills) != 1 || imported.AllowedSkills[0] != skill.ID {
		t.Fatalf("imported agent must still reference the skill by its traveled ID: %+v", imported.AllowedSkills)
	}
}
