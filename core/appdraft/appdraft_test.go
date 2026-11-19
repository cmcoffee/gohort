package appdraft

import (
	"testing"

	"github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func draftWorld(t *testing.T) {
	t.Helper()
	prev := core.RootDB
	core.RootDB = &core.DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { core.RootDB = prev })
}

// A draft is one per app per editor, each save a revision of its own (two in
// one second are two), a verdict held only while the change it judged stands,
// and every draft of an app gone with the app.
func TestADraftIsItsEditorsAndGoesWithTheApp(t *testing.T) {
	draftWorld(t)
	d := Save(Draft{Spec: core.AppSpec{Owner: "o", Slug: "shelf", Name: "Shelf"}, Editor: "o"}, false)
	if d.Started == "" || d.Spec.Created == "" || d.Spec.Updated == "" {
		t.Fatalf("a first save is not stamped: %+v", d)
	}
	again := Save(d, false)
	if again.Spec.Updated == d.Spec.Updated {
		t.Fatal("two saves read as one revision")
	}
	if !RecordVerify("o", "shelf", "o", true, "ok") {
		t.Fatal("no verdict recorded")
	}
	got, _ := Load("o", "shelf", "o")
	if got.Spec.Verify == nil || !got.Spec.Verify.Current(got.Spec) {
		t.Fatalf("the verdict is not on the draft's revision: %+v", got.Spec.Verify)
	}
	if kept := Save(got, true); kept.Spec.Verify == nil || !kept.Spec.Verify.Current(kept.Spec) {
		t.Error("a save that keeps the verdict lost it")
	}
	got, _ = Load("o", "shelf", "o")
	if dropped := Save(got, false); dropped.Spec.Verify != nil {
		t.Error("a change kept a verdict on what it changed")
	}

	Save(Draft{Spec: core.AppSpec{Owner: "o", Slug: "shelf"}, Editor: "m"}, false)
	Save(Draft{Spec: core.AppSpec{Owner: "o", Slug: "shelf-two"}, Editor: "o"}, false)
	if n := len(List("o", "shelf")); n != 2 {
		t.Errorf("drafts of shelf: %d, want the owner's and the maintainer's", n)
	}
	core.DeleteAppSpec("o", "shelf")
	if n := len(List("o", "shelf")); n != 0 {
		t.Errorf("the deleted app's drafts are still there: %d", n)
	}
	if _, ok := Load("o", "shelf-two", "o"); !ok {
		t.Error("deleting one app took another's draft")
	}
}
