package customapps

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A grant to one shared app opens that app alone, as sharedAppReachableBy
// says, and the dashboard's My apps list offers what the My Apps page lists.
// The coarse check used to read a grant to /apps/alpha as a grant to /apps,
// which opened every shared app to whoever held one.
func TestAPerAppGrantOpensThatAppAlone(t *testing.T) {
	T := sharingTestApp(t)
	auth := &DBase{Store: kvlite.MemStore()}
	saved := AuthDB
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { AuthDB = saved })
	for _, u := range []string{"alice", "bob", "carol"} {
		AuthSetUser(auth, u, "pw", false)
	}
	AuthSetUserApps(auth, "alice", []string{"/apps/alpha"})
	AuthSetUserApps(auth, "carol", []string{"/apps"})
	for _, slug := range []string{"alpha", "beta"} {
		SaveAppSpec(AppSpec{Slug: slug, Name: strings.ToUpper(slug[:1]) + slug[1:], Owner: "bob", Shared: true})
		SetSharedOwner(T.DB, sharedAppsIndex, slug, "bob", true)
	}
	req := func(user string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/apps/", nil)
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(auth, user)})
		return r
	}
	pinned := func(user string) (out []string) {
		for _, c := range T.DashboardPinnable(req(user)) {
			out = append(out, c.Path)
		}
		return
	}
	listed := func(user string) (out []string) {
		w := httptest.NewRecorder()
		T.handleAppsList(w, req(user), user)
		for _, slug := range []string{"alpha", "beta"} {
			if strings.Contains(w.Body.String(), `"slug":"`+slug+`"`) {
				out = append(out, "/apps/"+slug)
			}
		}
		return
	}
	for _, c := range []struct {
		user string
		want []string
	}{
		{"alice", []string{"/apps/alpha"}},
		{"carol", []string{"/apps/alpha", "/apps/beta"}},
	} {
		if got := pinned(c.user); !slices.Equal(got, c.want) {
			t.Errorf("%s's dashboard offers %v, want %v", c.user, got, c.want)
		}
		if got := listed(c.user); !slices.Equal(got, c.want) {
			t.Errorf("%s's My Apps lists %v, want %v", c.user, got, c.want)
		}
		for _, slug := range []string{"alpha", "beta"} {
			if got, want := T.sharedAppReachableBy(req(c.user), slug), slices.Contains(c.want, "/apps/"+slug); got != want {
				t.Errorf("%s may open %s: %v, want %v", c.user, slug, got, want)
			}
		}
	}
}
