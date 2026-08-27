package guides

import (
	"context"
	"testing"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// ctxProbeSource is a reference source that records the session it was handed
// when its item tools were minted. Stands in for servitor, whose
// investigate_<system> tool dispatches a sub-run that must die with the turn.
type ctxProbeSource struct{ got **ToolSession }

func (s ctxProbeSource) Kind() string  { return "ctxprobe" }
func (s ctxProbeSource) Label() string { return "Ctx Probe" }
func (s ctxProbeSource) List(string) []ReferenceItem {
	return []ReferenceItem{{ID: "item1", Name: "Probe One"}}
}
func (s ctxProbeSource) Fetch(context.Context, string, string, string) string { return "" }

func (s ctxProbeSource) ItemToolsWithSession(sess *ToolSession, user, itemID string) []AgentToolDef {
	*s.got = sess
	return []AgentToolDef{{Tool: Tool{Name: "probe_" + itemID}}}
}

// TestCoauthorToolsRootAttachedSourcesOnTheTurn — guides builds its app tools
// OUTSIDE the run (the contract is a plain []AgentToolDef), so it used to mint
// attached-source tools through the session-less ReferenceItemTools. A source
// whose tool dispatches its own sub-run then rooted that run on
// context.Background(): stopping the guides chat stopped the chat and left a
// servitor investigation running against the live machine, reporting to nobody.
func TestCoauthorToolsRootAttachedSourcesOnTheTurn(t *testing.T) {
	var got *ToolSession
	RegisterReferenceSource(ctxProbeSource{got: &got})

	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	saveGuide(udb, Guide{
		ID: "g1", Owner: "u", Title: "G",
		References: []ReferenceSelection{{Kind: "ctxprobe", ItemID: "item1"}},
	})
	udb.Set(activeTable, "current", "g1")

	T := &Guides{AppCore: AppCore{DB: root}}
	orch := &orchestrate.OrchestrateApp{AppCore: AppCore{DB: root}}

	ctx, cancel := context.WithCancel(context.Background())
	tools := T.coauthorTools(ctx, udb, orch, "u", true)

	var minted bool
	for _, td := range tools {
		if td.Tool.Name == "probe_item1" {
			minted = true
		}
	}
	if !minted {
		t.Fatal("the attached source contributed no tools — the rest of this test proves nothing")
	}
	if got == nil {
		t.Fatal("attached-source tools were minted with a NIL session; a sub-run they dispatch cannot be canceled with the turn")
	}
	// The turn's own context, not a detached one: canceling the turn must reach
	// anything the source rooted on what it was given.
	cancel()
	select {
	case <-got.Context().Done():
	default:
		t.Error("canceling the turn did not reach the context handed to the source's tools")
	}
}

// TestGuideDispatchFollowsAttachedAgents — the Guide Author reached the user's
// whole fleet. Two defaults conspired: the `agents` grouped tool is a framework
// tool appended regardless of AllowedTools, and an app-agent spec naming no
// DispatchMode resolves to "all". So it dispatched to agents the guide had
// never attached as Sources, which is where the owner says what backs a guide.
func TestGuideDispatchFollowsAttachedAgents(t *testing.T) {
	// No guide open, and a guide with no agent Sources: both mean no dispatch.
	// "none" specifically, NOT an empty allowlist — effectiveDispatchMode reads
	// an empty AllowedDispatchTargets as the default, which is "all".
	for _, g := range []Guide{{}, {ID: "g", References: []ReferenceSelection{
		{Kind: "system", ItemID: "mft"},
		{Kind: "collection", ItemID: "c1"},
	}}} {
		mode, targets := guideDispatchPolicy(g)
		if mode != orchestrate.DispatchNone {
			t.Errorf("guide with no agent Sources got mode %q, want %q — an empty allowlist reads as ALL", mode, orchestrate.DispatchNone)
		}
		if len(targets) != 0 {
			t.Errorf("targets = %v, want none", targets)
		}
	}

	// Attached agents, and only those. Non-agent Sources are not dispatch
	// targets, and a blank id never becomes one.
	g := Guide{ID: "g", References: []ReferenceSelection{
		{Kind: "system", ItemID: "mft"},
		{Kind: orchestrate.AgentReferenceKind, ItemID: "agent-a"},
		{Kind: orchestrate.AgentReferenceKind, ItemID: "  "},
		{Kind: orchestrate.AgentReferenceKind, ItemID: "agent-b"},
	}}
	mode, targets := guideDispatchPolicy(g)
	if mode != orchestrate.DispatchOnly {
		t.Errorf("mode = %q, want %q", mode, orchestrate.DispatchOnly)
	}
	want := map[string]bool{"agent-a": true, "agent-b": true}
	if len(targets) != len(want) {
		t.Fatalf("targets = %v, want exactly %v", targets, want)
	}
	for _, id := range targets {
		if !want[id] {
			t.Errorf("targets include %q, which the guide never attached", id)
		}
	}
}
