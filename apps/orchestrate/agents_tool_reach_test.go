package orchestrate

// The `agents` tool is offered when there is something to reach, and not
// otherwise. A catalog entry reading "delegate work and get the result back",
// on a turn where every dispatch is refused, is an invitation the gate then
// declines — round after round, until the turn dies on the escalation counter.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// reachTurn builds a turn against a store holding the given peers.
func reachTurn(t *testing.T, self AgentRecord, peers ...AgentRecord) *chatTurn {
	t.Helper()
	udb := &DBase{Store: kvlite.MemStore()}
	self.Owner = "someuser"
	if self.ID == "" {
		self.ID = "a1"
	}
	_, _ = saveAgent(udb, self)
	for _, p := range peers {
		p.Owner = "someuser"
		_, _ = saveAgent(udb, p)
	}
	return &chatTurn{app: &OrchestrateApp{}, agent: self, udb: udb, user: "someuser"}
}

func TestRunIsAdvertisedOnlyWhenSomethingIsReachable(t *testing.T) {
	withPeer := reachTurn(t, AgentRecord{ID: "a1", Name: "Lead"}, AgentRecord{ID: "a2", Name: "Researcher"})
	if !withPeer.canDispatchAnything() {
		t.Fatal("a fleet with another agent in it is reachable")
	}
	def := withPeer.agentsGroupedToolDef(true)
	if _, ok := def.Tool.Parameters["agent"]; !ok {
		t.Error("run must be advertised when a peer exists")
	}
	if !strings.Contains(def.Tool.Parameters["action"].Description, "run") {
		t.Errorf("the action enum must offer run: %s", def.Tool.Parameters["action"].Description)
	}

	// Allow none. The gate refuses every dispatch, so the schema must stop
	// describing one — this is the agent that keeps trying and keeps being told
	// no.
	none := reachTurn(t, AgentRecord{ID: "a1", Name: "Lead", DispatchMode: dispatchNone},
		AgentRecord{ID: "a2", Name: "Researcher"})
	if none.canDispatchAnything() {
		t.Fatal("Allow none reaches nothing")
	}
	def = none.agentsGroupedToolDef(true)
	if _, ok := def.Tool.Parameters["agent"]; ok {
		t.Error("run advertised to an agent whose every dispatch the gate refuses")
	}
	if strings.Contains(def.Tool.Parameters["action"].Description, "run") {
		t.Errorf("the action enum still names run: %s", def.Tool.Parameters["action"].Description)
	}
	// Private mode's lever comes off with it: the cap is only there because a
	// `run` can reach a sub-agent's web_search, and there is no run here.
	if toolCarriesNetworkCap(def.Tool) {
		t.Error("a read-only variant should not claim network reach")
	}

}

// A fleet of one — the state every new install starts in. Nothing is wrong with
// the policy; there is simply nobody else, and the tool has nothing to offer.
//
// Asserted on the SET rather than on canDispatchAnything, because the seed
// registry this package shares is written by other tests in it, and an app
// agent one of them registers is a real peer as far as the fleet is concerned.
func TestAFleetOfOneReachesNobody(t *testing.T) {
	turn := reachTurn(t, AgentRecord{ID: "a1", Name: "Lead"})
	for _, a := range turn.dispatchableFleet() {
		if a.ID == "a1" {
			t.Error("an agent must not count itself as a dispatch target")
		}
	}
	if len(turn.dispatchablePipelines()) != 0 || len(turn.dispatchableMachines()) != 0 {
		t.Error("no pipelines or machines were saved; none should be reachable")
	}
}

// Reach is not the only reason to carry the tool: list and get READ the fleet,
// which is what an authoring agent works on.
func TestAuthoringAgentKeepsTheReadOnlyTool(t *testing.T) {
	author := reachTurn(t, AgentRecord{ID: "seed-builder", Name: "Builder", DispatchMode: dispatchNone})
	if !author.agentsToolWanted() {
		t.Fatal("an authoring agent needs list/get even with nothing to dispatch to")
	}
	if author.canDispatchAnything() {
		t.Error("...but it still cannot dispatch")
	}

	plain := reachTurn(t, AgentRecord{ID: "a1", Name: "Helper", DispatchMode: dispatchNone})
	if plain.agentsToolWanted() {
		t.Error("an agent that can neither dispatch nor author has no use for the tool")
	}
}

// A turn assembled for its SCHEMA alone — the catalog picker, a test — has no
// fleet to read and is not asking what it can reach. It gets the full surface.
func TestSchemaOnlyAssemblyDescribesTheWholeTool(t *testing.T) {
	def := (&chatTurn{}).agentsGroupedToolDef(true)
	if _, ok := def.Tool.Parameters["agent"]; !ok {
		t.Fatal("the schema-only variant must still describe run")
	}
	if !(&chatTurn{}).agentsToolWanted() {
		t.Error("the schema-only variant must still be wanted")
	}
}

// The gate reads reach off the SAME helpers the advertisement surfaces use, so
// an agent whose allowlist names no agent at all — only a pipeline — keeps run.
func TestAPipelineIsReachEnough(t *testing.T) {
	turn := reachTurn(t, AgentRecord{ID: "a1", Name: "Lead",
		DispatchMode: dispatchOnly, AllowedDispatchTargets: []string{"Weekly digest"}})
	SavePipelineDef(turn.udb, PipelineDef{ID: "p1", Name: "Weekly digest", Owner: "someuser"})
	if len(turn.dispatchableFleet()) != 0 {
		t.Fatalf("the allowlist names no agent; fleet = %+v", turn.dispatchableFleet())
	}
	if len(turn.dispatchablePipelines()) == 0 {
		t.Fatal("the allowlist names the pipeline by the name the author knows it by")
	}
	if !turn.canDispatchAnything() {
		t.Error("a runnable pipeline is a dispatch target; run must stay advertised")
	}
	if _, ok := turn.agentsGroupedToolDef(true).Tool.Parameters["pipeline"]; !ok {
		t.Error("run must still describe the pipeline target")
	}
}
