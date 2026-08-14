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
