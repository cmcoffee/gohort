package customapps

// The anonymous capability link is gone, and this is what keeps it gone.
//
// It served an app to whoever held a URL, with no account, running the owner's
// data sources under the owner's credentials: the one path on which a run could
// not be attributed to a person, which is why it needed an administrator's
// approval to exist at all. Sharing to signed-in users is untouched, and gives
// each opener their own copy.

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The surface itself, checked in the source rather than by probing a route:
// what matters is that nothing REGISTERS an anonymous path that runs anything,
// since a public path is a deployment-wide exemption from the cookie
// middleware and not merely one app's handler.
//
// One public path is allowed, by name: the folder an app's sandboxed frame
// loads its own scripts and stylesheets from (asset_files.go). It serves
// those files and nothing else: no data source, action, record or setting,
// and nothing runs. Any other registration, or anything else answered ahead
// of RequireUser, fails here.
func TestNoAnonymousSurfaceIsRegistered(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	const allowedPublic = `RegisterPublicPath(T.WebPath() + appFilesPath)`
	const allowedEarly = "if strings.HasPrefix(path, strings.Trim(appFilesPath, \"/\")+\"/\") {\n\t\tT.handleAppFiles(w, r)\n\t\treturn\n\t}"
	public := 0
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(n)
		if err != nil {
			t.Fatalf("reading %s: %v", n, err)
		}
		src := string(raw)
		public += strings.Count(src, `RegisterPublicPath(`)
		if strings.Count(src, `RegisterPublicPath(`) != strings.Count(src, allowedPublic) {
			t.Errorf("%s registers a public path other than the app files folder", n)
		}
		for _, gone := range []string{
			`promotion.RegisterApprover("public_link"`,
			`func (T *CustomApps) handlePublic(`,
			`func (T *CustomApps) setPublic(`,
			`func newPublicToken(`,
		} {
			if strings.Contains(src, gone) {
				t.Errorf("the anonymous surface is back in %s: %s", n, gone)
			}
		}
	}
	if public > 1 {
		t.Errorf("%d public paths registered; only the app files folder may be", public)
	}
	// And the one that matters most on its own: an app route must not dispatch
	// anything before RequireUser but the app files folder.
	raw, _ := os.ReadFile("customapps.go")
	src := string(raw)
	if strings.Contains(src, `strings.HasPrefix(path, "pub/")`) {
		t.Error("a request is being routed ahead of the auth check again")
	}
	i := strings.Index(src, "func (T *CustomApps) route(")
	j := strings.Index(src[i:], "RequireUser(")
	if i < 0 || j < 0 {
		t.Fatal("route or its RequireUser not found")
	}
	ahead := src[i : i+j]
	ahead = strings.Replace(ahead, allowedEarly, "", 1)
	if strings.Contains(ahead, "T.handle") || strings.Contains(ahead, "ServeHTTP") {
		t.Errorf("something other than the app files folder is answered ahead of RequireUser:\n%s", ahead)
	}
}

// Removing the code stops links being served; the migration is what stops them
// resolving, and tells the owner which app they had.
func TestRetiringALinkWithdrawsItAndSaysSo(t *testing.T) {
	T := sharingTestApp(t)
	root := &DBase{Store: kvlite.MemStore()}
	savedRoot := RootDB
	RootDB = root
	t.Cleanup(func() { RootDB = savedRoot })

	spec := AppSpec{Slug: "tally", Name: "Tally", Owner: "alice", PublicToken: "tok-1"}
	SaveAppSpec(spec)
	// A STRUCT, matching what the retired index actually held: kvlite encodes
	// with gob, which matches by field name across types but refuses a map
	// where a struct was written. Seeding the wrong shape would test the
	// decoder rather than the migration.
	T.DB.Set(publicAppsIndexRetired, "tok-1", struct {
		Owner string `json:"owner"`
		Slug  string `json:"slug"`
	}{Owner: "alice", Slug: "tally"})

	T.retirePublicLinks()

	if len(T.DB.Keys(publicAppsIndexRetired)) != 0 {
		t.Error("a token still resolves, so an old link would still find an app")
	}
	// The spec's own copy goes too: a spec carrying a token reads as published
	// on every surface that checks the field, and it is published nowhere.
	if s, ok := loadSpec("alice", "tally"); !ok || s.PublicToken != "" {
		t.Errorf("the spec still carries its token: %q", s.PublicToken)
	}
	list := notices.List(root, "alice")
	if len(list) != 1 {
		t.Fatalf("the owner was not told, or was told twice: %+v", list)
	}
	// By NAME. "a link was withdrawn" is a support ticket; "the link for Tally
	// was withdrawn" is something somebody can act on.
	if !strings.Contains(list[0].Title, "Tally") {
		t.Errorf("the notice does not name the app: %q", list[0].Title)
	}
	if !strings.Contains(list[0].Body, "share it to signed-in users") {
		t.Errorf("the notice does not say what to do instead: %q", list[0].Body)
	}

	// Idempotent: a second startup finds nothing and says nothing more.
	T.retirePublicLinks()
	if got := len(notices.List(root, "alice")); got != 1 {
		t.Errorf("a second run raised another notice: %d", got)
	}
}
