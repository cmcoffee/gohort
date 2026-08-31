package customapps

import (
	"strings"
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

// The tier dials are DERIVED from the stored definition when the pane renders,
// never registered when it was saved. That is what makes them survive a restart
// — and what makes a renamed stage lose its dial rather than keep one that
// applies to a stage no longer there.
func TestTierDialsOnlyExistForAnAppWithAPipeline(t *testing.T) {
	RegisterCustomAppTierControl("/custom/_admin")
	// No binding: no dial. A cost control on an app with no stages is a dial
	// for nothing, which is exactly what Render returning nil is for.
	for _, c := range appadmin.For(appadmin.App{Slug: "a", Owner: "u"}) {
		if _, isForm := c.(ui.FormPanel); isForm {
			t.Error("an app with no pipeline rendered a tier form")
		}
	}
	// With a binding but no resolvable definition (no orchestrate in a unit
	// test), it must still decline rather than render an empty form.
	for _, c := range appadmin.For(appadmin.App{Slug: "a", Owner: "u", PipelineID: "nope"}) {
		if f, isForm := c.(ui.FormPanel); isForm && len(f.Fields) == 0 {
			t.Error("an unresolvable pipeline rendered a form with no fields")
		}
	}
}

// An app with nothing sandboxed has nothing to review, and the control has to
// be absent rather than an empty panel promising a look at nothing.
func TestReviewControlOnlyForAppsThatRunCode(t *testing.T) {
	RegisterCustomAppReviewControl("/custom/_admin")
	for _, c := range appadmin.For(appadmin.App{Slug: "plain", Owner: "u"}) {
		if d, isDisplay := c.(ui.DisplayPanel); isDisplay && strings.Contains(d.Source, "/review") {
			t.Error("an app with no scripts rendered the review panel")
		}
	}
}

// "none declared" is a statement about the DECLARATION, not about the reach:
// fetch is granted by default and the owner's credentials are auto-granted to
// app scripts, so an operator reading an empty list must not read it as inert.
func TestEmptyCapabilityListSaysWhatItActuallyMeans(t *testing.T) {
	if got := capsOrNone(nil); got != "none declared" {
		t.Errorf("capsOrNone(nil) = %q", got)
	}
	if got := capsOrNone([]string{"fetch", "log"}); got != "fetch, log" {
		t.Errorf("capsOrNone = %q", got)
	}
}
