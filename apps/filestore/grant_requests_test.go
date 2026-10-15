package filestore

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appstores"
	"github.com/cmcoffee/gohort/core/promotion"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An app's request for a store goes through the approvals queue: filed only
// for a store that exists and an app the requester owns, and approving it adds
// exactly the rights it names to whatever the app already holds there.
func TestAnAppsStoreRequestIsApprovedThroughTheQueue(t *testing.T) {
	app, intake, _ := pullFixture(t)
	authDB := &DBase{Store: kvlite.MemStore()}
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return authDB }
	RootDB = authDB
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })
	SaveAppSpec(AppSpec{Owner: "alice", Slug: "prep", Name: "Case prep", RecordKey: "id"})
	registerGrantRequests(app)
	// It already holds pull; it asks for write and commands.
	intake.AppGrants = []AppGrant{{App: "alice/prep", Pull: true}}
	SaveStore(app.DB, intake)

	name := appstores.GrantRequestName(intake.Slug, "prep", true, true, false)
	if err := promotion.CreatePromotionRequest(authDB, "alice", appstores.GrantRequestKind, name, "it downloads and extracts"); err != nil {
		t.Fatal(err)
	}
	if st, _ := LoadStore(app.DB, intake.Slug); len(st.AppGrants) != 1 || st.AppGrants[0].Write {
		t.Fatal("filing a request changed the grant before anyone approved it")
	}
	if err := promotion.Approve(authDB, promotion.RequestKey(appstores.GrantRequestKind, "alice", name), "admin"); err != nil {
		t.Fatal(err)
	}
	st, _ := LoadStore(app.DB, intake.Slug)
	if len(st.AppGrants) != 1 || !st.AppGrants[0].Write || !st.AppGrants[0].Commands || !st.AppGrants[0].Pull {
		t.Errorf("after approval: %+v", st.AppGrants)
	}

	for what, bad := range map[string]string{
		"a store that does not exist": appstores.GrantRequestName("nowhere", "prep", true, false, false),
		"an app that is not hers":     appstores.GrantRequestName(intake.Slug, "someone-elses", true, false, false),
	} {
		if err := promotion.CreatePromotionRequest(authDB, "alice", appstores.GrantRequestKind, bad, ""); err == nil {
			t.Errorf("%s was filed", what)
		}
	}
	// The name reads back to what it asked for.
	if s, slug, w, c, p, ok := appstores.ParseGrantRequestName(name); !ok || s != intake.Slug || slug != "prep" || !w || !c || p {
		t.Errorf("parsed %q: %v %v %v %v %v %v", name, s, slug, w, c, p, ok)
	}
	if !strings.Contains(name, ": read, write, run commands") {
		t.Errorf("the name an admin reads: %q", name)
	}
}
