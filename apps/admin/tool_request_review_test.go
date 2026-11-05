package admin

// A tool request is reviewed against what it would publish: the changes to a
// published tool, the whole of a new one, and a name clash said plainly.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/promotion"
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

// A skill request is reviewed the same way: an edit to a published skill as
// the changes everybody would get, a new one whole.
func TestSkillRequestReviewShowsWhatApprovingPublishes(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })

	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess."})
	if err := CreatePromotionRequest(db, "alice", SkillPromotionKind, "Triage", ""); err != nil {
		t.Fatal(err)
	}
	update, review := skillRequestReview("alice", "Triage")
	if update || !strings.Contains(review, "Assess.") {
		t.Errorf("a new skill's review: %v %q", update, review)
	}
	if err := promotion.Approve(db, PromotionRequestKey(SkillPromotionKind, "alice", "Triage"), "root"); err != nil {
		t.Fatal(err)
	}
	pub := PublishedSkillsBy(db, "alice")[0]
	pub.Instructions = "Assess, then page."
	SaveSkill(db, "alice", pub)
	update, review = skillRequestReview("alice", "Triage")
	if !update || !strings.Contains(review, "- Assess.") || !strings.Contains(review, "+ Assess, then page.") {
		t.Errorf("an edit's review: %v %q", update, review)
	}

	// And the queue the administrator reads carries it.
	prevAuth := AuthDB
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prevAuth })
	db.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	mux := http.NewServeMux()
	(&AdminApp{}).RegisterRoutes(mux, "")
	r := httptest.NewRequest("GET", "/api/promotions", nil)
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(db, "root")})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "Assess, then page.") {
		t.Errorf("the queue does not show the skill edit to review: %d %s", w.Code, w.Body.String())
	}
}
