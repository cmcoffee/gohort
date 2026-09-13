package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func parentTrailOf(db Database, agentID, sessionID string) []SessionDiag {
	var list []SessionDiag
	db.Get(sessionDiagTable, agentID+":"+sessionID, &list)
	return list
}

// A dispatched turn's breadcrumb lands in its own sub-session trail AND in
// the conversation it descends from, tagged with the sub-agent's name — the
// only trail a person can open.
func TestDispatchedDiagMirrorsIntoTheParentConversation(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	parentCtx := withDiagParent(context.Background(), "lead", "conv-1")

	child := &chatTurn{
		agent: AgentRecord{ID: "researcher", Name: "Researcher"},
		udb:   udb,
		ctx:   parentCtx, // inherited from the parent's dispatch
	}
	child.beginDispatchDiag("researcher", "sub-9")
	child.turnDiag("guardrail-halted", "rule 'no external posts' stopped the reply")

	own := parentTrailOf(udb, "researcher", "sub-9")
	if len(own) == 0 || own[len(own)-1].Kind != "guardrail-halted" {
		t.Fatalf("own trail = %+v", own)
	}
	parent := parentTrailOf(udb, "lead", "conv-1")
	if len(parent) != 1 || parent[0].Kind != "guardrail-halted" || !strings.HasPrefix(parent[0].Detail, "↳ Researcher: ") {
		t.Fatalf("parent trail = %+v", parent)
	}
	// machine_not_on_dispatch, filed by beginDispatchDiag itself, mirrors too.
	withMachine := &chatTurn{agent: AgentRecord{ID: "planner", Name: "Planner", Machine: "m-1"}, udb: udb, ctx: parentCtx}
	withMachine.beginDispatchDiag("planner", "sub-10")
	found := false
	for _, d := range parentTrailOf(udb, "lead", "conv-1") {
		if d.Kind == "machine_not_on_dispatch" && strings.Contains(d.Detail, "↳ Planner") {
			found = true
		}
	}
	if !found {
		t.Fatalf("machine_not_on_dispatch not mirrored: %+v", parentTrailOf(udb, "lead", "conv-1"))
	}
}

// A live turn is its own conversation and never mirrors; a dispatch with no
// stamp on its context has nowhere to mirror to and writes only its own trail.
func TestDiagMirrorStaysQuietWithoutAParent(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	live := &chatTurn{agent: AgentRecord{ID: "lead", Name: "Lead"}, udb: udb, ctx: withDiagParent(context.Background(), "lead", "conv-1"),
		session: &ChatSession{ID: "conv-1", AgentID: "lead"}}
	live.turnDiag("spend-cap", "daily cap reached")
	if got := parentTrailOf(udb, "lead", "conv-1"); len(got) != 1 || strings.HasPrefix(got[0].Detail, "↳") {
		t.Fatalf("live turn should write once, untagged: %+v", got)
	}

	orphan := &chatTurn{agent: AgentRecord{ID: "researcher", Name: "Researcher"}, udb: udb, ctx: context.Background()}
	orphan.beginDispatchDiag("researcher", "sub-2")
	orphan.turnDiag("tool-denied", "x")
	if got := parentTrailOf(udb, "researcher", "sub-2"); len(got) != 1 {
		t.Fatalf("orphan own trail = %+v", got)
	}
	if got := parentTrailOf(udb, "lead", "conv-1"); len(got) != 1 {
		t.Fatalf("unstamped dispatch must not reach the conversation: %+v", got)
	}
}

// Depth: a grandchild dispatched from a child still lands in the ROOT
// conversation, because only the live turn stamps.
func TestDiagMirrorReachesTheRootAcrossDepth(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	root := withDiagParent(context.Background(), "lead", "conv-1")
	grandchild := &chatTurn{agent: AgentRecord{ID: "fetcher", Name: "Fetcher"}, udb: udb, ctx: root}
	grandchild.beginDispatchDiag("fetcher", "sub-sub-3")
	grandchild.turnDiag("provider-refusal", "429")
	if got := parentTrailOf(udb, "lead", "conv-1"); len(got) != 1 || !strings.Contains(got[0].Detail, "↳ Fetcher: 429") {
		t.Fatalf("root trail = %+v", got)
	}
}
