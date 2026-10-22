package orchestrate

// Framework prompt text written for a person chatting on the web stays out of
// the shapes it does not fit: a machine step's delegate, a step that narrowed
// its tools, a plan worker, a sub-agent. And the round preamble names only the
// ways out a turn has.

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestTheRoundPreambleNamesOnlyTheWaysOutThisTurnHas(t *testing.T) {
	both := roundShapePreamble(8, true, true)
	if !strings.Contains(both, "ask_user") || !strings.Contains(both, "plan_set") {
		t.Errorf("a turn with both should hear of both:\n%s", both)
	}
	if s := roundShapePreamble(8, true, false); strings.Contains(s, "plan_set") || !strings.Contains(s, "ask_user") {
		t.Errorf("no plan_set:\n%s", s)
	}
	if s := roundShapePreamble(8, false, false); strings.Contains(s, "plan_set") || strings.Contains(s, "ask_user") {
		t.Errorf("neither:\n%s", s)
	}
	// No announcing tool calls: the person sees each one run.
	for _, gone := range []string{"Before a tool call, write ONE short sentence", "dead air"} {
		if strings.Contains(both, gone) {
			t.Errorf("the preamble still asks for narration: %q", gone)
		}
	}
	if !strings.Contains(both, "no need to announce a tool call") {
		t.Error("the preamble does not say tool calls need no announcing")
	}
}

func TestAShapingCallAfterASubRunReachesNoTools(t *testing.T) {
	ph := MachinePhase{Name: "triage", Agent: "helper", Tools: []string{"web_search"}, Think: "on",
		Output: []PipelineField{{Name: "need", Type: "string"}}}
	s := shapingStep(ph)
	if PhaseReach(s) != ReachNone || s.Think != "off" {
		t.Errorf("the shaping call reaches tools or reasons: %+v", s)
	}
	if len(s.Output) != 1 || ph.Reach == ReachNone {
		t.Error("the shaping step lost its fields, or changed the step itself")
	}
	if PhaseTools(s, []AgentToolDef{{Tool: Tool{Name: "web_search"}}}) != nil {
		t.Error("the shaping call was handed a tool")
	}
}

func TestChatGuidanceStaysWithTheShapesItFits(t *testing.T) {
	// A sub-agent's reply goes to its parent: nothing to tell a user.
	sub := AgentRecord{ID: "sub-1", OwnedBy: "parent-1"}
	blocks := frameworkPromptBlocks("", sub, false)
	for _, marker := range []string{cannotAuthorMarker, brokenToolMarker} {
		if strings.Contains(blocks, marker) {
			t.Errorf("a sub-agent got the %q block", marker)
		}
	}
	top := AgentRecord{ID: "top-1"}
	if !strings.Contains(frameworkPromptBlocks("", top, false), brokenToolMarker) {
		t.Error("a top-level agent lost the broken-tool block")
	}
	// Delegations show as cards now; the fleet block no longer says to hide them.
	src, err := os.ReadFile("runner_context_blocks.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "the user doesn't know the fleet structure") {
		t.Error("Available agents still tells the agent the person cannot see delegations")
	}
	// A plan worker does not inherit the lead's unkept promise.
	w, err := os.ReadFile("runner_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(w), `TurnNotes: func(user string) string { return turnNotes(ws.sess, t.udb, "", user) }`) {
		t.Error("a plan worker's notes are keyed by the chat session, which carries the lead's unkept promise")
	}
}

// One rule for who reads the reply: the guidance a person watching the web
// chat needs is worded for a channel conversation, and for a run nobody reads
// as it comes, instead of telling them to ask, approve in a pane, or write a
// plan nobody sees.
func TestGuidanceIsWordedForWhoReadsTheReply(t *testing.T) {
	chat := seedNamed(t, "Chat")
	web := appendAgentCapabilityBlocks("", chat, nil, "u", true, surfaceWeb)
	conv := appendAgentCapabilityBlocks("", chat, nil, "u", false, surfaceConversation)
	alone := appendAgentCapabilityBlocks("", chat, nil, "u", false, surfaceUnattended)

	if !strings.Contains(web, "ask_user / ask_user_form before building") || !strings.Contains(web, "Authorizations pane") {
		t.Error("the web lost its own wording")
	}
	for name, p := range map[string]string{"conversation": conv, "unattended": alone} {
		for _, gone := range []string{"ask_user / ask_user_form before building", "Authorizations pane", "lay it out briefly in your reply before acting"} {
			if strings.Contains(p, gone) {
				t.Errorf("%s run still says %q", name, gone)
			}
		}
	}
	if !strings.Contains(conv, "ask it yourself, in your reply") || !strings.Contains(conv, "must ask for, in your reply") {
		t.Error("a channel conversation is not told to ask in its reply")
	}
	if !strings.Contains(alone, "nobody is here to ask") || strings.Contains(alone, "ASK the user how they want to be alerted") {
		t.Error("an unattended run is still told to ask")
	}
	if surfaceForRun(AgentSyncRun{Kind: "channel"}) != surfaceConversation ||
		surfaceForRun(AgentSyncRun{Kind: "channel", InputReportFrom: "a monitor"}) != surfaceUnattended ||
		surfaceForRun(AgentSyncRun{}) != surfaceUnattended {
		t.Error("the surface of a run")
	}
}

// A persona section that needs a tool only the web mounts stays off a run
// elsewhere, even when the agent's own list allows it.
func TestAPersonaSectionForAWebOnlyToolStaysOnTheWeb(t *testing.T) {
	persona := "Intro.\n\n## Asking\n<!-- @requires-tools: ask_user -->\n\nCall ask_user.\n\n## Searching\n\nSearch first."
	agent := AgentRecord{AllowedTools: []string{"web_search"}}
	web := gatedPersonaOn(agent, persona, surfaceWeb)
	off := gatedPersonaOn(agent, persona, surfaceUnattended)
	if !strings.Contains(web, "Call ask_user.") || strings.Contains(web, "@requires") {
		t.Errorf("the web run: %q", web)
	}
	if strings.Contains(off, "Call ask_user.") || !strings.Contains(off, "Search first.") || strings.Contains(off, "@requires") {
		t.Errorf("a run off the web: %q", off)
	}
	open := gatedPersonaOn(AgentRecord{}, persona, surfaceConversation)
	if strings.Contains(open, "Call ask_user.") {
		t.Error("an agent with no tool list still got the ask section off the web")
	}
}
