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
// what matters is that nothing REGISTERS an anonymous path, since a public path
// is a deployment-wide exemption from the cookie middleware and not merely one
// app's handler.
func TestNoAnonymousSurfaceIsRegistered(t *testing.T) {
	raw, err := os.ReadFile("customapps.go")
	if err != nil {
		t.Fatalf("reading the source: %v", err)
	}
	src := string(raw)
	for _, gone := range []string{
		`RegisterPublicPath(`,
		`promotion.RegisterApprover("public_link"`,
		`func (T *CustomApps) handlePublic(`,
		`func (T *CustomApps) setPublic(`,
		`func newPublicToken(`,
	} {
		if strings.Contains(src, gone) {
			t.Errorf("the anonymous surface is back: %s", gone)
		}
	}
	// And the one that matters most on its own: an app route must not dispatch
	// anything before RequireUser.
	if strings.Contains(src, `strings.HasPrefix(path, "pub/")`) {
		t.Error("a request is being routed ahead of the auth check again")
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
