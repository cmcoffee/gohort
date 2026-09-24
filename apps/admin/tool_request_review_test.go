package admin

// A tool request is reviewed against what it would publish: the changes to a
// published tool, the whole of a new one, and a name clash said plainly.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestToolRequestReviewShowsWhatApprovingPublishes(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })

	if err := AdminPersistTempTool(db, "alice", TempTool{Name: "lookup", CommandTemplate: "echo one"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolShared(db, "alice", "lookup", true); err != nil {
		t.Fatal(err)
	}
	if err := AdminPersistTempTool(db, "alice", TempTool{Name: "lookup", CommandTemplate: "echo two"}); err != nil {
		t.Fatal(err)
	}
	if err := CreatePromotionRequest(db, "alice", "tool", "lookup", ""); err != nil {
		t.Fatal(err)
	}
	_ = AdminPersistTempTool(db, "bob", TempTool{Name: "fresh", CommandTemplate: "echo new"})
	_ = CreatePromotionRequest(db, "bob", "tool", "fresh", "")
	_ = AdminPersistTempTool(db, "carol", TempTool{Name: "lookup", CommandTemplate: "echo clash"})
	_ = CreatePromotionRequest(db, "carol", "tool", "lookup", "")

	releases := map[string]ToolRelease{}
	for _, rel := range ToolReleases(db) {
		releases[rel.Name] = rel
	}
	update, review := toolRequestReview(db, releases, "alice", "lookup")
	if !update || !strings.Contains(review, "- echo one") || !strings.Contains(review, "+ echo two") {
		t.Errorf("an update request should show its diff: %v\n%s", update, review)
	}
	update, review = toolRequestReview(db, releases, "bob", "fresh")
	if update || !strings.Contains(review, "+ echo new") {
		t.Errorf("a first publish should show the whole tool: %v\n%s", update, review)
	}
	if _, review = toolRequestReview(db, releases, "carol", "lookup"); !strings.Contains(review, "already publishes") {
		t.Errorf("a clash with a published name should be said: %s", review)
	}
}
