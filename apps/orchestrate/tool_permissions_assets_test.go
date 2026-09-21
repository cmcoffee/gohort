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
		"return ['ask', 'always', 'attended'];",
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
	if !strings.Contains(orchestrateWebAssets, "Object.keys(permOffered).forEach(function(n) {") ||
		!strings.Contains(orchestrateWebAssets, "if (!granted[n]) { return; }") {
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

// The agent's OWN tools carry a permission too, and they are the likeliest
// thing to have a grant on them: a credential-backed shell or api tool built
// for this agent.
//
// They never appear in the server-supplied pool — availableWorkerToolOptions
// carries SHARED rows only, because an agent-scoped row belongs to one agent's
// kit — so the first cut showed no ladder on exactly the rows that needed one
// and, worse, dropped their stored grant on the next save of this modal.
func TestTheAgentsOwnToolsAreEligibleAndCounted(t *testing.T) {
	src := orchestrateWebAssets
	if !strings.Contains(src, "(agent.tools || []).forEach(function(t) {") {
		t.Error("the agent's own tools are not made eligible for a permission")
	}
	if !strings.Contains(src, "scopedCbs.forEach(function(c) { if (c.checked) granted[c.value] = true; });") {
		t.Error("the save does not count the agent's own tools as granted, so their grants are dropped")
	}
	if !strings.Contains(src, "toolPermControl(t.name, scopedPermCbs[t.name])") {
		t.Error("the agent's own rows render no permission control")
	}
}

// Whatever is stored comes back. The round trip is the whole point: a grant
// set before this control existed must show as Always, not as Ask, or saving
// the modal quietly revokes it.
func TestStoredGrantsSurviveTheRoundTrip(t *testing.T) {
	src := orchestrateWebAssets
	// Seeded from both stored lists...
	for _, want := range []string{
		"(agent.auto_approve_tools || []).forEach(function(n) { preApproved[n] = true; });",
		"(agent.no_unattended_tools || []).forEach(function(n) { heldBack[n] = true; });",
		"if (heldBack[name]) { return 'attended'; }",
		"if (preApproved[name] && states.indexOf('always') >= 0) { return 'always'; }",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("not seeded from storage: %s", want)
		}
	}
	// ...and written back from the rows that actually offered a choice.
	if !strings.Contains(src, "Object.keys(permOffered).forEach(") {
		t.Error("the save does not write back the state it read")
	}
	// A stored entry the modal never asked about survives the save. Without
	// this, opening the modal and pressing Save revoked whatever it had not
	// drawn a control for.
	for _, want := range []string{
		"(agent.auto_approve_tools || []).filter(function(n) { return !permOffered[n]; })",
		"(agent.no_unattended_tools || []).filter(function(n) { return !permOffered[n]; })",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the save clears what it never asked about: %s", want)
		}
	}
}

// A tool the gate would NOT stop shows what it does, and is not offered an
// approval that grants nothing.
//
// This is the mistake the privilege card already made once and fixed: it
// tiered on temptool.NeedsConfirm, so a tool that simply runs rendered "ask",
// inviting the owner to grant something the runtime never withholds. The
// ladder repeated it by defaulting every row to Ask, which is why an agent
// whose tools were allowed when they were added read as though every one of
// them would stop.
func TestAToolNothingWithholdsIsNotOfferedAnApproval(t *testing.T) {
	src := orchestrateWebAssets
	if !strings.Contains(src, "if ((toolPolicy[name] || '') === 'auto') { return ['runs', 'attended']; }") {
		t.Error("the ladder does not read the gate's own answer; every row defaults to Ask")
	}
	// It still offers the one direction that WOULD change such a tool.
	if !strings.Contains(src, "runs: 'Runs'") {
		t.Error("a freely-running tool has no state of its own to show")
	}
	// And the policy is fetched rather than guessed.
	if !strings.Contains(src, "'/tool-policy'") {
		t.Error("the modal never asks what the gate would do")
	}
}

// The ladder is about UNATTENDED runs, and that has to be on screen rather
// than in hover text.
//
// "Ask" read as "ask me every time": an owner set it, used the agent in chat,
// was never prompted, and reasonably concluded the control did nothing. The
// gate only exists on the two unattended surfaces (autonomousGate); in chat an
// agent uses its tools freely and always has.
func TestTheLadderSaysItIsAboutUnattendedRuns(t *testing.T) {
	src := orchestrateWebAssets
	if !strings.Contains(src, "SCHEDULED or STANDING run") {
		t.Error("nothing on screen says which runs the pill governs")
	}
	if !strings.Contains(src, "None of it applies in chat") {
		t.Error("the modal does not say the pill is silent in chat, which is where it was read as broken")
	}
}
