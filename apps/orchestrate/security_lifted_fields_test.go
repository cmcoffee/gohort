package orchestrate

// The editor is for what an agent IS. What it may reach is a different errand,
// and it lives on the agent's Security window.
//
// Named per FILE, because both files are in this package: asking the package
// whether a control exists cannot tell which page offers it, which is the
// entire question. This is the same shape as the assertion that caught the
// workspace ceiling being offered in two places at once - two controls over one
// fact drift, and the one you did not use is the one you go on believing.

import (
	"os"
	"strings"
	"testing"
)

func TestEnforcementIsOfferedOnSecurityAndNotTheEditor(t *testing.T) {
	editor := mustReadFile(t, "page_agent.go")
	security := mustReadFile(t, "page_agent_access.go")

	for _, c := range []struct {
		field string
		why   string
	}{
		{"force_private", "whether the agent may reach out at all"},
		{"allow_private_mode", "whether the person using it may cut the network"},
		{"disabled_tool_actions", "which parts of a tool it may use"},
		{"enabled_credentials", "which outside systems it may reach"},
	} {
		if strings.Contains(editor, `"`+c.field+`"`) {
			t.Errorf("the editor still offers %s (%s), so there are two controls over one fact", c.field, c.why)
		}
		if !strings.Contains(security, `"`+c.field+`"`) {
			t.Errorf("%s (%s) is offered nowhere: it left the editor and did not arrive on Security", c.field, c.why)
		}
	}

	// The credential picker's URLs have to be ABSOLUTE now. Security sits a
	// level deeper than the editor it came from (/agent/<id>/access), so the
	// relative paths it used there resolve against THIS page and fetch
	// something else entirely - which surfaces as an empty picker, not an
	// error.
	if strings.Contains(security, `"../api/agent-credentials`) {
		t.Error("the credential picker kept its relative URL, which resolves against the wrong page here")
	}
	if !strings.Contains(security, `T.WebPrefix() + "/api/agent-credentials?id="`) {
		t.Error("the credential picker does not use an absolute URL")
	}

	// A sub-agent runs on its parent's credentials, so scoping it separately
	// would offer a decision that never takes effect.
	if !strings.Contains(security, `if strings.TrimSpace(agent.OwnedBy) == "" {`) {
		t.Error("credential scoping is offered on sub-agents, where it decides nothing")
	}
}

// The tab is named for the question it answers. The workspace ceiling and
// Private mode are the two halves of what may leave, so they are one tab.
func TestTheNetworkTabHoldsBothHalves(t *testing.T) {
	security := mustReadFile(t, "page_agent_access.go")
	if strings.Contains(security, `Group:    "Workspace"`) || strings.Contains(security, `Group: "Workspace"`) {
		t.Error("the tab is still called Workspace, which does not cover the privacy controls now under it")
	}
	if strings.Count(security, `Group:    "Network"`) < 3 {
		t.Error("the Network tab is missing one of its sections")
	}
	// The fleet page mirrors these tabs; a rename on one side alone puts the
	// same setting under two different headings.
	if !strings.Contains(mustReadFile(t, "page_fleet_security.go"), `Group:    "Network"`) {
		t.Error("the all-agents page still calls it Workspace, so one setting sits under two headings")
	}
}

// A default nobody can find is not a default. The per-agent control offers
// "use the default for all agents" and has to say where that is set.
func TestThePerAgentControlSaysWhereTheDefaultLives(t *testing.T) {
	security := mustReadFile(t, "page_agent_access.go")
	if !strings.Contains(security, "The default for all agents is set on the All agents page") {
		t.Error("the control offers a default without saying where to change it")
	}
	if !strings.Contains(security, `Label: "All agents"`) {
		t.Error("the link to the all-agents page is gone, so the pointer points at nothing")
	}
}

// Nothing on these pages starts folded: the heading already says what it is.
func TestTheEditorsRemainingHeaderIsNotCollapsed(t *testing.T) {
	editor := mustReadFile(t, "page_agent.go")
	if strings.Contains(editor, `Label: "Access & visibility", Collapsed: true`) {
		t.Error("the heading is stale and folded: its contents moved to Security")
	}
}

// A page's nav travels with its BODY, not only with its header row.
//
// Security is read from inside the chat overlay as often as from its own URL,
// and the overlay draws the body alone - so the "All agents" link, which is the
// only route to the fleet-wide defaults the per-agent controls point at, did
// not exist in the place people actually read the page from.
func TestThePagesNavSurvivesBeingDrawnAsAPanel(t *testing.T) {
	src := mustReadRuntime(t, "99_epilogue.js")
	if strings.Count(src, "navStrip(cfg)") < 2 {
		t.Error("the nav is built in one rendering only, so a page drawn as a panel loses it")
	}
	if !strings.Contains(src, "bodyNav = navStrip(cfg)") {
		t.Error("renderPageBody does not draw the page's nav")
	}
	// The nav only. The host surface draws its own chrome, and a second title
	// inside it would be the same page announcing itself twice.
	if strings.Contains(src, "bodyNav.appendChild(el('h1'") {
		t.Error("the body rendering grew a page title, which the host already shows")
	}
}

func mustReadRuntime(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../core/ui/assets/runtime/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
