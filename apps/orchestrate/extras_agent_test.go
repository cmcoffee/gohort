package orchestrate

// The agent recipes in extras/ are pasted at /api/agents/import. One
// that fails to import, or that quietly disagrees with what its own
// prompt promises, is worse than no recipe — so both are checked here.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtrasAgentRecipesImport(t *testing.T) {
	paths, _ := filepath.Glob("../../extras/*.agent.json")
	if len(paths) == 0 {
		t.Skip("no agent recipes in extras/")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			var imp agentExport
			if err := json.Unmarshal(raw, &imp); err != nil {
				t.Fatalf("not a valid agent recipe: %v", err)
			}
			if strings.TrimSpace(imp.Name) == "" {
				t.Error("a recipe needs a name")
			}
			if strings.TrimSpace(imp.OrchestratorPrompt) == "" {
				t.Error("a recipe needs a prompt")
			}
			if imp.ID != "" || imp.Owner != "" {
				t.Error("a recipe must not assert an identity — import assigns one")
			}
		})
	}
}

// The investigator's whole point is that specifics do not survive an
// investigation while generalizations do. That is two config flags plus
// a prompt paragraph, and all three have to agree: a prompt that says
// "do not save the specifics" on an agent whose vector memory is quietly
// accumulating them is a promise the config breaks.
func TestInvestigatorRecipeKeepsLessonsNotIncidents(t *testing.T) {
	raw, err := os.ReadFile("../../extras/investigator.agent.json")
	if err != nil {
		t.Skip("recipe not present")
	}
	var imp agentExport
	if err := json.Unmarshal(raw, &imp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// "agent" mode is the Lessons-learned directive, not the
	// personalization one.
	if imp.MemoryMode != "" && imp.MemoryMode != "agent" {
		t.Errorf("memory_mode should be the lessons directive, got %q", imp.MemoryMode)
	}
	// Reference Memory is the layer that would compound one incident's
	// findings into the next investigation by similarity. It is the
	// actual mechanism of the mixing-up this agent must not do.
	if !imp.DisableInferred {
		t.Error("disable_inferred must be set, or incident specifics compound across bundles")
	}
	// And the prompt has to say it, because the mode is a directive to
	// the model rather than a gate on the write.
	prompt := strings.ToLower(imp.OrchestratorPrompt)
	for _, want := range []string{"do not save", "generalized"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt should tell the model what not to keep; missing %q", want)
		}
	}
}
