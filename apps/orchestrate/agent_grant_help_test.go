// What the agent editor says about access other apps have granted.
//
// This lands in the "Cortex & capability" section's HELP rather than as its own
// field, because a header field starts a new page section — a row here would
// have cut that section in two and stranded the toggles below it.
package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func withGrantor(t *testing.T, grants ...AgentGrant) {
	t.Helper()
	RegisterAgentGrantor(AgentGrantor{
		Name: "servitor", Label: "Machines", ManageURL: "/servitor/manage",
		Granted: func(_, _ string) []AgentGrant { return grants },
	})
}

func TestGrantHelpNamesWhatIsHeldAndWhereToChangeIt(t *testing.T) {
	withGrantor(t, AgentGrant{Label: "Lab Box", Detail: "nothing without asking"})
	help := appGrantHelp("craig", "wren")

	for _, want := range []string{
		"GRANTED BY OTHER APPS", // the heading someone scans for
		"Machines: Lab Box",     // what it holds
		"nothing without asking", // how much
		"/servitor/manage",       // where to change it
		"read-only here",         // why there are no controls
	} {
		if !strings.Contains(help, want) {
			t.Errorf("capability help should carry %q, got:\n%s", want, help)
		}
	}
}

// An app granting nothing still reports, so the capability is discoverable
// before it is needed rather than after.
func TestGrantHelpShowsAppsGrantingNothing(t *testing.T) {
	withGrantor(t)
	help := appGrantHelp("craig", "wren")
	if !strings.Contains(help, "Machines: none") {
		t.Errorf("an app granting nothing should still appear, got:\n%s", help)
	}
}

// An unsaved agent has no id, so it cannot hold a grant — and the section must
// not sprout an empty heading on the create form.
func TestGrantHelpIsEmptyForAnUnsavedAgent(t *testing.T) {
	withGrantor(t, AgentGrant{Label: "Lab Box"})
	if got := appGrantHelp("craig", ""); got != "" {
		t.Errorf("a new agent should get no grant text, got %q", got)
	}
}

// The text is appended to existing help, so it must start with a break rather
// than running into the sentence before it.
func TestGrantHelpAppendsCleanly(t *testing.T) {
	withGrantor(t, AgentGrant{Label: "Lab Box"})
	help := appGrantHelp("craig", "wren")
	if !strings.HasPrefix(help, "\n\n") {
		t.Errorf("appended help must separate itself from the copy above, got %q", help[:20])
	}
	if strings.HasSuffix(help, "\n") {
		t.Error("trailing newlines would show as a gap in the panel")
	}
}
