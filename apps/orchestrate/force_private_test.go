package orchestrate

import (
	"context"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
)

// An app agent declares ForcePrivate in its SPEC because its per-user shadow
// cannot be trusted to carry it: a minimal shadow minted by a tool-approval
// (the bundle path's saveAgent) has ForcePrivate=false, and the shadow refresh
// rebased six other framework-owned fields while never touching this one.
//
// Only the model-routing gate consulted the spec. The two paths that actually
// strip network access read the record alone, so such a shadow was correctly
// denied a remote lead model while keeping fetch_url / web_search /
// browse_page in its catalog behind an open connector. For an agent holding SSH
// credentials and log contents, that is the wrong half to enforce.
func TestForcePrivateEnforcesFromTheSpecNotJustTheRecord(t *testing.T) {
	const id = "test-forceprivate-appagent"
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: id, Name: "Investigator", Hidden: true, ForcePrivate: true,
	})

	// The stale shadow: the spec says private, this user's record does not.
	stale := AgentRecord{ID: id, Name: "Investigator", ForcePrivate: false}
	if !agentForcesPrivate(stale) {
		t.Fatal("a shadow whose spec declares ForcePrivate must be treated as private")
	}

	// The record's own flag still stands on its own, for ordinary agents that
	// have no spec at all.
	if !agentForcesPrivate(AgentRecord{ID: "ordinary", ForcePrivate: true}) {
		t.Error("a record-level ForcePrivate must still be honoured")
	}
	if agentForcesPrivate(AgentRecord{ID: "ordinary"}) {
		t.Error("an agent that is private by neither route must not be forced private")
	}

	// The enforcement that matters: network-capable tools are stripped from the
	// stale shadow, not merely denied a lead model.
	netTool := AgentToolDef{Tool: Tool{
		Name: "fetch_url", Description: "fetch", Caps: []Capability{CapNetwork},
	}}
	plain := AgentToolDef{Tool: Tool{Name: "list_sections", Description: "list"}}
	_, tools := applyForcePrivateToDispatch(context.Background(), &ToolSession{}, []AgentToolDef{netTool, plain}, stale)
	for _, td := range tools {
		if td.Tool.Name == "fetch_url" {
			t.Fatal("a spec-private agent kept a network tool in its dispatch catalog")
		}
	}
	if len(tools) != 1 {
		t.Fatalf("stripped %d tool(s); only the network-capable one should go", 2-len(tools))
	}
}
