package core

import "testing"

// Attribution is DECLARED, never inferred from the key. The prefixes look like
// a convention and are two conventions plus exceptions — app.techwriter beside
// blogger.editor beside admin.tool_groups.suggest — and a wrong guess does not
// read as a guess to whoever finds it: it reads as a dial that is theirs.
func TestOnlyDeclaredControlsBelongToAnApp(t *testing.T) {
	RegisterRouteStage(RouteStage{Key: "app.claimtest", Label: "Claimed", App: "/claimtest"})
	RegisterRouteStage(RouteStage{Key: "claimtest.unclaimed", Label: "Unclaimed"})

	claimed := RouteStagesForApp("/claimtest")
	if len(claimed) != 1 || claimed[0].Key != "app.claimtest" {
		t.Fatalf("claimed stages = %+v, want just the declared one", claimed)
	}
	// The key says "claimtest" as loudly as the other one does. It still does
	// not belong to the app, because nobody said so.
	for _, st := range claimed {
		if st.Key == "claimtest.unclaimed" {
			t.Error("a stage was attributed by its key prefix rather than its claim")
		}
	}
	// And it is still in the full registry, i.e. still on its mechanism tab.
	var found bool
	for _, st := range ListRouteStages() {
		if st.Key == "claimtest.unclaimed" {
			found = true
		}
	}
	if !found {
		t.Error("an unclaimed stage must stay on its own tab, not disappear")
	}
}

func TestNoAppMeansNoClaims(t *testing.T) {
	if got := RouteStagesForApp(""); got != nil {
		t.Errorf("an empty app path claimed %d stages — empty means \"belongs to nobody\"", len(got))
	}
	if got := TunablesForApp(""); got != nil {
		t.Errorf("an empty app path claimed %d tunables", len(got))
	}
	if got := RouteStagesForApp("/never-registered"); got != nil {
		t.Errorf("an unknown app claimed %d stages", len(got))
	}
}

func TestTunablesAreClaimedTheSameWay(t *testing.T) {
	RegisterTunable(TunableSpec{Key: "tune_claimtest_a", Label: "A", App: "/claimtest", Default: 1})
	RegisterTunable(TunableSpec{Key: "tune_claimtest_b", Label: "B", Default: 1})
	got := TunablesForApp("/claimtest")
	if len(got) != 1 || got[0].Key != "tune_claimtest_a" {
		t.Errorf("claimed tunables = %+v, want just the declared one", got)
	}
}

// A claim naming no registered app is a control that VANISHES from the app view
// while still working on its own tab: the quietest possible failure, because
// the dial is fine and only its home is wrong. It has to be said out loud.
func TestAnUnknownClaimIsReportedRatherThanDropped(t *testing.T) {
	RegisterRouteStage(RouteStage{Key: "app.ghosttest", Label: "Ghost", App: "/no-such-app"})
	// The reporter walks the registry and warns; what this pins is that the
	// claim is still THERE to be warned about, rather than silently discarded
	// at registration time where nobody would ever see it.
	var found bool
	for _, st := range ListRouteStages() {
		if st.Key == "app.ghosttest" && st.App == "/no-such-app" {
			found = true
		}
	}
	if !found {
		t.Error("an unknown claim was dropped at registration; it must survive to be reported")
	}
	reportUnknownAppClaims() // must not panic with nothing registered to match
}
