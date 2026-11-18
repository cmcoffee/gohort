package customapps

import (
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/ui"
)

// The way to change an app is under its ⚙, for its owner and its
// maintainers: Builder, its starting form already answered (Change, App,
// this app), so the form does not open asking what to do with the app
// hidden behind that question. Anyone else sees no such way, and the ⚙
// shows only when there is something of theirs behind it.
func TestTheGearHoldsTheWayToChangeAnApp(t *testing.T) {
	prev := agenthost.Get()
	t.Cleanup(func() { agenthost.Register(prev) })
	agenthost.Register(&orchestrate.OrchestrateApp{})
	spec := AppSpec{Slug: "swing", Name: "Swing", Owner: "alice", Maintainers: []string{"mo"}}

	for _, user := range []string{"alice", "mo"} {
		sec := changeSection(spec, user, "alice")
		if sec == nil {
			t.Fatalf("%s has no way to change the app", user)
		}
		bar, _ := sec.Body.(ui.Toolbar)
		if len(bar.Actions) != 1 {
			t.Fatalf("change section: %+v", sec.Body)
		}
		l := bar.Actions[0]
		if l.Label != "Change with Builder" || l.Method != "GET" || !strings.Contains(l.URL, "agent=seed-builder") ||
			!strings.Contains(l.URL, "intake.action=Change") || !strings.Contains(l.URL, "intake.kind=App") ||
			!strings.HasSuffix(l.URL, "intake.target=swing") {
			t.Errorf("link = %+v", l)
		}
		if !hasSettingsPage(spec, user, "alice") {
			t.Errorf("%s has no ⚙", user)
		}
	}
	if changeSection(spec, "bob", "alice") != nil {
		t.Error("someone the app is shared with was offered to change it")
	}
	if hasSettingsPage(spec, "bob", "alice") {
		t.Error("an app with nothing of bob's behind the ⚙ showed him one")
	}
	spec.Memory = "on"
	if !hasSettingsPage(spec, "bob", "alice") {
		t.Error("an app that remembers bob shows him no ⚙")
	}
	if u := builderChangeURL("{slug}"); !strings.HasSuffix(u, "intake.target={slug}") {
		t.Errorf("the row action's URL must keep its placeholder: %s", u)
	}
}
