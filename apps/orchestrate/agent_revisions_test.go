package orchestrate

import (
	"strconv"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/revisions"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Editing an agent used to be a straight overwrite: after a behaviour
// regression there was no answer to "what did this say last week" and no way
// back. One authoring session burned four saves and every one was final.
func TestSavingAnAgentKeepsThePriorVersion(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}

	created, err := saveAgent(db, AgentRecord{Name: "Scout", OrchestratorPrompt: "answer briefly"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// A creation replaces nothing, so it has no history of its own.
	if revs := revisions.List(db, revisions.KindAgent, created.ID); len(revs) != 0 {
		t.Fatalf("a new agent starts with history: %+v", revs)
	}

	edited := created
	edited.OrchestratorPrompt = "answer at length"
	if _, err := saveAgentAs(db, edited, "edited instructions"); err != nil {
		t.Fatalf("edit: %v", err)
	}

	revs := revisions.List(db, revisions.KindAgent, created.ID)
	if len(revs) != 1 {
		t.Fatalf("got %d revisions, want 1", len(revs))
	}
	if revs[0].Reason != "edited instructions" {
		t.Errorf("reason = %q", revs[0].Reason)
	}
	var back AgentRecord
	if !revisions.Load(db, revisions.KindAgent, created.ID, "", &back) {
		t.Fatal("the kept version did not load")
	}
	if back.OrchestratorPrompt != "answer briefly" {
		t.Errorf("kept prompt = %q, want the version that was replaced", back.OrchestratorPrompt)
	}
}

// A ring six deep cannot afford writes that changed nothing. A re-save with no
// edit in it, which a debounced editor produces per keystroke pause, would
// otherwise evict every version worth keeping.
func TestResavingAnUnchangedAgentFilesNothing(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	created, err := saveAgent(db, AgentRecord{Name: "Scout", OrchestratorPrompt: "answer briefly"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := saveAgent(db, created); err != nil {
			t.Fatalf("re-save: %v", err)
		}
	}
	if revs := revisions.List(db, revisions.KindAgent, created.ID); len(revs) != 0 {
		t.Errorf("a save that changed nothing filed %d revision(s)", len(revs))
	}
}

// A lock is a one-bit toggle with its own setter. Filing it would spend a slot
// on something already trivially reversible and evict an actual edit.
func TestLockingAnAgentFilesNothing(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	created, err := saveAgent(db, AgentRecord{Name: "Scout", OrchestratorPrompt: "answer briefly"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := setAgentLocked(db, created, true); err != nil {
		t.Fatalf("lock: %v", err)
	}
	if revs := revisions.List(db, revisions.KindAgent, created.ID); len(revs) != 0 {
		t.Errorf("locking filed %d revision(s)", len(revs))
	}
}

func TestDeletingAnAgentDropsItsHistory(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	created, err := saveAgent(db, AgentRecord{Name: "Scout", OrchestratorPrompt: "v1", Owner: "u"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	edited := created
	edited.OrchestratorPrompt = "v2"
	if _, err := saveAgent(db, edited); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if revs := revisions.List(db, revisions.KindAgent, created.ID); len(revs) != 1 {
		t.Fatalf("setup: got %d revisions", len(revs))
	}
	if err := deleteAgent(db, created.ID, "u"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if revs := revisions.List(db, revisions.KindAgent, created.ID); len(revs) != 0 {
		t.Errorf("history outlived the agent: %+v", revs)
	}
}

// The point of keeping versions: walk an agent back to what it used to say.
func TestRollingBackAnAgentRestoresTheKeptVersion(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	created, err := saveAgent(db, AgentRecord{Name: "Scout", OrchestratorPrompt: "v1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, p := range []string{"v2", "v3", "v4"} {
		next := created
		next.OrchestratorPrompt = p
		if _, err := saveAgentAs(db, next, "edited instructions"); err != nil {
			t.Fatalf("edit %s: %v", p, err)
		}
		created = next
	}

	revs := revisions.List(db, revisions.KindAgent, created.ID)
	if len(revs) != 3 {
		t.Fatalf("got %d revisions, want 3", len(revs))
	}
	// Find the entry holding v1 and go back to it by id, the way an owner
	// reading a listing would.
	var target int
	for _, r := range revs {
		var body AgentRecord
		if revisions.Load(db, revisions.KindAgent, created.ID, strconv.Itoa(r.Seq), &body) && body.OrchestratorPrompt == "v1" {
			target = r.Seq
		}
	}
	if target == 0 {
		t.Fatal("no kept version holds v1")
	}

	back, err := rollbackAgent(db, created.ID, strconv.Itoa(target))
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if back.OrchestratorPrompt != "v1" {
		t.Errorf("restored prompt = %q, want v1", back.OrchestratorPrompt)
	}
	stored, ok := loadAgent(db, created.ID)
	if !ok || stored.OrchestratorPrompt != "v1" {
		t.Errorf("stored prompt = %q, want v1", stored.OrchestratorPrompt)
	}

	// The rollback files what it replaced, so going back is itself reversible.
	after := revisions.List(db, revisions.KindAgent, created.ID)
	if len(after) != 4 {
		t.Fatalf("got %d revisions after rollback, want 4", len(after))
	}
	if after[0].Reason != "rolled back to #"+strconv.Itoa(target) {
		t.Errorf("newest reason = %q", after[0].Reason)
	}
	var replaced AgentRecord
	if !revisions.Load(db, revisions.KindAgent, created.ID, "", &replaced) || replaced.OrchestratorPrompt != "v4" {
		t.Errorf("the rollback did not file the version it replaced: %q", replaced.OrchestratorPrompt)
	}
}

func TestRollingBackWithoutHistoryRefuses(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	created, err := saveAgent(db, AgentRecord{Name: "Scout", OrchestratorPrompt: "v1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := rollbackAgent(db, created.ID, ""); err == nil {
		t.Error("rolling back an agent with no kept versions must say so")
	}
	if _, err := rollbackAgent(db, created.ID, "99"); err == nil {
		t.Error("an id that was never issued must not resolve")
	}
}
