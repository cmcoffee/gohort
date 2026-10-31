package agents

// The chat a dashboard card opens draws an agent's intake form with the
// console's own script. It had a copy of its own that never learned steps or
// live options, so Builder's form ("Which one?" listing the person's own
// agents and apps) could not be filled in here, and the options it fetches
// relative to this page had no route. Walked in a browser when it was fixed;
// pinned here so a second copy does not grow back.

import (
	"os"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/apps/orchestrate"
)

func TestTheDashboardChatUsesTheConsolesIntakeForm(t *testing.T) {
	b, err := os.ReadFile("agents.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "orchestrate.IntakeFormHead") || !strings.Contains(src, "window.uiAgentIntakeSetAgent(") {
		t.Error("the page does not load the shared intake form and name its agent")
	}
	if strings.Contains(src, "function buildForm(") || strings.Contains(src, "ui-orch-intake {") {
		t.Error("a copy of the intake form is back in this app")
	}
	if !strings.Contains(src, `rest == "api/intake/options"`) || !strings.Contains(src, "PublicHandleIntakeOptions") {
		t.Error("the form's live options have no route on this page")
	}
	for _, want := range []string{"options_from", "show_when", "exclusive_option", "window.uiAgentIntakeSetAgent = function"} {
		if !strings.Contains(orchestrate.IntakeFormHead, want) {
			t.Errorf("the shared intake form lacks %s", want)
		}
	}
}
