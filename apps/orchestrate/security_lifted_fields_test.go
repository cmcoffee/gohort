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
	// The all-agents page carries no SETTINGS at all any more - only standing
	// decisions - so it has no Network tab to keep in step. A second copy of
	// the defaults chain is what this replaced.
	fleet := mustReadFile(t, "page_fleet_security.go")
	if strings.Contains(fleet, "fleet-defaults") || strings.Contains(fleet, "workspace_network") {
		t.Error("the owner-side page offers the settings again, so two surfaces answer one question")
	}
}

// A default nobody can find is not a default. The per-agent control offers
// "use the default for all agents" and has to say where that is set.
func TestThePerAgentControlSaysWhereTheDefaultLives(t *testing.T) {
	security := mustReadFile(t, "page_agent_access.go")
	// The option itself names the value it resolves to, which is what the
	// reader actually needs: "Default (Allowed)" says both what happens and
	// that this agent is following rather than deciding.
	if !strings.Contains(security, `Label: "Default (" + settingWord(key, effectiveDeploymentDefault(db, key)) + ")",`) {
		t.Error("the inherited option does not say what it resolves to")
	}
	if !strings.Contains(security, "set once for the whole deployment, by an administrator") {
		t.Error("the control offers a default without saying whose it is")
	}
}

// Nothing on these pages starts folded: the heading already says what it is.
func TestTheEditorsRemainingHeaderIsNotCollapsed(t *testing.T) {
	editor := mustReadFile(t, "page_agent.go")
	if strings.Contains(editor, `Label: "Access & visibility", Collapsed: true`) {
		t.Error("the heading is stale and folded: its contents moved to Security")
	}
}

// A route out of a page belongs in a SECTION, not in the page nav.
//
// Security is read as a panel inside chat as often as at its own URL, and a
// panel draws the body alone - so a link carried only by the header does not
// exist on the surface most people read it from. Drawing Page.Nav into the body
// was the wrong answer twice: Nav is usually the whole HUB menu, which the host
// is already showing, and .ui-page-tabs is laid out as a column of the header
// GRID, so in ordinary body flow it drew on top of what was beneath it.
func TestTheRouteToTheFleetDefaultIsInTheSection(t *testing.T) {
	security := mustReadFile(t, "page_agent_access.go")
	if !strings.Contains(security, `adminOnlyLink(RequestIsAdmin(r), "Where that default is set"`) {
		t.Error("the control offers a default with no way to reach where it is set")
	}
	// Only for somebody who can follow it. A link to a page that would refuse
	// the reader is worse than no link: it reads as a permission they have and
	// a page that is broken.
	if !strings.Contains(security, "func adminOnlyLink(isAdmin bool") {
		t.Error("the link is shown to everyone, including the people it would refuse")
	}

	// And the body rendering does NOT draw the page nav.
	src := mustReadRuntime(t, "99_epilogue.js")
	if strings.Contains(src, "bodyNav") {
		t.Error("renderPageBody draws Page.Nav again, which lands the hub menu on top of the page")
	}
	if strings.Count(src, "= navStrip(cfg)") != 1 {
		t.Error("the header nav grew a second caller")
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
