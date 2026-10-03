package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
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
