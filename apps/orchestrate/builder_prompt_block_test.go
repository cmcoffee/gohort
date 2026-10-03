package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/prompts"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Builder's instructions are a prompt block holding the raw document, an
// edit to the block is what Builder then runs with, placeholders still
// expanded, and clearing the edit puts the shipped prompt back.
func TestBuildersPromptIsABlock(t *testing.T) {
	var block PromptBlock
	for _, b := range AllPromptBlocks() {
		if b.Key == BuilderPromptKey {
			block = b
		}
	}
	if block.Text == "" {
		t.Fatal("Builder's instructions are not in the prompt registry")
	}
	shipped, _ := seedAgentByID("seed-builder")
	if !strings.Contains(block.Text, "{{") {
		t.Skip("the Builder seed has no placeholders to check expansion with")
	}
	if strings.Contains(shipped.OrchestratorPrompt, "{{") {
		t.Fatal("the shipped prompt reached Builder with its placeholders unexpanded")
	}

	SetPromptOverrideDB(&DBase{Store: kvlite.MemStore()})
	t.Cleanup(func() { SetPromptOverrideDB(nil) })
	i := strings.Index(block.Text, "{{")
	j := strings.Index(block.Text[i:], "}}") + i + 2
	SetPromptOverride(BuilderPromptKey, "EDITED BUILDER. "+block.Text[i:j])
	edited, _ := seedAgentByID("seed-builder")
	if !strings.HasPrefix(edited.OrchestratorPrompt, "EDITED BUILDER. ") || strings.Contains(edited.OrchestratorPrompt, "{{") {
		t.Fatalf("edited prompt = %.120q", edited.OrchestratorPrompt)
	}
	ClearPromptOverride(BuilderPromptKey)
	if back, _ := seedAgentByID("seed-builder"); back.OrchestratorPrompt != shipped.OrchestratorPrompt {
		t.Fatal("clearing the edit did not put the shipped prompt back")
	}
}

// Every block can carry a tier's own wording into a prompt: its text can be
// found there, and Builder's, expanded and gated as a turn gets it, takes the
// worker's own wording when the worker answers.
func TestEveryBlockCanBeWordedPerTier(t *testing.T) {
	// Filled per call with that turn's tools, and ending in the fill: there
	// is nothing to find it by, and nothing a tuned wording would buy.
	cannot := map[string]bool{toolsDirectiveKey: true}
	for _, b := range AllPromptBlocks() {
		why := prompts.TierTextPlaceable(b.Key, b.Text)
		if why != "" && !cannot[b.Key] {
			t.Errorf("%s: %s", b.Key, why)
		}
		if why == "" && cannot[b.Key] {
			t.Errorf("%s is listed as unplaceable and is not: take it off the list", b.Key)
		}
	}
	SetPromptOverrideDB(&DBase{Store: kvlite.MemStore()})
	t.Cleanup(func() { SetPromptOverrideDB(nil) })
	var raw string
	for _, b := range AllPromptBlocks() {
		if b.Key == BuilderPromptKey {
			raw = b.Text
		}
	}
	rec, _ := seedAgentByID("seed-builder")
	system := "Agent context.\n\n" + gatedPersonaFor(rec, rec.OrchestratorPrompt) + "\n\nFramework blocks."
	prompts.SetPromptTierOverride(prompts.TierWorker, BuilderPromptKey, "WORKER BUILDER. "+raw, "")
	got := prompts.ApplyTierText(prompts.TierWorker, system)
	if !strings.Contains(got, "WORKER BUILDER. ") || strings.Contains(got, "{{") {
		t.Fatalf("the worker's Builder wording was not swapped in as expanded: %.200q", got)
	}
	if prompts.ApplyTierText(prompts.TierLead, system) != system {
		t.Fatal("the lead got the worker's Builder wording")
	}
}
