package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Builder's record rebases onto the in-code seed on every read, so a field the
// owner sets only survives if it is named in applyBuilderDeploymentState. Three
// have fallen off that list so far — scope pills, bundled tools, and now the
// lead-model toggle — and each looked identical from the outside: a control
// that saves and then reads back at its old value. These pin the ones that
// must survive AND the ones that must not, because the second half is the
// reason the list exists.

func TestBuilderShadowPreservesOwnerDecisions(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	seed, ok := seedAgentByID("seed-builder")
	if !ok {
		t.Fatal("seed-builder should be a seed agent")
	}
	// What the owner set on their copy.
	shadow := AgentRecord{
		ID:                      "seed-builder",
		LeadModel:               true,
		Rules:                   "always probe before asking",
		DisabledCredentials:     []string{"stripe"},
		DisabledPipelines:       []string{"nightly"},
		AttachedPipelines:       []string{"triage"},
		DisabledPersistentTools: []string{"noisy_tool"},
		Tools:                   []TempTool{{Name: "scoped_helper", CommandTemplate: "echo hi"}},
		// Framework structure the owner does NOT get a vote on — a stale
		// shadow carrying an old prompt must not win over the current code.
		OrchestratorPrompt: "STALE PROMPT FROM AN OLD BUILD",
		AllowedTools:       []string{"only_this_one"},
	}
	db.Set(agentsTable, "seed-builder", shadow)

	got, ok := loadAgent(db, "seed-builder")
	if !ok {
		t.Fatal("loadAgent(seed-builder) failed")
	}
	if !got.LeadModel {
		t.Error("lead_model must survive the rebase — this is the toggle that would not stay on")
	}
	if got.Rules != shadow.Rules {
		t.Errorf("rules = %q, want the owner's", got.Rules)
	}
	if len(got.DisabledCredentials) != 1 || got.DisabledCredentials[0] != "stripe" {
		t.Errorf("disabled credentials = %v", got.DisabledCredentials)
	}
	if len(got.AttachedPipelines) != 1 || len(got.DisabledPipelines) != 1 || len(got.DisabledPersistentTools) != 1 {
		t.Errorf("scope state lost: %+v / %+v / %+v", got.AttachedPipelines, got.DisabledPipelines, got.DisabledPersistentTools)
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != "scoped_helper" {
		t.Errorf("bundled tools lost: %+v", got.Tools)
	}
	// The other half of the contract: code wins on framework structure.
	if got.OrchestratorPrompt == shadow.OrchestratorPrompt {
		t.Error("a stale shadow prompt must NOT survive — prompt updates have to reach existing deployments")
	}
	if got.OrchestratorPrompt != seed.OrchestratorPrompt {
		t.Error("prompt should come from the in-code seed")
	}
	if len(got.AllowedTools) == 1 && got.AllowedTools[0] == "only_this_one" {
		t.Error("a stale shadow AllowedTools must NOT survive")
	}
}

// TestBuilderShadowMigrationKeepsSameState — the startup migration and the read
// path must agree. They didn't: the migration preserved only Rules, so every
// restart wrote the seed's empty scope fields over the shadow that loadAgent
// then read back from, quietly undoing state the read path was protecting.
func TestBuilderShadowMigrationKeepsSameState(t *testing.T) {
	seed, ok := seedAgentByID("seed-builder")
	if !ok {
		t.Fatal("seed-builder should be a seed agent")
	}
	shadow := AgentRecord{
		ID:                  "seed-builder",
		LeadModel:           true,
		Rules:               "keep me",
		DisabledCredentials: []string{"stripe"},
		Tools:               []TempTool{{Name: "scoped_helper", CommandTemplate: "echo hi"}},
	}
	// What the migration would write.
	merged := seed
	applyBuilderDeploymentState(&merged, shadow)

	// What a read would produce from the same shadow.
	readBack := seed
	applyBuilderDeploymentState(&readBack, shadow)

	if merged.LeadModel != readBack.LeadModel || !merged.LeadModel {
		t.Error("migration must not drop lead_model")
	}
	if len(merged.DisabledCredentials) != len(readBack.DisabledCredentials) || len(merged.DisabledCredentials) != 1 {
		t.Errorf("migration must not drop scope state: %v", merged.DisabledCredentials)
	}
	if len(merged.Tools) != 1 {
		t.Errorf("migration must not drop bundled tools: %+v", merged.Tools)
	}
	if merged.Rules != "keep me" {
		t.Errorf("migration must not drop rules: %q", merged.Rules)
	}

	// An empty shadow leaves the seed's own values alone rather than blanking
	// Rules — a fresh deployment has nothing to preserve.
	fresh := seed
	applyBuilderDeploymentState(&fresh, AgentRecord{})
	if fresh.Rules != seed.Rules {
		t.Errorf("an empty shadow should not blank the seed's rules: %q", fresh.Rules)
	}
	if fresh.LeadModel != false {
		t.Error("an empty shadow should leave lead_model off")
	}
}
