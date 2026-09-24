package customapps

import (
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Slugs are per-owner but the shared index is one global map, so deleting or
// unsharing your own app must leave another user's same-slug shared app in
// the index. It used to unset the slug whoever held it.
func TestDeleteOwnAppKeepsOthersShare(t *testing.T) {
	T := sharingTestApp(t)
	SaveAppSpec(AppSpec{Slug: "tool", Name: "Bob Tool", Owner: "bob", Shared: true})
	SetSharedOwner(T.DB, sharedAppsIndex, "tool", "bob", true)
	SaveAppSpec(AppSpec{Slug: "tool", Name: "Alice Tool", Owner: "alice"})

	w := httptest.NewRecorder()
	T.handleDeleteApp(w, httptest.NewRequest(http.MethodDelete, "/apps/_app?slug=tool", nil), "alice")
	if w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if owner, ok := LookupSharedOwner(T.DB, sharedAppsIndex, "tool"); !ok || owner != "bob" {
		t.Fatalf("alice's delete unshared bob's app: owner=%q shared=%v", owner, ok)
	}

	// Unsharing an app you hold at a slug someone else shares is the same case.
	SaveAppSpec(AppSpec{Slug: "tool", Name: "Alice Tool", Owner: "alice"})
	if err := T.setShared("alice", "tool", false); err != nil {
		t.Fatal(err)
	}
	if owner, ok := LookupSharedOwner(T.DB, sharedAppsIndex, "tool"); !ok || owner != "bob" {
		t.Fatalf("alice's unshare unshared bob's app: owner=%q shared=%v", owner, ok)
	}

	// The owner's own delete still clears the entry.
	w = httptest.NewRecorder()
	T.handleDeleteApp(w, httptest.NewRequest(http.MethodDelete, "/apps/_app?slug=tool", nil), "bob")
	if _, ok := LookupSharedOwner(T.DB, sharedAppsIndex, "tool"); ok {
		t.Fatal("owner delete left the shared index entry")
	}
}
