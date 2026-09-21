package orchestrate

// The Tools modal's permission ladder, as shipped in the page assets.
//
// Structural rather than behavioural, because the asset is a script in an HTML
// file: what is worth pinning is that the three states exist, that they
// decompose back into the two fields the runtime reads, and that a permission
// cannot be saved for a tool the agent cannot call.

import (
	"strings"
	"testing"
)

func TestTheLadderHasThreeStatesAndSavesToBothLists(t *testing.T) {
	src := orchestrateWebAssets
	for _, want := range []string{
		"var permOrder = ['ask', 'always', 'attended'];",
		"agent.auto_approve_tools = always;",
		"agent.no_unattended_tools = attended;",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing from the modal: %s", want)
		}
	}
	// Seeded from the stored lists, or reopening the modal would show every
	// tool as Ask and saving would quietly clear both.
	for _, want := range []string{
		"(agent.auto_approve_tools || []).forEach",
		"(agent.no_unattended_tools || []).forEach",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the ladder does not read back the stored state: %s", want)
		}
	}
}

// A permission on a tool the agent cannot call is a grant nobody made, and it
// would come back the moment the tool was ticked again.
func TestOnlyGrantedToolsKeepAPermission(t *testing.T) {
	if !strings.Contains(orchestrateWebAssets, "if (!granted[n] || !approvable[n]) { return; }") {
		t.Error("the save writes permissions for tools the agent does not have")
	}
}

// A read-only tool never prompts, so every state means the same thing and the
// row offers none.
func TestAReadOnlyToolGetsNoLadder(t *testing.T) {
	if !strings.Contains(orchestrateWebAssets, "if (!approvable[name]) { return null; }") {
		t.Error("the ladder is offered on tools nothing withholds")
	}
}
