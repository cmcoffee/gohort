package filestore

// The path scope is what lets ANOTHER app's tool take a folder name as a
// parameter and have it checked when the tool runs. Quoting is not
// containment, and an enum cannot express a set that changes — which is
// the whole point of a drop folder.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func scopeFixture(t *testing.T) (*FileStoreApp, Store, string) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"scan-2026-08-13", "scan-2026-08-12"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	st, err := SaveStore(app.DB, Store{Name: "Support bundles", Path: root})
	if err != nil {
		t.Fatalf("save store: %v", err)
	}
	return app, st, root
}

func TestScopeResolvesAFolderToAnAbsolutePath(t *testing.T) {
	app, st, root := scopeFixture(t)

	got, err := app.resolveScope("u", st.Slug, "scan-2026-08-13")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// Absolute, so a template gets something unambiguous regardless of
	// what working directory the target happens to have.
	if !filepath.IsAbs(got) {
		t.Errorf("expected an absolute path, got %q", got)
	}
	if resolved, _ := filepath.EvalSymlinks(filepath.Join(root, "scan-2026-08-13")); got != resolved {
		t.Errorf("resolved to %q, want %q", got, resolved)
	}
}

func TestScopeRefusesWhatQuotingWouldHaveAllowed(t *testing.T) {
	app, st, _ := scopeFixture(t)

	// Each of these is a perfectly well-formed single argument that
	// shell-quoting would pass through untouched.
	for _, bad := range []string{"../", "..", "../../etc", "/etc", "nope", ""} {
		if _, err := app.resolveScope("u", st.Slug, bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	// And the refusal says what would change the answer, because a model
	// reads it and has to act on it.
	_, err := app.resolveScope("u", st.Slug, "nope")
	if err == nil || !strings.Contains(err.Error(), "Support bundles") {
		t.Errorf("the refusal should name the store to look in, got: %v", err)
	}
}

func TestScopeListsWhatIsValidNow(t *testing.T) {
	app, st, root := scopeFixture(t)

	got := app.listScope("u", st.Slug)
	if len(got) != 2 {
		t.Fatalf("expected both folders, got %v", got)
	}
	// The late-binding property: a folder that appears after the tool was
	// authored is immediately valid, which is the case an enum cannot
	// cover.
	if err := os.MkdirAll(filepath.Join(root, "scan-2026-08-14"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := app.listScope("u", st.Slug); len(got) != 3 {
		t.Errorf("a new folder should be valid without re-authoring, got %v", got)
	}
	if _, err := app.resolveScope("u", st.Slug, "scan-2026-08-14"); err != nil {
		t.Errorf("the new folder should resolve: %v", err)
	}
}

func TestScopeIsRegisteredUnderFiles(t *testing.T) {
	// The kind is what a tool parameter names ("files:<store>"), so the
	// registration and the declaration have to agree.
	if !PathScopeKnown("files:anything") {
		t.Error("the files path scope is not registered")
	}
	// An unknown kind fails CLOSED: a parameter declaring a constraint
	// nobody implements must not quietly become unconstrained.
	if _, err := ResolvePathScope("u", "", "nosuchkind:x", "value"); err == nil {
		t.Error("an unknown scope kind should refuse rather than pass")
	}
}

// The link gate (core.ResolvePathScope): a scoped root that is ALSO an
// attachable source has to be attached to the CALLING agent, not merely
// reachable by its owner. Before this, a store's own search/read tools
// appeared only on an agent it was attached to while a servitor command
// tool declaring path_scope resolved against the user — so an agent
// nobody had linked the store to could still run a command against it.
func TestScopeRequiresTheCallingAgentToBeLinked(t *testing.T) {
	app, st, _ := scopeFixture(t)
	prevApp := registeredFileStoreApp
	registeredFileStoreApp = app
	t.Cleanup(func() { registeredFileStoreApp = prevApp })

	prev := AgentHoldsReference
	t.Cleanup(func() { AgentHoldsReference = prev })
	AgentHoldsReference = func(user, agentID, kind, itemID string) bool {
		return agentID == "linked" && kind == "files" && itemID == st.Slug
	}

	ref := "files:" + st.Slug
	if _, err := ResolvePathScope("u", "linked", ref, "scan-2026-08-13"); err != nil {
		t.Errorf("a linked agent should resolve: %v", err)
	}
	_, err := ResolvePathScope("u", "unlinked", ref, "scan-2026-08-13")
	if err == nil {
		t.Fatal("an unlinked agent reached a store nobody attached to it")
	}
	// The refusal has to say what would change the answer — the model
	// cannot attach anything itself, so the sentence is for the person
	// who reads it.
	if !strings.Contains(err.Error(), "Sources") {
		t.Errorf("refusal should name where to fix it, got %q", err)
	}

	// No agent in play (a CLI path, a test) is NOT a refusal: there is no
	// attachment to check, and the user gate still applies.
	if _, err := ResolvePathScope("u", "", ref, "scan-2026-08-13"); err != nil {
		t.Errorf("a non-agent caller should still resolve: %v", err)
	}
	// Neither is a deployment with no app owning agent records — the
	// feature would be dead rather than ungated.
	AgentHoldsReference = nil
	if _, err := ResolvePathScope("u", "unlinked", ref, "scan-2026-08-13"); err != nil {
		t.Errorf("with no agent-record app wired this should resolve: %v", err)
	}
}

// The admin section renders on the ADMIN page, not on this app's own, so
// a relative URL resolves against /admin and 404s. That is exactly how it
// failed the first time it was loaded, and the section is data rather
// than code — nothing else would catch it.
func TestAdminSectionUsesAbsoluteURLs(t *testing.T) {
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	body, err := jsonOf(app.adminSection())
	if err != nil {
		t.Fatalf("marshal section: %v", err)
	}
	// Every URL this section talks to must be rooted at the app's mount.
	for _, frag := range []string{`"/filestore/api/stores"`, `/filestore/api/stores?slug={slug}`} {
		if !strings.Contains(body, frag) {
			t.Errorf("expected an absolute URL %s in the section", frag)
		}
	}
	// A bare "api/stores" anywhere means one got missed.
	if strings.Contains(body, `"api/stores`) {
		t.Error("a relative api/stores URL survives — it will 404 from the admin page")
	}
}

func jsonOf(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// The admin gate must read the AUTH store, not the app's.
//
// T.DB is global.db.Bucket("filestore") — a namespaced substore — so
// asking it for the auth table finds nothing and refuses everyone,
// including a real admin. That is how this failed the first time it was
// clicked, and the symptom (admin_only for the admin) points at
// permissions rather than at the wrong database.
func TestAdminGateReadsTheAuthStoreNotTheAppStore(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	appDB := root.Bucket("filestore") // what an app actually gets

	prev := AuthDB
	AuthDB = func() Database { return root }
	defer func() { AuthDB = prev }()

	root.Set(AuthTable, "user:boss", AuthUser{Username: "boss", Admin: true})
	token := AuthCreateSession(root, "boss")

	req := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/filestore/api/stores", nil)
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
		return r
	}

	// The bug: checked against the app's bucket, an admin is refused.
	if AuthIsAdmin(appDB, req()) {
		t.Fatal("fixture wrong — the app bucket should not resolve the session")
	}
	// The fix: checked against the auth store, the admin passes.
	w := httptest.NewRecorder()
	if !adminOnly(w, req()) {
		t.Errorf("a real admin was refused: %d %s", w.Code, w.Body.String())
	}

	// A non-admin is still refused, and told what would change it.
	root.Set(AuthTable, "user:temp", AuthUser{Username: "temp"})
	plain := AuthCreateSession(root, "temp")
	r2 := httptest.NewRequest(http.MethodGet, "/filestore/api/stores", nil)
	r2.AddCookie(&http.Cookie{Name: "gohort_session", Value: plain})
	w2 := httptest.NewRecorder()
	if adminOnly(w2, r2) {
		t.Error("a non-admin was let through")
	}
	if !strings.Contains(w2.Body.String(), "admin-only") {
		t.Errorf("the refusal should say why: %s", w2.Body.String())
	}
}

// With no users configured there is no admin to be, and gating on a role
// nobody holds locks the page for everyone.
func TestAdminGateIsOpenWhenAuthIsNotConfigured(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return root }
	defer func() { AuthDB = prev }()

	w := httptest.NewRecorder()
	if !adminOnly(w, httptest.NewRequest(http.MethodGet, "/x", nil)) {
		t.Error("a deployment with no users should not be locked out of its own config")
	}
}

// A constraint nobody can discover is a constraint nobody declares. The
// mint prompt lists the available roots so a tool author can scope a
// folder parameter to one; without this it reaches for a free string or
// a frozen enum, and the frozen enum is exactly what cannot express a
// drop folder.
func TestStoresAreAdvertisedAsPathScopeRoots(t *testing.T) {
	app, st, _ := scopeFixture(t)
	prevApp := registeredFileStoreApp
	registeredFileStoreApp = app
	defer func() { registeredFileStoreApp = prevApp }()

	var found bool
	for _, rt := range PathScopeRoots("u") {
		if rt.Ref == "files:"+st.Slug {
			found = true
			if rt.Label != st.Name {
				t.Errorf("root should carry the store's name, got %q", rt.Label)
			}
		}
	}
	if !found {
		t.Errorf("the store is not advertised as a path scope root")
	}
}

// --- who may reach a store --------------------------------------------

// Configuring a store was admin-only from the start; READING one was not
// restricted at all. An admin registers /data/customer-logs once and any
// account with an agent could attach it — the gate was on the cheap half.
func TestAssignedStoreIsInvisibleToOthers(t *testing.T) {
	app, st, _ := scopeFixture(t)
	st.AllowedUsers = []string{"alice"}
	if _, err := SaveStore(app.DB, st); err != nil {
		t.Fatalf("save: %v", err)
	}

	src := storeSource{app: app}

	// The picker shows it to alice and not to bob.
	if items := src.List("alice"); len(items) != 1 {
		t.Errorf("alice should see her store, got %v", items)
	}
	if items := src.List("bob"); len(items) != 0 {
		t.Errorf("bob should not see it, got %v", items)
	}

	// And the picker is a courtesy, not the gate. An agent record can
	// carry a stale attachment from before the restriction, and an item
	// id is a string somebody can supply — so every resolve checks too.
	if tools := src.ItemTools("bob", st.Slug); len(tools) != 0 {
		t.Errorf("bob got %d tools for a store he cannot reach", len(tools))
	}
	if txt := src.Fetch(context.Background(), "bob", st.Slug, "anything"); txt != "" {
		t.Errorf("bob fetched from a store he cannot reach: %q", txt)
	}
	if tools := src.ItemTools("alice", st.Slug); len(tools) == 0 {
		t.Error("alice lost her tools")
	}
}

// The path scope is reachable from a minted servitor command tool, which
// is a different door into the same folder.
func TestAssignedStoreIsNotReachableThroughAPathScope(t *testing.T) {
	app, st, _ := scopeFixture(t)
	st.AllowedUsers = []string{"alice"}
	if _, err := SaveStore(app.DB, st); err != nil {
		t.Fatalf("save: %v", err)
	}

	if _, err := app.resolveScope("alice", st.Slug, "scan-2026-08-13"); err != nil {
		t.Fatalf("alice should resolve: %v", err)
	}
	_, err := app.resolveScope("bob", st.Slug, "scan-2026-08-13")
	if err == nil {
		t.Fatal("bob resolved a folder in a store he cannot reach")
	}
	// The refusal must not confirm the store exists — that is a fact he
	// can do nothing with and should not have.
	if strings.Contains(err.Error(), "Support bundles") {
		t.Errorf("the refusal leaks the store's existence: %v", err)
	}
	// It is not advertised to him as a constraint either.
	for _, rt := range app.scopeRoots("bob") {
		if rt.Ref == "files:"+st.Slug {
			t.Error("a store bob cannot reach was advertised to him")
		}
	}
}

// Empty stays open: closing by default would silently break every store
// registered before this existed, and a security change that presents as
// "the tools vanished" is one nobody diagnoses correctly.
func TestUnassignedStoreStaysOpen(t *testing.T) {
	app, st, _ := scopeFixture(t)
	if len(st.AllowedUsers) != 0 {
		t.Fatal("fixture should be unrestricted")
	}
	src := storeSource{app: app}
	for _, who := range []string{"alice", "bob", ""} {
		if !st.AllowsUser(who) {
			t.Errorf("an empty list should allow %q", who)
		}
		if len(src.ItemTools(who, st.Slug)) == 0 && who != "" {
			t.Errorf("%q lost tools on an unrestricted store", who)
		}
	}
}

// The agent editor's grant rows. A file store was the one grant with no
// answer on the page where someone asks what an agent can reach:
// servitor's machines were listed there, a folder of logs was not.
func TestLinkedStoreShowsOnTheAgentGrantRow(t *testing.T) {
	app, st, _ := scopeFixture(t)
	prev := AgentHoldsReference
	t.Cleanup(func() { AgentHoldsReference = prev })
	AgentHoldsReference = func(user, agentID, kind, itemID string) bool {
		return agentID == "linked" && kind == "files" && itemID == st.Slug
	}

	got := app.grantsFor("u", "linked")
	if len(got) != 1 || got[0].Label != "Support bundles" {
		t.Fatalf("a linked store should be listed, got %+v", got)
	}
	// Read-only is the posture worth restating on a row somebody reads
	// while deciding whether to leave an attachment in place.
	if !strings.Contains(got[0].Detail, "read-only") {
		t.Errorf("the row should say it is read-only, got %q", got[0].Detail)
	}
	if len(app.grantsFor("u", "unlinked")) != 0 {
		t.Error("an unlinked agent must report no stores — the row reads \"none\"")
	}
	// With no app owning agent records, nothing can hold a link.
	AgentHoldsReference = nil
	if len(app.grantsFor("u", "linked")) != 0 {
		t.Error("with no agent-record app wired the row should be empty, not asserted")
	}
}
