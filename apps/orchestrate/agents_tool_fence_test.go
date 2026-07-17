package orchestrate

import (
	"errors"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestAgentsToolOptsOutOfBlanketFence — the agents tool carries CapNetwork so
// Private mode strips it (a `run` could reach a sub-agent's web_search), but the
// untrusted fence keys off that SAME cap. Without the TrustedOutput opt-out,
// every list/get — pure reads of the user's own agent registry — got wrapped in
// "UNTRUSTED EXTERNAL CONTENT", telling an authoring agent to distrust the very
// records it was about to edit. Assert both halves: the cap stays (Private mode
// keeps working) AND the blanket fence is declined.
func TestAgentsToolOptsOutOfBlanketFence(t *testing.T) {
	def := (&chatTurn{}).agentsGroupedToolDef(true)

	if !toolCarriesNetworkCap(def.Tool) {
		t.Fatal("agents lost CapNetwork — Private mode would stop stripping it, letting a turn leak via a sub-agent dispatch")
	}
	if !def.Tool.TrustedOutput {
		t.Fatal("agents must set TrustedOutput so the blanket fence doesn't wrap internal list/get reads")
	}
	// The two must combine to "runner does not blanket-fence this tool" — the
	// exact condition at the fence site.
	if toolCarriesNetworkCap(def.Tool) && !def.Tool.TrustedOutput {
		t.Fatal("agents would still be blanket-fenced")
	}
}

// TestFenceAgentsOutput pins the per-action half: dispatch results get the
// fence, while errors and empty results pass through untouched.
func TestFenceAgentsOutput(t *testing.T) {
	out, err := fenceAgentsOutput("a sub-agent said something", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(out, untrustedContentFence) {
		t.Fatal("run/run_tool output must be fenced — it carries whatever a sub-agent produced, possibly from the web")
	}
	if !strings.HasSuffix(out, "a sub-agent said something") {
		t.Fatalf("fence must PREFIX the payload, leaving it intact; got %q", out)
	}

	// An error must not be fenced — that would bury the message behind a banner.
	sentinel := errors.New("dispatch blew up")
	if out, err := fenceAgentsOutput("", sentinel); err != sentinel || out != "" {
		t.Fatalf("error path should pass through untouched; got (%q, %v)", out, err)
	}
	if out, _ := fenceAgentsOutput("   ", nil); strings.Contains(out, untrustedContentFence) {
		t.Fatal("blank output should not be fenced — there is nothing to fence")
	}
}

// TestAgentsGetResolvesByName — agents(get) used to hard-require an id while the
// same tool's `agent` param documents "Name or id" for run/run_tool, so the
// natural first call was rejected and cost a list-then-get round trip.
func TestAgentsGetResolvesByName(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	const owner = "alice"
	udb := agentUserDB(root, owner)
	if udb == nil {
		t.Fatal("agentUserDB nil")
	}
	saved, err := saveAgent(udb, AgentRecord{
		Name: "OSINT Investigator", OrchestratorPrompt: "investigate", Owner: owner,
	})
	if err != nil {
		t.Fatalf("saveAgent: %v", err)
	}
	turn := &chatTurn{udb: udb, user: owner}

	// By name — the call that used to fail.
	byName, err := turn.agentsGetAction(map[string]any{"id": "OSINT Investigator"})
	if err != nil {
		t.Fatalf("agents(get) by name: %v", err)
	}
	if !strings.Contains(byName, saved.ID) {
		t.Fatalf("get by name did not return the agent record; got %q", byName)
	}

	// Via the `agent` key, which run/run_tool use — accepted interchangeably.
	viaAgentKey, err := turn.agentsGetAction(map[string]any{"agent": "OSINT Investigator"})
	if err != nil {
		t.Fatalf("agents(get) via agent key: %v", err)
	}
	if !strings.Contains(viaAgentKey, saved.ID) {
		t.Fatal("get via the agent key did not return the agent record")
	}

	// By id still works — the fix must not regress the documented path.
	byID, err := turn.agentsGetAction(map[string]any{"id": saved.ID})
	if err != nil {
		t.Fatalf("agents(get) by id: %v", err)
	}
	if !strings.Contains(byID, saved.ID) {
		t.Fatal("get by id regressed")
	}

	// A name that matches nothing must still 404 rather than resolve to junk.
	if _, err := turn.agentsGetAction(map[string]any{"id": "No Such Agent"}); err == nil {
		t.Fatal("expected not-found for an unknown name")
	}
	// Neither key supplied — the error should name both options.
	if _, err := turn.agentsGetAction(map[string]any{}); err == nil {
		t.Fatal("expected an error when neither id nor agent is supplied")
	}
}
