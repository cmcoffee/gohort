package customapps

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appdraft"
)

// My Apps shows Builder's work in progress: an app with a draft says so,
// with Preview, Make live and Discard; a new app not live yet has its own
// row, which opens nothing live. Each is the person's own draft alone.
func TestMyAppsShowsBuildersWorkInProgress(t *testing.T) {
	T, installed, working := previewTestApp(t)
	appdraft.Save(appdraft.Draft{Spec: working, Editor: "alice"}, false)
	appdraft.Save(appdraft.Draft{Spec: AppSpec{Owner: "alice", Slug: "notes", Name: "Notes"}, Editor: "alice"}, false)
	// Someone else's draft of an app of theirs is not alice's to see.
	appdraft.Save(appdraft.Draft{Spec: AppSpec{Owner: "bob", Slug: "bobs", Name: "Bob's"}, Editor: "bob"}, false)
	// A maintainer's draft of an app of alice's is kept in her store, and is
	// the maintainer's work, not hers.
	appdraft.Save(appdraft.Draft{Spec: AppSpec{Owner: "alice", Slug: "kept", Name: "Kept"}, Editor: "carol"}, false)

	var rows []map[string]string
	if err := json.Unmarshal(as(t, T, "alice", httptest.NewRequest(http.MethodGet, "/apps/_apps", nil)).Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	bySlug := map[string]map[string]string{}
	for _, r := range rows {
		bySlug[r["slug"]] = r
	}
	if r := bySlug[installed.Slug]; r["draft"] != "1" || !strings.Contains(r["note"], "not live yet") || r["not_open"] == "1" {
		t.Errorf("the live app with a draft: %v", r)
	}
	if r := bySlug["notes"]; r["draft"] != "1" || r["state"] != "draft" || r["not_open"] != "1" || r["mine"] == "1" {
		t.Errorf("the new app not live yet: %v", r)
	}
	if _, ok := bySlug["bobs"]; ok {
		t.Error("alice's list shows bob's draft")
	}
	if _, ok := bySlug["kept"]; ok {
		t.Error("alice's list shows a maintainer's draft as hers")
	}

	if w := as(t, T, "alice", httptest.NewRequest(http.MethodGet, "/apps/_app/draft/preview?slug=wx", nil)); w.Code != http.StatusFound || !strings.Contains(w.Header().Get("Location"), "/apps/wx/_preview?token=") {
		t.Errorf("Preview: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := as(t, T, "bob", httptest.NewRequest(http.MethodGet, "/apps/_app/draft/preview?slug=wx", nil)); w.Code != http.StatusNotFound {
		t.Errorf("bob previewed alice's draft: %d", w.Code)
	}

	// Make live is the host's, handed the draft and who pressed it.
	var gotBy, gotSlug string
	prev := appdraft.MakeLive
	appdraft.MakeLive = func(d appdraft.Draft, by string) (string, error) {
		gotBy, gotSlug = by, d.Spec.Slug
		return "live", nil
	}
	t.Cleanup(func() { appdraft.MakeLive = prev })
	if w := as(t, T, "alice", httptest.NewRequest(http.MethodPost, "/apps/_app/draft/live?slug=wx", strings.NewReader("{}"))); w.Code != http.StatusOK || gotBy != "alice" || gotSlug != "wx" {
		t.Errorf("Make live: %d %s (%q %q)", w.Code, w.Body.String(), gotBy, gotSlug)
	}

	// Discard leaves the live app; a new app never live goes with its draft.
	if w := as(t, T, "alice", httptest.NewRequest(http.MethodPost, "/apps/_app/draft/discard?slug=wx", strings.NewReader("{}"))); w.Code != http.StatusOK {
		t.Fatalf("Discard: %d %s", w.Code, w.Body.String())
	}
	if _, ok := appdraft.Load("alice", "wx", "alice"); ok {
		t.Error("the discarded draft is still there")
	}
	if live, ok := loadSpec("alice", "wx"); !ok || live.Name != installed.Name {
		t.Errorf("discarding changed the live app: %v %+v", ok, live)
	}
	if w := as(t, T, "alice", httptest.NewRequest(http.MethodPost, "/apps/_app/draft/discard?slug=notes", strings.NewReader("{}"))); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "never live") {
		t.Errorf("Discard of a new app: %d %s", w.Code, w.Body.String())
	}
	if w := as(t, T, "alice", httptest.NewRequest(http.MethodPost, "/apps/_app/draft/discard?slug=bobs", strings.NewReader("{}"))); w.Code != http.StatusNotFound {
		t.Errorf("alice discarded bob's draft: %d", w.Code)
	}
}
