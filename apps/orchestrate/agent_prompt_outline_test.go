package orchestrate

// Builder writes an agent's prompt in the outline the editor offers, so it
// opens in the editor's sections instead of as one block above them.

import (
	"strings"
	"testing"
)

func TestBuilderIsToldTheAgentPromptOutline(t *testing.T) {
	create := createAgentTool{}.Params()["orchestrator_prompt"].Description
	update := updateAgentTool{}.Params()["orchestrator_prompt"].Description
	for name, d := range map[string]string{"create_agent": create, "update_agent": update} {
		for _, sec := range orchestratorPromptSections {
			if !strings.Contains(d, "## "+sec.Title+"\n") {
				t.Errorf("%s does not name the %q section", name, sec.Title)
			}
		}
		if !strings.Contains(d, "markdown list") || !strings.Contains(d, "second person") || !strings.Contains(d, "rules field") {
			t.Errorf("%s does not say how sections are written: %s", name, d)
		}
		if strings.Contains(d, "—") {
			t.Errorf("%s has an em-dash", name)
		}
	}
	if !strings.Contains(update, "same headings") || strings.Contains(create, "same headings") {
		t.Error("only update_agent says how to change one part")
	}
}
