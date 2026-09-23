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
