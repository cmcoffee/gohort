package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// request_build queues a build_agent authorization stamped with the requester,
// and refuses a blank brief. The approval side (Builder dispatch, OwnedBy
// stamping) is covered by the dispatch path; this locks the queue contract.
func TestRequestBuildQueuesAuthorization(t *testing.T) {
	prev := RootDB
	defer func() { RootDB = prev }()
	RootDB = &DBase{Store: kvlite.MemStore()}

	td := requestBuildTool("alice", "agent-moltbook", "Moltbook")

	// Blank brief refused.
	if _, err := td.Handler(map[string]any{"brief": "   "}); err == nil {
		t.Error("blank brief must be refused")
	}

	out, err := td.Handler(map[string]any{"brief": "A viral-post researcher for Moltbook.", "name": "Viral Researcher"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(out, "approval") {
		t.Errorf("result should tell the agent it's queued for approval: %q", out)
	}

	// Exactly one build_agent authorization, stamped with the requester + builder.
	found := 0
	for _, a := range ListAuthorizations(RootDB, "alice") {
		if a.Action == buildAgentAction {
			found++
			if a.FromAgent != "agent-moltbook" {
				t.Errorf("FromAgent should be the requester, got %q", a.FromAgent)
			}
			if a.Agent != "builder" {
				t.Errorf("approval should dispatch builder, got %q", a.Agent)
			}
			if !strings.Contains(a.Brief, "Viral Researcher") {
				t.Errorf("suggested name should ride in the brief: %q", a.Brief)
			}
		}
	}
	if found != 1 {
		t.Fatalf("expected exactly one build_agent authorization, got %d", found)
	}
}
