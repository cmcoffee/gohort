package guides

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// TestReadOnlyGuideTools proves a reader of a shared guide gets exactly the
// non-mutating knowledge tools — so the Guide Author can answer questions from
// the guide's linked knowledge, but can't edit the document or reach the
// web/reference-pull tools.
func TestReadOnlyGuideTools(t *testing.T) {
	full := []AgentToolDef{
		{Tool: Tool{Name: "add_section"}},
		{Tool: Tool{Name: "edit_section"}},
		{Tool: Tool{Name: "delete_section"}},
		{Tool: Tool{Name: "move_section"}},
		{Tool: Tool{Name: "rename_section"}},
		{Tool: Tool{Name: "list_sections"}},
		{Tool: Tool{Name: "research"}},
		{Tool: Tool{Name: "search_knowledge"}},
		{Tool: Tool{Name: "list_reference_sources"}},
		{Tool: Tool{Name: "pull_reference"}},
	}
	got := map[string]bool{}
	for _, tl := range readOnlyGuideTools(full) {
		got[tl.Tool.Name] = true
	}
	// Readers get the guide's knowledge (collections) AND its linked sources
	// (references) for Q&A — all owner-scoped, read-only.
	wantReader := []string{"list_sections", "search_knowledge", "list_reference_sources", "pull_reference"}
	if len(got) != len(wantReader) {
		t.Fatalf("reader tools = %v, want exactly %v", got, wantReader)
	}
	for _, want := range wantReader {
		if !got[want] {
			t.Errorf("reader should get the %q tool", want)
		}
	}
	// Every mutating / web-researching tool stays withheld.
	for _, mustExclude := range []string{"add_section", "edit_section", "delete_section", "move_section", "rename_section", "research"} {
		if got[mustExclude] {
			t.Errorf("reader must NOT get the %q tool", mustExclude)
		}
	}
}

// TestReferenceAttached checks the gate that keeps a reader's pull_reference to
// the guide's own linked sources.
func TestReferenceAttached(t *testing.T) {
	g := Guide{References: []ReferenceSelection{{Kind: "system", ItemID: "srv-1"}}}
	if !referenceAttached(g, "system", "srv-1") {
		t.Error("attached source should be recognized")
	}
	if referenceAttached(g, "system", "srv-2") || referenceAttached(g, "mcp:confluence", "srv-1") {
		t.Error("a source the owner did NOT link must be rejected")
	}
}

// TestResolveGuideSharing covers the resolve seam: an owned guide resolves from
// the requester's own store; a guide shared by someone else resolves from the
// owner's store via the index; and CanManageShared gates who may edit it.
func TestResolveGuideSharing(t *testing.T) {
	appDB := OpenCache()
	alice := UserDB(appDB, "alice")
	bob := UserDB(appDB, "bob")

	// Alice owns g1 and shares it; g2 she keeps private.
	saveGuide(alice, Guide{ID: "g1", Title: "Shared", Owner: "alice"})
	saveGuide(alice, Guide{ID: "g2", Title: "Private", Owner: "alice"})
	SetSharedOwner(appDB, sharedGuidesIndex, "g1", "alice", true)

	// Alice resolves her own guide from her own store and can manage it.
	if g, owner, oudb, ok := resolveGuide(appDB, alice, "alice", "g1"); !ok ||
		owner != "alice" || g.Title != "Shared" || oudb == nil {
		t.Fatalf("alice should resolve her own g1: ok=%v owner=%q title=%q", ok, owner, g.Title)
	} else if !CanManageShared("alice", owner, false) {
		t.Error("alice should manage her own guide")
	}

	// Bob resolves the shared guide from alice's store, but cannot manage it.
	g, owner, oudb, ok := resolveGuide(appDB, bob, "bob", "g1")
	if !ok || owner != "alice" || g.Title != "Shared" || oudb == nil {
		t.Fatalf("bob should resolve shared g1 in alice's store: ok=%v owner=%q", ok, owner)
	}
	if CanManageShared("bob", owner, false) {
		t.Error("bob (non-owner, non-admin) must NOT manage a shared guide")
	}
	if !CanManageShared("bob", owner, true) {
		t.Error("an admin should manage any shared guide")
	}

	// Bob cannot resolve alice's private guide at all.
	if _, _, _, ok := resolveGuide(appDB, bob, "bob", "g2"); ok {
		t.Error("bob must not resolve a private guide he doesn't own")
	}

	// Unsharing removes it from the index, so bob can no longer resolve it.
	SetSharedOwner(appDB, sharedGuidesIndex, "g1", "alice", false)
	if _, _, _, ok := resolveGuide(appDB, bob, "bob", "g1"); ok {
		t.Error("bob must not resolve g1 after it is unshared")
	}
}

// canEdit mirrors the composition in (*Guides).resolve: a manager, or anyone when
// the guide is shared for edit.
func canEdit(g Guide, reqUser, owner string, isAdmin bool) bool {
	return CanManageShared(reqUser, owner, isAdmin) || g.sharedForEdit()
}

// TestShareModeEdit covers the view-vs-edit distinction: a view-shared guide is
// read-only to non-owners; an edit-shared guide lets any user edit but still not
// manage sharing/delete.
func TestShareModeEdit(t *testing.T) {
	viewG := Guide{ID: "v", Owner: "alice", Shared: true, ShareMode: "view"}
	editG := Guide{ID: "e", Owner: "alice", Shared: true, ShareMode: ShareModeEdit}
	privG := Guide{ID: "p", Owner: "alice"}

	if viewG.sharedForEdit() || privG.sharedForEdit() {
		t.Error("only an edit-shared guide is sharedForEdit")
	}
	if !editG.sharedForEdit() {
		t.Error("edit-shared guide should be sharedForEdit")
	}

	// A non-owner: can edit the edit-shared guide, cannot edit the view-shared one.
	if canEdit(viewG, "bob", "alice", false) {
		t.Error("bob must NOT edit a view-only shared guide")
	}
	if !canEdit(editG, "bob", "alice", false) {
		t.Error("bob SHOULD edit an edit-shared guide")
	}
	// But edit access never grants manage (sharing/delete) to a non-owner.
	if CanManageShared("bob", "alice", false) {
		t.Error("edit access must not let a non-owner manage sharing/delete")
	}
	// The owner edits + manages either way.
	if !canEdit(viewG, "alice", "alice", false) || !CanManageShared("alice", "alice", false) {
		t.Error("owner should edit and manage their own guide")
	}
}
