package customapps

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// groupShareFixture: alice and bob in "Team" (members may share), alice also
// in "Everyone" (they may not), carol in no group.
func groupShareFixture(t *testing.T) (*CustomApps, AppGroup, AppGroup) {
	t.Helper()
	T := sharingTestApp(t)
	auth := &DBase{Store: kvlite.MemStore()}
	saved := AuthDB
	AuthDB = func() Database { return auth }
	team, _ := SaveAppGroup(auth, AppGroup{Name: "Team", Apps: []string{"/apps"}, Shareable: true})
	everyone, _ := SaveAppGroup(auth, AppGroup{Name: "Everyone", Apps: []string{"/apps"}})
	t.Cleanup(func() {
		DeleteAppGroup(auth, team.ID)
		DeleteAppGroup(auth, everyone.ID)
		AuthDB = saved
	})
	AuthSetUser(auth, "alice", "pw", false)
	AuthSetUser(auth, "bob", "pw", false)
	AuthSetUser(auth, "carol", "pw", false)
	AuthSetUserGroups(auth, "alice", []string{team.ID, everyone.ID})
	AuthSetUserGroups(auth, "bob", []string{team.ID})
	SaveAppSpec(AppSpec{Slug: "tally", Name: "Tally", Owner: "alice"})
	return T, team, everyone
}

// The owner shares with a group they are in, no administrator: its members
// reach the app, nobody else does, and a group whose administrator did not
// open it for sharing is refused.
func TestAnOwnerSharesAnAppWithTheirGroup(t *testing.T) {
	T, team, everyone := groupShareFixture(t)

	if gs := shareableGroupsOf("alice"); len(gs) != 1 || gs[0].ID != team.ID {
		t.Fatalf("alice may share with %v, want only Team", gs)
	}
	if err := T.setGroupShare("alice", "tally", everyone.ID, true); err == nil {
		t.Error("a group not opened for sharing was shared with")
	}
	if err := T.setGroupShare("carol", "tally", team.ID, true); err == nil {
		t.Error("someone who does not own the app shared it")
	}

	if err := T.setGroupShare("alice", "tally", team.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, owner, ok := T.resolveSpec("bob", "tally"); !ok || owner != "alice" {
		t.Errorf("bob, in the group, should open alice's app: ok=%v owner=%q", ok, owner)
	}
	if _, _, ok := T.resolveSpec("carol", "tally"); ok {
		t.Error("carol, in no group, opened it")
	}

	// The group is shut for sharing: every share to it stops at once.
	team.Shareable = false
	SaveAppGroup(AuthDB(), team)
	if _, _, ok := T.resolveSpec("bob", "tally"); ok {
		t.Error("a group that stopped letting members share still carried the app")
	}
	spec, _ := loadSpec("alice", "tally")
	if _, lapsed := sharedGroupNames(spec); len(lapsed) != 1 {
		t.Errorf("the owner should be told the share lapsed: %v", lapsed)
	}
	team.Shareable = true
	SaveAppGroup(AuthDB(), team)

	// The owner leaves the group: it is no longer theirs to share with.
	AuthSetUserGroups(AuthDB(), "alice", []string{everyone.ID})
	if _, _, ok := T.resolveSpec("bob", "tally"); ok {
		t.Error("the share outlived the owner leaving the group")
	}
	AuthSetUserGroups(AuthDB(), "alice", []string{team.ID, everyone.ID})

	// Stopping the deployment-wide share leaves the group share standing.
	if err := T.setShared("alice", "tally", false); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := T.resolveSpec("bob", "tally"); !ok {
		t.Error("turning off the everyone-share took the group share with it")
	}

	// Unsharing the group: the app is alice's alone again.
	if err := T.setGroupShare("alice", "tally", team.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := T.resolveSpec("bob", "tally"); ok {
		t.Error("bob still opens it after the group share was removed")
	}
	if _, shared := LookupSharedOwner(T.DB, sharedAppsIndex, "tally"); shared {
		t.Error("nothing shares the app, yet the slug is still in the shared index")
	}
}

// My Apps lists a group-shared app for a member, naming the group, and offers
// the owner the groups they may share with.
func TestMyAppsShowsGroupShares(t *testing.T) {
	T, team, _ := groupShareFixture(t)
	if err := T.setGroupShare("alice", "tally", team.ID, true); err != nil {
		t.Fatal(err)
	}
	saved := requestIsAdmin
	requestIsAdmin = func(*http.Request) bool { return false }
	t.Cleanup(func() { requestIsAdmin = saved })

	w := httptest.NewRecorder()
	T.handleAppsList(w, httptest.NewRequest(http.MethodGet, "/apps/_list", nil), "alice")
	if !strings.Contains(w.Body.String(), `\"name\":\"Team\"`) || !strings.Contains(w.Body.String(), "shared with Team") {
		t.Errorf("the owner's row should offer Team and say it is shared with it:\n%s", w.Body.String())
	}
	spec, _ := loadSpec("alice", "tally")
	if !appReaches(spec, "bob") || appReaches(spec, "carol") {
		t.Error("appReaches disagrees with the group share")
	}
}
