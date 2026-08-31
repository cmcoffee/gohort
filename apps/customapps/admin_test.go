package customapps

import (
	"testing"

	"github.com/cmcoffee/gohort/core/appadmin"
	"github.com/cmcoffee/gohort/core/ui"
)

// A control that has nothing to say must be ABSENT, not empty. A present
// control that moves nothing is the failure this surface exists to avoid.
func TestControlsThatDoNotApplyAreAbsent(t *testing.T) {
	appadmin.Register(appadmin.Control{
		Key: "test.only_public", Group: "Exposure",
		Render: func(spec appadmin.App) ui.Component {
			if spec.PublicToken == "" {
				return nil
			}
			return ui.Toolbar{}
		},
	})
	appadmin.Register(appadmin.Control{
		Key: "test.always", Group: "Access",
		Render: func(spec appadmin.App) ui.Component { return ui.Toolbar{} },
	})

	private := appadmin.For(appadmin.App{Slug: "a"})
	public := appadmin.For(appadmin.App{Slug: "a", PublicToken: "tok"})
	if len(public) != len(private)+1 {
		t.Errorf("a published app should gain exactly the public control: %d vs %d", len(public), len(private))
	}
}

// Last registration wins, the way a tunable does, so a key is defined once.
func TestARepeatedControlKeyReplaces(t *testing.T) {
	before := len(appadmin.For(appadmin.App{Slug: "x"}))
	c := appadmin.Control{Key: "test.dup", Group: "Access",
		Render: func(spec appadmin.App) ui.Component { return ui.Toolbar{} }}
	appadmin.Register(c)
	mid := len(appadmin.For(appadmin.App{Slug: "x"}))
	appadmin.Register(c)
	after := len(appadmin.For(appadmin.App{Slug: "x"}))
	if mid != before+1 || after != mid {
		t.Errorf("counts %d → %d → %d; a repeated key must replace, not append", before, mid, after)
	}
}

// The allowlist is additive: unset means what sharing has always meant, so
// this could be added to a running deployment without locking anybody out.
func TestAnUnsetAllowlistIsEverySignedInUser(t *testing.T) {
	if !appadmin.UserMayReach(nil, "owner", "slug", "anybody") {
		t.Error("an app with no allowlist must stay open to every signed-in user")
	}
	if appadmin.UserMayReach(nil, "owner", "slug", "") {
		t.Error("an anonymous request must never pass the reach check")
	}
}
