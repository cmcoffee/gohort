package orchestrate

// Builder is rebased onto its in-code seed on every load, so the only
// fields that survive a save are the ones applyBuilderDeploymentState
// names. A toggle the editor RENDERS but that list omits saves happily
// and reads back false — it looks like a control that refuses to stay
// on, and migrateBuilderShadows then writes the rebuilt record back over
// the shadow at boot, making the loss permanent at the next restart.
//
// This has now happened twice (LeadModel, then MCPExposed), so it gets a
// test rather than a third comment.

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestBuilderKeepsOwnerSetDeploymentState(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}

	// A shadow as the editor would write it: owner decisions only.
	shadow := AgentRecord{
		ID:         "seed-builder",
		Rules:      "never touch production",
		MCPExposed: true,
		LeadModel:  true,
	}
	db.Set(agentsTable, "seed-builder", shadow)

	got, ok := loadAgent(db, "seed-builder")
	if !ok {
		t.Fatal("Builder did not load")
	}
	// The framework still owns the prompt — that is the whole point of the
	// rebase, and a test that broke it would be worse than no test.
	if got.OrchestratorPrompt == shadow.OrchestratorPrompt {
		t.Error("the in-code prompt should have replaced the shadow's")
	}
	// The owner's decisions survive.
	if !got.MCPExposed {
		t.Error("MCPExposed was discarded — the editor shows this toggle on Builder, so it reads as refusing to stay on")
	}
	if !got.LeadModel {
		t.Error("LeadModel was discarded")
	}
	if got.Rules != "never touch production" {
		t.Errorf("Rules were discarded: %q", got.Rules)
	}

	// And false stays false — the carry must not be a one-way latch that
	// makes the toggle impossible to turn back OFF.
	db.Set(agentsTable, "seed-builder", AgentRecord{ID: "seed-builder", MCPExposed: false, Rules: "x"})
	if off, _ := loadAgent(db, "seed-builder"); off.MCPExposed {
		t.Error("MCPExposed could be turned on but not off")
	}
}
