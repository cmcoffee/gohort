package customapps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appstores"
	"github.com/cmcoffee/snugforge/kvlite"
)

type reachStores struct{}

func (reachStores) GrantsFor(owner, slug string) []appstores.Grant {
	if owner == "alice" && slug == "pcap" {
		return []appstores.Grant{
			{Store: "caps", Name: "Captures", Read: true, Write: true, Commands: true},
			{Store: "ref", Name: "Reference", Read: true},
		}
	}
	return nil
}
func (reachStores) RunCommand(context.Context, string, string, string, string, string, string) (string, error) {
	return "", nil
}
func (reachStores) RunAction(context.Context, string, string, string, string, string, map[string]any) (string, error) {
	return "", nil
}
func (reachStores) Pull(context.Context, string, string, string, string, bool) (string, error) {
	return "", nil
}

// An administrator's store grant covers whoever uses the app, so the Share
// dialog's record and the administrator's review both say which stores the
// app reaches and what it may do there; an app with none says nothing.
func TestSharingAnAppSaysWhichFileStoresItReaches(t *testing.T) {
	T := sharingTestApp(t)
	prev := appstores.Provider
	appstores.Provider = reachStores{}
	t.Cleanup(func() { appstores.Provider = prev })
	auth := &DBase{Store: kvlite.MemStore()}
	saved := AuthDB
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { AuthDB = saved })
	AuthSetUser(auth, "alice", "pw", false)
	SaveAppSpec(AppSpec{Slug: "pcap", Name: "Pcap", Owner: "alice", RecordKey: "id"})
	SaveAppSpec(AppSpec{Slug: "notes", Name: "Notes", Owner: "alice", RecordKey: "id"})

	r := httptest.NewRequest("GET", "/_apps", nil)
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(auth, "alice")})
	w := httptest.NewRecorder()
	routed(T)(w, r)
	body := w.Body.String()
	if !strings.Contains(body, `Captures: read and change its files, run its commands\nReference: read its files`) {
		t.Errorf("the Share dialog is not told which stores the app reaches: %s", body)
	}
	if strings.Count(body, "store_reach") != 1 {
		t.Errorf("an app with no store grants carries a store line: %s", body)
	}
	if got := adminReview(AppSpec{Slug: "pcap", Owner: "alice"}, "alice")["file_stores"]; got != "Captures: read and change its files, run its commands; Reference: read its files" {
		t.Errorf("the review's file stores: %v", got)
	}
	if got := adminReview(AppSpec{Slug: "notes", Owner: "alice"}, "alice")["file_stores"]; got != "none" {
		t.Errorf("an app with no grants: %v", got)
	}
}
