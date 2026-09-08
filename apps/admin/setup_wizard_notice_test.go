package admin

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestTheDashboardTellsAFreshAdminWhatIsMissing. The first-run wizard has
// always existed and always redirected — from the ADMIN app's root. Login
// redirects to "/", so the first admin met a grid of app cards, opened one,
// and watched it fail with no model, while the guided flow sat at a URL
// nothing had sent them to.
func TestTheDashboardTellsAFreshAdminWhatIsMissing(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	app := &AdminApp{db: db}

	// No provider configured: the notice appears, and it hands over the
	// wizard rather than merely reporting the problem.
	got := app.firstRunNotices(false)
	if len(got) != 1 {
		t.Fatalf("a fresh install told the admin nothing: %+v", got)
	}
	if got[0].URL != app.setupWizardPath() {
		t.Errorf("the notice points at %q, not the wizard", got[0].URL)
	}
	if got[0].Action == "" {
		t.Error("the notice has no button, so it is a complaint rather than a route")
	}

	// Once a provider is saved it stops — the same condition the redirect
	// uses, read from the same helper, so the two cannot disagree.
	db.Set(LLMTable, "provider", "openai")
	if got := app.firstRunNotices(false); len(got) != 0 {
		t.Errorf("a configured install still nags: %+v", got)
	}

	// And a dismissed wizard stays dismissed on the dashboard too — one
	// decision, both surfaces.
	db.Unset(LLMTable, "provider")
	if got := app.firstRunNotices(true); len(got) != 0 {
		t.Errorf("the notice ignores the dismissal the wizard honours: %+v", got)
	}
}
