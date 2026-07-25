package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestIntentMentionsTool(t *testing.T) {
	cases := []struct {
		intent, name, host string
		want               bool
	}{
		{"browse the moltbook feeds and reply to posts", "moltbook", "", true},
		{"check the Get Feed results", "get_feed", "", true}, // underscore-as-space
		{"post the daily update to www.moltbook.com", "moltbook_poster", "www.moltbook.com", true},
		{"summarize today's news", "moltbook", "", false},
		{"", "moltbook", "", false},
	}
	for _, c := range cases {
		if got := intentMentionsTool(strings.ToLower(c.intent), c.name, c.host); got != c.want {
			t.Errorf("intent=%q name=%q host=%q: got %v want %v", c.intent, c.name, c.host, got, c.want)
		}
	}
}

// TestRepeatedLoadPromotion pins Tier 2: three DISTINCT sessions promote;
// repeat loads within one session are one signal; crossing the threshold
// queues exactly one scope suggestion.
func TestRepeatedLoadPromotion(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	udb := UserDB(root, "u")
	rec, err := saveAgent(udb, AgentRecord{Name: "Molt", Owner: "u", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}

	recordToolLoads(udb, rec.ID, "sess-1", []string{"moltbook"})
	recordToolLoads(udb, rec.ID, "sess-1", []string{"moltbook"}) // same session — no extra signal
	recordToolLoads(udb, rec.ID, "sess-2", []string{"moltbook"})
	if p := promotedByLoadHistory(udb, rec.ID); p["moltbook"] {
		t.Fatal("two distinct sessions must not promote yet")
	}
	recordToolLoads(udb, rec.ID, "sess-3", []string{"moltbook"})
	if p := promotedByLoadHistory(udb, rec.ID); !p["moltbook"] {
		t.Fatal("three distinct sessions should promote")
	}

	// The threshold crossing queued ONE scope suggestion, deduped thereafter.
	count := func() int {
		n := 0
		for _, a := range ListAuthorizations(RootDB, "u") {
			if a.Action == "scope_tool" && a.Agent == rec.ID && a.Brief == "moltbook" {
				n++
			}
		}
		return n
	}
	if got := count(); got != 1 {
		t.Fatalf("want exactly 1 scope suggestion, got %d", got)
	}
	recordToolLoads(udb, rec.ID, "sess-4", []string{"moltbook"})
	if got := count(); got != 1 {
		t.Fatalf("suggestion must not duplicate; got %d", got)
	}
}

// TestElevatedToolSetMentionCap pins Tier 1: intent-named lazy tools elevate,
// capped, deterministically by name; kit tools are skipped.
func TestElevatedToolSetMentionCap(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	rec, err := saveAgent(udb, AgentRecord{Name: "Molt", Owner: "u", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}
	turn := &chatTurn{udb: udb, agent: rec}
	turn.intentText = "use tool_a tool_b tool_c tool_d and also moltbook"
	mk := func(name string) AgentToolDef {
		return AgentToolDef{Tool: Tool{Name: name, Parameters: map[string]ToolParam{"x": {Type: "string"}}}}
	}
	all := []AgentToolDef{mk("tool_a"), mk("tool_b"), mk("tool_c"), mk("tool_d"), mk("moltbook"), mk("unrelated")}
	kit := map[string]bool{"tool_a": true} // already direct — not counted against the cap
	elevated := turn.elevatedToolSet(nil, all, kit)
	if len(elevated) != toolElevateMentionCap {
		t.Fatalf("want %d elevations, got %d: %v", toolElevateMentionCap, len(elevated), elevated)
	}
	if elevated["tool_a"] != "" {
		t.Fatal("kit tool must not be elevated")
	}
	if elevated["unrelated"] != "" {
		t.Fatal("unmentioned tool must not be elevated")
	}
	// Deterministic under the cap: sorted names → moltbook, tool_b, tool_c win.
	for _, want := range []string{"moltbook", "tool_b", "tool_c"} {
		if elevated[want] != "mentioned" {
			t.Fatalf("expected %q elevated; got %v", want, elevated)
		}
	}
}
