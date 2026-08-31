package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
	RegisterTunable(TunableSpec{Key: "tune_claimtest_a", Category: "Limits", Label: "A",
		App: "/claimtest", Kind: KindInt, Default: 1, Min: 1, Max: 10})
	RegisterTunable(TunableSpec{Key: "tune_claimtest_b", Category: "Limits", Label: "B",
		Kind: KindInt, Default: 1, Min: 1, Max: 10})
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

// A grant is a PATH STRING held in three places. Renaming a mount without
// rewriting them revokes access silently: the app is there, the person has a
// grant, and the two no longer name the same thing.
func TestGrantsFollowAMountRename(t *testing.T) {
	db := memDB(t)
	prev := AuthDB
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })

	AuthSetUser(db, "alice", "pw", false)
	AuthSetUserApps(db, "alice", []string{"/custom", "/custom/weather", "/customer-portal", "/servitor"})
	AuthSetDefaultApps(db, []string{"/custom"})

	if n := MigrateAppPathGrants(db, "/custom", "/apps"); n == 0 {
		t.Fatal("migration reported no changes")
	}
	user, _ := AuthGetUser(db, "alice")
	want := map[string]bool{"/apps": true, "/apps/weather": true, "/customer-portal": true, "/servitor": true}
	for _, p := range user.Apps {
		if !want[p] {
			t.Errorf("unexpected grant after migration: %q", p)
		}
		delete(want, p)
	}
	for p := range want {
		t.Errorf("grant missing after migration: %q", p)
	}
	// Prefix-aware, not substring: "/customer-portal" starts with "/custom"
	// and is a different app.
	for _, p := range user.Apps {
		if strings.HasPrefix(p, "/appser") {
			t.Errorf("a neighbouring path was mangled: %q", p)
		}
	}
	if got := AuthGetDefaultApps(db); len(got) != 1 || got[0] != "/apps" {
		t.Errorf("default apps = %v, want [/apps]", got)
	}

	// Once, keyed by the rename: a second run must not re-fire.
	if n := MigrateAppPathGrants(db, "/custom", "/apps"); n != 0 {
		t.Errorf("migration ran twice, changing %d more record(s)", n)
	}
}

// The legacy mount is registered from inside an app's Routes(), so it only
// exists once every app has registered. Mounting the redirects before that
// loop mounts an empty map, and the old path 404s — which is the single thing
// a legacy mount exists to prevent, failing where nobody looks: on the links
// that were already out in the world.
func TestLegacyMountRedirectsAfterAppsRegister(t *testing.T) {
	RegisterLegacyMount("/oldmount", "/newmount")
	mux := http.NewServeMux()
	mountLegacyRedirects(mux)

	for _, tc := range []struct{ from, want string }{
		{"/oldmount/", "/newmount/"},
		{"/oldmount/weather/data/x", "/newmount/weather/data/x"},
		{"/oldmount/pub/TOK/?a=1", "/newmount/pub/TOK/?a=1"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.from, nil))
		// 308, not 302: the method and body survive, so a POST to an old API
		// path lands as a POST instead of silently becoming a GET.
		if rec.Code != http.StatusPermanentRedirect {
			t.Errorf("%s -> %d, want 308", tc.from, rec.Code)
		}
		if got := rec.Header().Get("Location"); got != tc.want {
			t.Errorf("%s -> %q, want %q", tc.from, got, tc.want)
		}
	}

	// And the old subtree has to be past the cookie gate, or an anonymous
	// capability link redirects to a login page rather than to where it moved.
	if !isPublicPath("/oldmount/pub/TOK/") {
		t.Error("the legacy subtree must stay public, or anonymous links land on login")
	}
}

func TestALegacyMountToItselfIsIgnored(t *testing.T) {
	before := len(legacyRedirects)
	RegisterLegacyMount("/same", "/same")
	RegisterLegacyMount("", "/x")
	if len(legacyRedirects) != before {
		t.Error("a no-op or incomplete legacy mount should not register")
	}
}
