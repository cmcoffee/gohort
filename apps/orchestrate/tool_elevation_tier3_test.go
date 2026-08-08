// Tier 3 exists because Tiers 1 and 2 both miss the ordinary case. Wren had
// used get_top_stories before; "what's happening in the world today" names
// neither the tool nor its credential host (Tier 1 misses), and a tool the
// model reaches by improvising a shell command never accumulates load_tool
// history (Tier 2 misses). So the schema stayed lazy, the model had a name
// with no way to call it, and it invented one.
package orchestrate

import (
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func tier3Turn(t *testing.T) (*chatTurn, Database) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = root
	t.Cleanup(func() { RootDB = prev })
	udb := UserDB(root, "u")
	rec, err := saveAgent(udb, AgentRecord{Name: "Wren", Owner: "u", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}
	return &chatTurn{udb: udb, agent: rec}, udb
}

func hasArgs(name string) AgentToolDef {
	return AgentToolDef{Tool: Tool{Name: name, Parameters: map[string]ToolParam{"category": {Type: "string"}}}}
}

// The whole point: one prior success, and the schema is there next time.
func TestOneSuccessIsEnoughToElevate(t *testing.T) {
	turn, udb := tier3Turn(t)
	recordToolSuccess(udb, turn.agent.ID, "sess-1", "get_top_stories")

	elevated := turn.elevatedToolSet(nil, []AgentToolDef{hasArgs("get_top_stories"), hasArgs("unused_tool")}, nil)
	if elevated["get_top_stories"] != "prior-success" {
		t.Fatalf("a tool this agent has used must be in the catalog: %v", elevated)
	}
	if elevated["unused_tool"] != "" {
		t.Errorf("never-called tools stay lazy: %v", elevated)
	}
}

// Recording is per (tool, session), so one chatty session is one signal — and
// re-recording must not disturb what is already there.
func TestSuccessRecordingIsIdempotentPerSession(t *testing.T) {
	turn, udb := tier3Turn(t)
	for i := 0; i < 5; i++ {
		recordToolSuccess(udb, turn.agent.ID, "sess-1", "get_top_stories")
	}
	hist := map[string][]toolLoadEntry{}
	udb.Get(toolSuccessHistoryTable, turn.agent.ID, &hist)
	if got := len(hist["get_top_stories"]); got != 1 {
		t.Errorf("5 calls in one session = %d entries, want 1", got)
	}
	recordToolSuccess(udb, turn.agent.ID, "sess-2", "get_top_stories")
	hist = map[string][]toolLoadEntry{}
	udb.Get(toolSuccessHistoryTable, turn.agent.ID, &hist)
	if got := len(hist["get_top_stories"]); got != 2 {
		t.Errorf("a second session should add a signal: got %d entries", got)
	}
}

// Unbounded elevation would re-create the prompt cost the lazy split exists
// to avoid. Most-recent-success wins the cap.
func TestElevationIsCappedMostRecentFirst(t *testing.T) {
	turn, udb := tier3Turn(t)
	hist := map[string][]toolLoadEntry{}
	var all []AgentToolDef
	base := time.Now().Add(-24 * time.Hour)
	// tool_00 oldest … tool_09 newest.
	for i := 0; i < 10; i++ {
		name := string(rune('a'+i)) + "_tool"
		hist[name] = []toolLoadEntry{{Session: "s", At: base.Add(time.Duration(i) * time.Hour)}}
		all = append(all, hasArgs(name))
	}
	udb.Set(toolSuccessHistoryTable, turn.agent.ID, hist)

	elevated := turn.elevatedToolSet(nil, all, nil)
	if len(elevated) != toolSuccessElevateCap {
		t.Fatalf("elevated %d, want the cap of %d: %v", len(elevated), toolSuccessElevateCap, elevated)
	}
	// The five newest are j,i,h,g,f — the oldest must have fallen off.
	for _, want := range []string{"j_tool", "i_tool", "h_tool", "g_tool", "f_tool"} {
		if elevated[want] != "prior-success" {
			t.Errorf("recent tool %q should have won the cap: %v", want, elevated)
		}
	}
	if elevated["a_tool"] != "" {
		t.Errorf("the oldest tool must fall off the cap: %v", elevated)
	}
}

// History outlives the tool. A name the agent no longer has must not be
// elevated, and must not consume a slot that a live tool could use.
func TestStaleHistoryNeitherElevatesNorEatsTheCap(t *testing.T) {
	turn, udb := tier3Turn(t)
	hist := map[string][]toolLoadEntry{}
	now := time.Now()
	// Six gone tools with the NEWEST timestamps, one live tool the oldest.
	for i := 0; i < 6; i++ {
		hist[string(rune('a'+i))+"_gone"] = []toolLoadEntry{{Session: "s", At: now.Add(time.Duration(i) * time.Minute)}}
	}
	hist["get_top_stories"] = []toolLoadEntry{{Session: "s", At: now.Add(-time.Hour)}}
	udb.Set(toolSuccessHistoryTable, turn.agent.ID, hist)

	elevated := turn.elevatedToolSet(nil, []AgentToolDef{hasArgs("get_top_stories")}, nil)
	if elevated["get_top_stories"] != "prior-success" {
		t.Fatalf("the one live tool must still elevate past six dead names: %v", elevated)
	}
	if len(elevated) != 1 {
		t.Errorf("nothing else exists to elevate: %v", elevated)
	}
}

// Elevation is visibility, never a vouch: a kit tool is already direct, a
// zero-arg tool was never lazy, and a Trial tool must not out-promote the
// confirmation gate.
func TestElevationSkipsKitZeroArgAndDoesNotDoubleCount(t *testing.T) {
	turn, udb := tier3Turn(t)
	recordToolSuccess(udb, turn.agent.ID, "s", "kit_tool")
	recordToolSuccess(udb, turn.agent.ID, "s", "noarg_tool")

	noArg := AgentToolDef{Tool: Tool{Name: "noarg_tool"}}
	elevated := turn.elevatedToolSet(nil, []AgentToolDef{hasArgs("kit_tool"), noArg}, map[string]bool{"kit_tool": true})
	if len(elevated) != 0 {
		t.Errorf("nothing here needs elevating: %v", elevated)
	}
}

// Tier 2's reason must survive — a tool that qualifies both ways keeps the
// first label rather than being relabelled and counted twice.
func TestTierTwoKeepsItsReason(t *testing.T) {
	turn, udb := tier3Turn(t)
	for _, s := range []string{"s1", "s2", "s3"} {
		recordToolLoads(udb, turn.agent.ID, s, []string{"get_top_stories"})
	}
	recordToolSuccess(udb, turn.agent.ID, "s1", "get_top_stories")

	elevated := turn.elevatedToolSet(nil, []AgentToolDef{hasArgs("get_top_stories")}, nil)
	if elevated["get_top_stories"] != "repeated-load" {
		t.Errorf("reason = %q, want repeated-load", elevated["get_top_stories"])
	}
}
