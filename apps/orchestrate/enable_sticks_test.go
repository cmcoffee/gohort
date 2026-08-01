package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Two gates decide whether a pool tool is on for an agent: the allow-list has
// to name it (or be empty, meaning "everything"), and the deny list has to stay
// silent about it. Every surface that offers to turn a tool ON has to satisfy
// BOTH, because a surface that satisfies one reports success and changes
// nothing the reader can see — the switch is back off the next time they look.
//
// These are the two that didn't, both reported the same way: "I set it to
// enable, go back and it's disabled again."

// The Tools modal on an agent the user made themselves. Seeds got this fix in
// v0.5.698/699; a user-crafted agent gates its catalog through AllowedTools, so
// nothing ever recomputed its deny list — and the Scope pill writes into that
// deny list whenever the agent has no allow-list to trim. Re-checking the box
// saved an allow-list the stale deny entry then overrode, forever.
func TestToolsModalCanReEnableADeniedToolOnACustomAgent(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	prevRoot := RootDB
	RootDB = root
	t.Cleanup(func() { RootDB = prevRoot })

	mk := func(name string) TempTool {
		return TempTool{Name: name, Description: "d", CommandTemplate: "echo hi"}
	}
	for _, n := range []string{"shared_tool", "other_tool", "scoped_tool"} {
		if err := AdminPersistTempTool(udb, "u", mk(n)); err != nil {
			t.Fatal(err)
		}
	}
	if !SetUserToolScopeAgents(udb, "u", "scoped_tool", []string{"my-agent"}) {
		t.Fatal("scope write failed")
	}

	denied := func(rec AgentRecord) map[string]bool {
		got := map[string]bool{}
		for _, n := range rec.DisabledPersistentTools {
			got[n] = true
		}
		return got
	}

	// The owner ticks shared_tool along with a curated set of others. The
	// checkbox is the whole point of the modal: it has to clear the denial.
	rec := AgentRecord{
		ID: "my-agent", Owner: "u", Name: "Mine",
		AllowedTools:            []string{"web_search", "shared_tool"},
		DisabledPersistentTools: []string{"shared_tool", "other_tool", "scoped_tool"},
	}
	curateToolsFromModal(root, "u", &rec)
	got := denied(rec)
	if got["shared_tool"] {
		t.Error("a tool the owner just CHECKED must not stay in the deny list — that is the enable that never sticks")
	}
	if !got["other_tool"] {
		t.Error("a denial for a tool this save said nothing about is not this save's business")
	}
	if !got["scoped_tool"] {
		t.Error("the scoped group sends its own decisions in this same field — they must survive")
	}
	if len(rec.AllowedTools) != 2 {
		t.Errorf("a custom agent's literal allow-list is preserved verbatim, got %v", rec.AllowedTools)
	}

	// Everything checked collapses to the empty default-pool list, so the
	// picked names are gone from the payload — the denials have to clear on the
	// strength of the collapse alone, or all-on would be the one state you
	// cannot reach.
	rec = AgentRecord{
		ID: "my-agent", Owner: "u", Name: "Mine",
		DisabledPersistentTools: []string{"shared_tool", "scoped_tool"},
	}
	curateToolsFromModal(root, "u", &rec)
	got = denied(rec)
	if got["shared_tool"] {
		t.Error("all catalog boxes checked means no pool tool is off")
	}
	if !got["scoped_tool"] {
		t.Error("the scoped group is not part of what 'all checked' describes")
	}

	// The no-tools sentinel is a decision, not a checklist. Nothing to
	// translate, and nothing may be quietly re-enabled underneath it.
	rec = AgentRecord{
		ID: "my-agent", Owner: "u", Name: "Mine",
		AllowedTools:            []string{noToolsSentinel},
		DisabledPersistentTools: []string{"shared_tool"},
	}
	curateToolsFromModal(root, "u", &rec)
	if !denied(rec)["shared_tool"] || len(rec.AllowedTools) != 1 {
		t.Errorf("the sentinel save must pass through untouched, got %v / %v", rec.AllowedTools, rec.DisabledPersistentTools)
	}
}

// The Scope pill. Clearing the deny list and appending to the allow-list were
// alternatives — first one wins, return — so an agent carrying both a denial
// and a curated allow-list could never be granted the tool: 204 OK, pill off
// again on reload.
func TestScopePillOnClearsBothGates(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	prevRoot := RootDB
	RootDB = root
	t.Cleanup(func() { RootDB = prevRoot })
	udb := agentUserDB(root, "u")

	// Both are real pool tools: saveAgent heals an allow-list by dropping names
	// that resolve to nothing, so a placeholder here would be silently removed
	// and the test would be measuring the healer instead.
	for _, n := range []string{"shared_tool", "keep_tool"} {
		if err := AdminPersistTempTool(UserDB(root, "u"), "u", TempTool{
			Name: n, Description: "d", CommandTemplate: "echo hi",
		}); err != nil {
			t.Fatal(err)
		}
	}
	rec := AgentRecord{
		ID: "my-agent", Owner: "u", Name: "Mine", OrchestratorPrompt: "x",
		AllowedTools:            []string{"keep_tool"},
		DisabledPersistentTools: []string{"shared_tool"},
	}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}

	if err := attachGlobalToolToAgent(root, "u", "my-agent", "shared_tool"); err != nil {
		t.Fatalf("attach: %v", err)
	}
	after, ok := loadAgent(udb, "my-agent")
	if !ok {
		t.Fatal("agent vanished")
	}
	if !agentSeesGlobalTool(after, "shared_tool") {
		t.Errorf("the pill reported ON, so the agent must actually see the tool: allow=%v deny=%v",
			after.AllowedTools, after.DisabledPersistentTools)
	}
	if len(after.AllowedTools) != 2 {
		t.Errorf("granting a tool must not disturb the rest of the allow-list, got %v", after.AllowedTools)
	}

	// Idempotent: a second ON is a no-op, not a duplicate entry.
	if err := attachGlobalToolToAgent(root, "u", "my-agent", "shared_tool"); err != nil {
		t.Fatalf("re-attach: %v", err)
	}
	again, _ := loadAgent(udb, "my-agent")
	if len(again.AllowedTools) != 2 {
		t.Errorf("re-granting appended a duplicate: %v", again.AllowedTools)
	}

	// An agent pinned to zero tools is refused OUT LOUD. Accepting the toggle
	// and storing nothing is exactly the failure this file exists to close.
	pinned := AgentRecord{
		ID: "pinned", Owner: "u", Name: "Pinned", OrchestratorPrompt: "x",
		AllowedTools: []string{noToolsSentinel},
	}
	if _, err := saveAgent(udb, pinned); err != nil {
		t.Fatal(err)
	}
	err := attachGlobalToolToAgent(root, "u", "pinned", "shared_tool")
	if err == nil {
		t.Fatal("granting a tool to a no-tools agent must say why it cannot, not report success")
	}
	if !strings.Contains(err.Error(), "Pinned") {
		t.Errorf("the message should name the agent the user clicked, got %q", err)
	}
}
