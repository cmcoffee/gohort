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
		r := req(user)
		r.URL.Path = "/_apps"
		w := httptest.NewRecorder()
		routed(T)(w, r)
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

// A shared app's card says who shared it, as everyone reads them: their name
// beside their username, or the username alone. The viewer's own apps say
// nothing of the kind.
func TestASharedAppsCardSaysWhoSharedIt(t *testing.T) {
	T := sharingTestApp(t)
	auth := &DBase{Store: kvlite.MemStore()}
	saved := AuthDB
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { AuthDB = saved })
	for _, u := range []string{"bob", "carol"} {
		AuthSetUser(auth, u, "pw", false)
	}
	AuthSetUserApps(auth, "carol", []string{"/apps"})
	SaveAppSpec(AppSpec{Slug: "alpha", Name: "Alpha", Desc: "Tracks things.", Owner: "bob", Shared: true})
	SetSharedOwner(T.DB, sharedAppsIndex, "alpha", "bob", true)
	SaveAppSpec(AppSpec{Slug: "mine", Name: "Mine", Desc: "Carol's own.", Owner: "carol"})
	descs := func() map[string]string {
		r := httptest.NewRequest(http.MethodGet, "/apps/", nil)
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(auth, "carol")})
		out := map[string]string{}
		for _, c := range T.DashboardPinnable(r) {
			out[c.Path] = c.Desc
		}
		return out
	}
	if got := descs()["/apps/alpha"]; got != "Shared by bob. Tracks things." {
		t.Errorf("without a name: %q", got)
	}
	u, _ := AuthGetUser(auth, "bob")
	u.DisplayName = "Robert"
	auth.Set(AuthTable, "user:bob", u)
	got := descs()
	if got["/apps/alpha"] != "Shared by Robert (bob). Tracks things." {
		t.Errorf("with a name: %q", got["/apps/alpha"])
	}
	if got["/apps/mine"] != "Carol's own." {
		t.Errorf("the viewer's own app: %q", got["/apps/mine"])
	}
}
