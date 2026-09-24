package core

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/shareledger"
	"github.com/cmcoffee/snugforge/kvlite"
)

func toolCollisionStore(t *testing.T) Database {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })
	return db
}

// toolBundle is a one-tool bundle as an export would write it.
func toolBundle(t *testing.T, tt TempTool) []byte {
	t.Helper()
	recipe, err := json.Marshal(tt)
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(ArtifactBundle{Bundle: ArtifactBundleFormat,
		Artifacts: []PortableArtifact{{Type: "tool", Name: tt.Name, Recipe: recipe}}})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func pendingNamed(db Database, user, name string) (PendingTempTool, bool) {
	for _, p := range LoadPendingTempTools(db, user) {
		if p.Tool.Name == name {
			return p, true
		}
	}
	return PendingTempTool{}, false
}

// Preview said "skip" for a tool whose name matched one already waiting for
// review, and the import then replaced that pending tool: different code under
// the name an administrator may have been halfway through reading. Import
// skips it too now, so the preview is the truth.
func TestToolImportSkipsAPendingToolAsThePreviewSays(t *testing.T) {
	db := toolCollisionStore(t)
	if err := QueuePendingTempTool(db, "alice", TempTool{Name: "wiki_read", CommandTemplate: "echo first"}, "s1"); err != nil {
		t.Fatal(err)
	}
	bundle := toolBundle(t, TempTool{Name: "wiki_read", CommandTemplate: "echo second"})

	prev, err := PreviewArtifactBundleAsUser(db, bundle, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if prev.WouldSkip != 1 {
		t.Fatalf("precondition: preview skips it: %+v", prev.Items)
	}
	res, err := ImportArtifactBundleAsUser(db, bundle, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 0 || res.Skipped != 1 {
		t.Errorf("the import disagreed with its preview: %+v", res.Outcomes)
	}
	if p, _ := pendingNamed(db, "alice", "wiki_read"); p.Tool.CommandTemplate != "echo first" {
		t.Errorf("the pending tool was replaced: %q", p.Tool.CommandTemplate)
	}
}

// An import meets the rules every authoring path applies to a new name: a
// bundle could queue a tool named after a built-in, and approving it put a
// stub in front of the real one. Preview predicts the same skip.
func TestToolImportRefusesNamesNoAuthoringPathWouldAccept(t *testing.T) {
	db := toolCollisionStore(t)
	RegisterReservedToolName("zz_reserved_for_import_test")
	for _, name := range []string{"zz_reserved_for_import_test", "Not-Snake"} {
		bundle := toolBundle(t, TempTool{Name: name, CommandTemplate: "echo hi"})
		prev, err := PreviewArtifactBundleAsUser(db, bundle, "alice")
		if err != nil {
			t.Fatal(err)
		}
		if prev.WouldImport != 0 {
			t.Errorf("%q: preview predicts an import", name)
		}
		res, err := ImportArtifactBundleAsUser(db, bundle, "alice")
		if err != nil {
			t.Fatal(err)
		}
		if res.Imported != 0 {
			t.Errorf("%q: imported", name)
		}
		if _, queued := pendingNamed(db, "alice", name); queued {
			t.Errorf("%q: queued for approval", name)
		}
	}
}

// Two imports queueing one name are two authors. The second used to replace
// the first silently, scope and all, so the agent the first was scoped to
// lost its tool on approval. The same definition now keeps its place and gains
// the scope; a different one is refused and the pending tool left alone.
func TestQueueingANameAlreadyPendingNeverReplacesIt(t *testing.T) {
	db := toolCollisionStore(t)
	tool := TempTool{Name: "wiki_read", CommandTemplate: "echo one"}
	if err := QueuePendingTempToolScoped(db, "alice", tool, "import", []string{"agent-1"}); err != nil {
		t.Fatal(err)
	}
	if err := QueuePendingTempToolScoped(db, "alice", tool, "import", []string{"agent-2"}); err != nil {
		t.Fatalf("the same definition again should merge: %v", err)
	}
	p, _ := pendingNamed(db, "alice", "wiki_read")
	if !sliceHas(p.ScopeAgents, "agent-1") || !sliceHas(p.ScopeAgents, "agent-2") {
		t.Errorf("the scopes were not merged: %v", p.ScopeAgents)
	}
	if n := len(LoadPendingTempTools(db, "alice")); n != 1 {
		t.Errorf("one name, %d pending entries", n)
	}

	other := TempTool{Name: "wiki_read", CommandTemplate: "echo two"}
	if err := QueuePendingTempToolScoped(db, "alice", other, "import", []string{"agent-3"}); err == nil {
		t.Error("a different definition replaced the pending tool")
	}
	p, _ = pendingNamed(db, "alice", "wiki_read")
	if p.Tool.CommandTemplate != "echo one" || sliceHas(p.ScopeAgents, "agent-3") {
		t.Errorf("the refused queue changed the pending tool: %+v", p)
	}
}

// The share ledger said "Taken: your agents load it" whenever the name was on
// the adoption list: also when the adoption was pinned to somebody else's tool
// of that name, and when the user's own tool held the name.
func TestTheLedgerSaysWhichToolTheAgentsReallyLoad(t *testing.T) {
	db := toolCollisionStore(t)
	for _, owner := range []string{"lender_a", "lender_b"} {
		if err := AdminPersistTempTool(db, owner, TempTool{Name: "wiki_read", CommandTemplate: "echo " + owner}); err != nil {
			t.Fatal(err)
		}
		if err := SetPersistentTempToolSharedWith(db, owner, "wiki_read", []string{"taker"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetGlobalToolAdopted(db, "taker", "wiki_read", "lender_b", true); err != nil {
		t.Fatal(err)
	}
	detail := func() map[string]string {
		out := map[string]string{}
		for _, g := range shareledger.ToMe("taker") {
			if g.Kind == "tool" {
				out[g.Owner] = g.Detail
			}
		}
		return out
	}
	got := detail()
	if got["lender_b"] != "Taken: your agents load it." {
		t.Errorf("the taken tool: %q", got["lender_b"])
	}
	if strings.HasPrefix(got["lender_a"], "Taken") || !strings.Contains(got["lender_a"], "lender_b") {
		t.Errorf("an offer of the same name from somebody else read as taken: %q", got["lender_a"])
	}
	if m := shareledger.Manifest("tool", "lender_a", "wiki_read", "taker"); len(m) == 0 {
		t.Error("lender_a's tool does not load for taker, and the manifest said nothing")
	}

	// The user's own tool of the name wins over both.
	if err := AdminPersistTempTool(db, "taker", TempTool{Name: "wiki_read", CommandTemplate: "echo own"}); err != nil {
		t.Fatal(err)
	}
	if d := detail()["lender_b"]; strings.HasPrefix(d, "Taken") {
		t.Errorf("a taken tool the user's own copy shadows read as loaded: %q", d)
	}
	if m := shareledger.Manifest("tool", "lender_b", "wiki_read", "taker"); len(m) == 0 {
		t.Error("the manifest said nothing about the user's own copy running instead")
	}
}
