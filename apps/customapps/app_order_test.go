package customapps

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The order a person drags My Apps into is the order the list comes back in,
// and it is the dashboard's own card order: one order, not two kept in step.
// A slug that is not on their list is not stored.
func TestMyAppsKeepTheOrderTheyWereDraggedInto(t *testing.T) {
	T := sharingTestApp(t)
	prev := AuthDB
	auth := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { AuthDB = prev })
	for _, s := range []string{"alpha", "beta", "gamma"} {
		SaveAppSpec(AppSpec{Slug: s, Name: strings.ToUpper(s[:1]) + s[1:], Owner: "alice"})
	}
	list := func() string {
		w := httptest.NewRecorder()
		T.handleAppsList(w, httptest.NewRequest(http.MethodGet, "/apps/_apps", nil), "alice")
		var rows []map[string]string
		json.Unmarshal(w.Body.Bytes(), &rows)
		var slugs []string
		for _, r := range rows {
			slugs = append(slugs, r["slug"])
		}
		return strings.Join(slugs, " ")
	}
	if got := list(); got != "alpha beta gamma" {
		t.Fatalf("before any drag: %s", got)
	}

	w := httptest.NewRecorder()
	T.handleAppOrder(w, httptest.NewRequest(http.MethodPost, "/apps/_app/order",
		strings.NewReader(`{"order":["gamma","nobodys","alpha","beta"]}`)), "alice")
	if w.Code != http.StatusNoContent {
		t.Fatalf("saving the order: %d %s", w.Code, w.Body.String())
	}
	if got := list(); got != "gamma alpha beta" {
		t.Errorf("the list did not come back in the dragged order: %s", got)
	}
	if got := strings.Join(DashboardOrderOf("alice", []string{"/apps/alpha", "/apps/beta", "/apps/gamma"}), " "); got != "/apps/gamma /apps/alpha /apps/beta" {
		t.Errorf("the dashboard does not have the same order: %s", got)
	}
	// "nobodys" was sent before alpha. Had it been stored it would rank
	// ahead of alpha; unstored, it falls in after every placed app.
	if got := strings.Join(DashboardOrderOf("alice", []string{"/apps/nobodys", "/apps/alpha"}), " "); got != "/apps/alpha /apps/nobodys" {
		t.Errorf("a slug not on the list was stored: %s", got)
	}

	w = httptest.NewRecorder()
	T.handleAppOrder(w, httptest.NewRequest(http.MethodGet, "/apps/_app/order", nil), "alice")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("a GET changed the order: %d", w.Code)
	}
}
