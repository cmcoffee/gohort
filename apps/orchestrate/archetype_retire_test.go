package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestMaterializeArchetypeAgent pins the retirement safety net: a virgin
// retiring seed materializes into a user-owned agent carrying the seed's
// config and name; the operation is idempotent; a shadow or a non-seed target
// passes through materializeIfRetiringSeed unchanged.
func TestMaterializeArchetypeAgent(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")

	// The virgin seed resolves in a bare store (in-code seed), Owner=system.
	seed, ok := findAgentByNameOrID(udb, "u", "Research")
	if !ok || seed.ID != "seed-research" || seed.Owner != seedOwner {
		t.Fatalf("virgin Research should resolve to the seed; got id=%q owner=%q ok=%v", seed.ID, seed.Owner, ok)
	}

	// materializeIfRetiringSeed swaps the virgin seed for a user-owned copy.
	got := materializeIfRetiringSeed(udb, "u", seed)
	if got.ID == "seed-research" || got.Owner != "u" {
		t.Fatalf("virgin seed should materialize to a user-owned agent; got id=%q owner=%q", got.ID, got.Owner)
	}
	if got.Name != "Research" {
		t.Fatalf("materialized agent must keep the name for resolution; got %q", got.Name)
	}
	if len(got.AllowedTools) == 0 {
		t.Fatal("materialized Research should carry the seed's curated toolset")
	}

	// Idempotent: a second call (and a by-id dispatch) returns the SAME copy.
	again := materializeIfRetiringSeed(udb, "u", seed)
	if again.ID != got.ID {
		t.Fatalf("materialize must be idempotent; first=%q second=%q", got.ID, again.ID)
	}
	// After materialize, "Research" resolves to the user-owned copy, not the seed.
	if r, _ := findAgentByNameOrID(udb, "u", "Research"); r.ID != got.ID {
		t.Fatalf("name should now resolve to the materialized copy; got %q", r.ID)
	}

	// A non-seed target is untouched.
	normal := AgentRecord{ID: "abc", Owner: "u", Name: "Normal"}
	if out := materializeIfRetiringSeed(udb, "u", normal); out.ID != "abc" {
		t.Fatalf("non-seed target must pass through unchanged; got %q", out.ID)
	}
}

// TestShadowedRetiringSeedNotMaterialized pins that a user who SHADOWED the
// seed (customized it — their row is Owner=user at the seed id) is left
// untouched: no duplicate, no lost customization.
func TestShadowedRetiringSeedNotMaterialized(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	// Save a shadow: a user-owned row at the seed id.
	shadow := AgentRecord{ID: "seed-research", Owner: "u", Name: "Research",
		OrchestratorPrompt: "my custom research persona", AllowedTools: []string{"web_search"}}
	if _, err := saveAgent(udb, shadow); err != nil {
		t.Fatalf("save shadow: %v", err)
	}
	resolved, ok := findAgentByNameOrID(udb, "u", "seed-research")
	if !ok || resolved.Owner != "u" {
		t.Fatalf("shadow should resolve as user-owned; got owner=%q ok=%v", resolved.Owner, ok)
	}
	// materializeIfRetiringSeed leaves the shadow alone (Owner != seedOwner):
	// no new agent minted, the id stays the seed id. (The prompt is governed
	// by the seed-field merge in loadAgent, not by materialize — out of scope
	// here.)
	out := materializeIfRetiringSeed(udb, "u", resolved)
	if out.ID != "seed-research" || out.Owner != "u" {
		t.Fatalf("shadow must pass through unmaterialized; got id=%q owner=%q", out.ID, out.Owner)
	}
	// And no duplicate user-owned "Research" was created.
	dupes := 0
	for _, a := range listAgents(udb, "u") {
		if a.Name == "Research" {
			dupes++
		}
	}
	if dupes != 1 {
		t.Fatalf("shadow dispatch must not create a duplicate Research; found %d", dupes)
	}
}

// TestRetiringSeedsFilteredFromDispatchPicker pins that the dispatch-target
// picker no longer offers the retiring seeds (they're materialized on
// dispatch, not chosen as targets), while a real agent stays.
func TestRetiringSeedsFilteredFromDispatchPicker(t *testing.T) {
	if !isRetiringArchetypeSeed("seed-research") || !isRetiringArchetypeSeed("seed-kb") {
		t.Fatal("seed-research and seed-kb must be retiring archetype seeds")
	}
	if isRetiringArchetypeSeed("seed-chat") || isRetiringArchetypeSeed("seed-builder") {
		t.Fatal("only research/kb are retiring archetype seeds")
	}
}
