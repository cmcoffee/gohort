package scribe

// A guide is shared with whom the owner picks: people, a team they are on, or
// Everyone as one more choice, each able to view or edit. It used to be one
// "Share with everyone signed in" box and a view/edit switch for all of them.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/promotion"
	"github.com/cmcoffee/snugforge/kvlite"
)

func shareFixture(t *testing.T) (*Scribe, Database) {
	t.Helper()
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice", Groups: []string{"g-ops"}})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	adb.Set(AuthTable, "user:carol", AuthUser{Username: "carol", Groups: []string{"g-ops"}})
	adb.Set(AuthTable, "user:dan", AuthUser{Username: "dan"})
	SaveAppGroup(adb, AppGroup{ID: "g-ops", Name: "Ops", Shareable: true})
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return adb }
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })
	appDB := &DBase{Store: kvlite.MemStore()}
	saveGuide(UserDB(appDB, "alice"), Guide{ID: "g1", Title: "Runbook", Owner: "alice"})
	T := &Scribe{AppCore: AppCore{DB: appDB}}
	T.registerGuidePromotion()
	return T, appDB
}

// share PATCHes the picker's lists as user and returns the status.
func share(T *Scribe, user, body string) int {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPatch, "/scribe/share?id=g1", strings.NewReader(body))
	T.handleShare(w, r, UserDB(T.DB, user), user)
	return w.Code
}

// reach says what user gets of g1: "" (nothing), "view" or "edit".
func reach(T *Scribe, user string) string {
	r := httptest.NewRequest(http.MethodGet, "/scribe/guide?id=g1", nil)
	_, _, _, canEdit, ok := T.resolve(r, UserDB(T.DB, user), user, "g1")
	switch {
	case !ok:
		return ""
	case canEdit:
		return "edit"
	}
	return "view"
}

func TestAGuideIsSharedWithWhomTheOwnerPicks(t *testing.T) {
	T, appDB := shareFixture(t)

	// Bob by name, as an editor; the Ops team to read.
	if c := share(T, "alice", `{"allowed_users":["bob","group:g-ops"],"editors":["bob"]}`); c != http.StatusOK {
		t.Fatalf("share answered %d", c)
	}
	for user, want := range map[string]string{"bob": "edit", "carol": "view", "dan": ""} {
		if got := reach(T, user); got != want {
			t.Errorf("%s gets %q of the guide, want %q", user, got, want)
		}
	}

	// Everyone to read is asked of an administrator, not granted: dan gets
	// nothing yet, and the picker reads back what was asked for.
	if c := share(T, "alice", `{"allowed_users":["*","bob"],"editors":["bob"]}`); c != http.StatusOK {
		t.Fatalf("share answered %d", c)
	}
	if got := reach(T, "dan"); got != "" {
		t.Errorf("Everyone reached dan before an administrator approved it: %q", got)
	}
	if !PendingPromotion(AuthDB(), "alice", guidePromotionKind, "g1:view") {
		t.Fatal("Everyone filed no request")
	}
	pw := httptest.NewRecorder()
	T.handleShare(pw, httptest.NewRequest(http.MethodGet, "/scribe/share?id=g1", nil), UserDB(appDB, "alice"), "alice")
	if !strings.Contains(pw.Body.String(), `"allowed_users":["*","bob"]`) {
		t.Errorf("while it waits the picker reads %s", pw.Body.String())
	}
	if err := promotion.Approve(AuthDB(), promotion.RequestKey(guidePromotionKind, "alice", "g1:view"), "root"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	g, _ := loadGuide(UserDB(appDB, "alice"), "g1")
	if !g.Shared || g.ShareMode != "" || len(g.AllowedUsers) != 1 || g.AllowedUsers[0] != "bob" {
		t.Errorf("Everyone is stored where it always was: %+v", g)
	}
	for user, want := range map[string]string{"bob": "edit", "carol": "view", "dan": "view"} {
		if got := reach(T, user); got != want {
			t.Errorf("shared with everyone, %s gets %q, want %q", user, got, want)
		}
	}
	// Asking for everyone to edit is a new request; viewing stays meanwhile.
	share(T, "alice", `{"allowed_users":["*","bob"],"editors":["*","bob"]}`)
	if got := reach(T, "dan"); got != "view" || !PendingPromotion(AuthDB(), "alice", guidePromotionKind, "g1:edit") {
		t.Errorf("asking for edit: dan gets %q, request pending %v", got, PendingPromotion(AuthDB(), "alice", guidePromotionKind, "g1:edit"))
	}
	// Taking edit back withdraws the request, and needs nobody.
	share(T, "alice", `{"allowed_users":["*","bob"],"editors":["bob"]}`)
	if PendingPromotion(AuthDB(), "alice", guidePromotionKind, "g1:edit") {
		t.Error("taking edit back left the request waiting")
	}
	// The picker reads Everyone back as one more choice.
	w := httptest.NewRecorder()
	T.handleShare(w, httptest.NewRequest(http.MethodGet, "/scribe/share?id=g1", nil), UserDB(appDB, "alice"), "alice")
	var got guideShare
	json.Unmarshal(w.Body.Bytes(), &got)
	if strings.Join(got.AllowedUsers, ",") != "*,bob" || strings.Join(got.Editors, ",") != "bob" {
		t.Errorf("the picker reads %+v", got)
	}

	// Taking Everyone off takes dan's copy away on the next read. An editor the
	// share no longer reaches is not kept as one.
	if c := share(T, "alice", `{"allowed_users":["bob"],"editors":["bob","carol"]}`); c != http.StatusOK {
		t.Fatalf("share answered %d", c)
	}
	g, _ = loadGuide(UserDB(appDB, "alice"), "g1")
	if g.Shared || strings.Join(g.Editors, ",") != "bob" {
		t.Errorf("after narrowing: %+v", g)
	}
	for user, want := range map[string]string{"bob": "edit", "carol": "", "dan": ""} {
		if got := reach(T, user); got != want {
			t.Errorf("narrowed to bob, %s gets %q, want %q", user, got, want)
		}
	}

	// Only the owner changes who it reaches, editor or not.
	if c := share(T, "bob", `{"allowed_users":["bob","dan"]}`); c != http.StatusForbidden {
		t.Errorf("an editor changed the sharing: %d", c)
	}

	// The list labels it for the person it reaches.
	lw := httptest.NewRecorder()
	T.handleList(lw, httptest.NewRequest(http.MethodGet, "/scribe/guides", nil), UserDB(appDB, "bob"), "bob")
	if !strings.Contains(lw.Body.String(), "Runbook - shared (editable)") {
		t.Errorf("bob's list: %s", lw.Body.String())
	}

	// Restoring an earlier version keeps who it is shared with.
	saveGuideRev(UserDB(appDB, "alice"), g, "edit")
	revs := listRevisions(UserDB(appDB, "alice"), "g1")
	if len(revs) == 0 {
		t.Fatal("no revision to restore")
	}
	share(T, "alice", `{"allowed_users":["bob","dan"],"editors":["bob"]}`)
	rw := httptest.NewRecorder()
	T.handleRestore(rw, httptest.NewRequest(http.MethodPost, "/scribe/restore?id=g1&rev="+revs[0].ID, nil), UserDB(appDB, "alice"), "alice")
	if rw.Code != http.StatusOK {
		t.Fatalf("restore answered %d: %s", rw.Code, rw.Body.String())
	}
	if got := reach(T, "dan"); got != "view" {
		t.Errorf("a restore took the share away from dan: %q", got)
	}
}
