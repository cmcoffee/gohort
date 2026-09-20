package customapps

import (
	"encoding/json"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/promotion"
	"github.com/cmcoffee/snugforge/kvlite"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain sets ONE package-wide sandbox workspace dir. SetWorkspacesDir is
// "call once" in production; the sandbox pins a global pydeps path under the
// first workspace root, so per-test dirs that were created and then deleted
// mid-run left later sandbox tests pointing at a vanished path (bwrap: "Can't
// find source path …/_gohort_pydeps"). One dir for the whole package run keeps
// the sandbox consistent. Built under $HOME because the sandbox mounts a fresh
// tmpfs over /tmp (which would shadow a workspace placed there).
func TestMain(m *testing.M) {
	if home, err := os.UserHomeDir(); err == nil {
		if ws, e := os.MkdirTemp(home, "gohort-customapps-test-"); e == nil {
			SetWorkspacesDir(filepath.Join(ws, "workspaces"))
			code := m.Run()
			os.RemoveAll(ws)
			os.Exit(code)
		}
	}
	os.Exit(m.Run())
}

// TestRunDataSourcePython dispatches a trivial data-source script end to end:
// the app's records + a query param go in as env vars, the python prints a JSON
// array, and we get it back. Skips when the sandbox/python isn't available in the
// test environment (CI without python3 / bwrap), since this exercises real exec.
func TestRunDataSourcePython(t *testing.T) {
	// Workspace is set once for the package in TestMain.
	ds := AppDataSource{
		Name:     "demo",
		Language: "python",
		// Echo back the record count + the query param, as a one-row table.
		Script: "import os, json\n" +
			"recs = json.loads(os.environ.get('records', '[]'))\n" +
			"print(json.dumps([{'n': len(recs), 'q': os.environ.get('q', '')}]))\n",
		Capabilities: []string{}, // non-nil empty → no hook started (no network needed)
	}
	args := map[string]any{
		"records": `[{"a":1},{"a":2}]`,
		"q":       "hello",
	}

	out, err := runDataSource("tester", nil, "demo-app", ds, args)
	if err != nil {
		t.Skipf("sandbox/python unavailable in this environment: %v", err)
	}
	out = strings.TrimSpace(out)
	if !json.Valid([]byte(out)) {
		t.Skipf("non-JSON output (likely no python3/sandbox): %q", out)
	}
	var rows []map[string]any
	if e := json.Unmarshal([]byte(out), &rows); e != nil {
		t.Fatalf("output is not a JSON array: %q (%v)", out, e)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one row, got %d: %q", len(rows), out)
	}
	if rows[0]["q"] != "hello" {
		t.Fatalf("query param not passed through: %q", out)
	}
	if n, _ := rows[0]["n"].(float64); n != 2 {
		t.Fatalf("records not passed through (expected n=2): %q", out)
	}
}

// TestRunAppScriptAction dispatches an action-shaped script (prints a JSON object
// {message, records}) through the shared runner — the write-side seam. The
// framework's upsert of result.Records lives in handleAction; here we just prove
// the script runs and its object output round-trips.
func TestRunAppScriptAction(t *testing.T) {
	// Workspace is set once for the package in TestMain.
	script := "import os, json\n" +
		"recs = json.loads(os.environ.get('records', '[]'))\n" +
		"print(json.dumps({'message': 'synced ' + os.environ.get('note',''), 'records': recs + [{'id':'new'}]}))\n"
	args := map[string]any{"records": `[{"id":"a"}]`, "note": "ok"}

	out, err := runAppScript("tester", nil, "demo-app", "action", "sync", "python", script, []string{}, args)
	if err != nil {
		t.Skipf("sandbox/python unavailable: %v", err)
	}
	out = strings.TrimSpace(out)
	if !json.Valid([]byte(out)) {
		t.Skipf("non-JSON output (likely no python3/sandbox): %q", out)
	}
	var result struct {
		Message string           `json:"message"`
		Records []map[string]any `json:"records"`
	}
	if e := json.Unmarshal([]byte(out), &result); e != nil {
		t.Fatalf("output is not a JSON object: %q (%v)", out, e)
	}
	if result.Message != "synced ok" {
		t.Fatalf("message/param not passed through: %q", out)
	}
	if len(result.Records) != 2 {
		t.Fatalf("expected 2 records back (1 in + 1 added): %q", out)
	}
}

// TestDSCacheKey pins the cache-key contract that makes cachedRunDataSource safe
// for the author's rapid iterate→verify loop and correct as records change:
// same inputs → same key (a cache hit), but a change to the SCRIPT, the records,
// or a query param → a different key (a miss that recomputes). The script clause
// is the subtle one: without it, editing a script and re-verifying within the
// TTL — same records, same params — would serve the OLD script's output.
func TestDSCacheKey(t *testing.T) {
	base := AppDataSource{Name: "src", Language: "python", Script: "print(1)"}
	args := map[string]any{"records": `[{"a":1}]`, "q": "x"}
	key := dsCacheKey("owner", "app", base, args)

	// Identical inputs must reuse the key.
	if got := dsCacheKey("owner", "app", base, args); got != key {
		t.Fatalf("identical inputs changed the key:\n %s\n %s", key, got)
	}

	// Each of these single-field changes must MISS (distinct key).
	changed := map[string]string{
		"script": func() string {
			d := base
			d.Script = "print(2)"
			return dsCacheKey("owner", "app", d, args)
		}(),
		"language": func() string {
			d := base
			d.Language = "bash"
			return dsCacheKey("owner", "app", d, args)
		}(),
		"capabilities": func() string {
			d := base
			d.Capabilities = []string{"fetch"}
			return dsCacheKey("owner", "app", d, args)
		}(),
		"name":   dsCacheKey("owner", "app", AppDataSource{Name: "other", Language: "python", Script: "print(1)"}, args),
		"owner":  dsCacheKey("other", "app", base, args),
		"slug":   dsCacheKey("owner", "other", base, args),
		"record": dsCacheKey("owner", "app", base, map[string]any{"records": `[{"a":2}]`, "q": "x"}),
		"param":  dsCacheKey("owner", "app", base, map[string]any{"records": `[{"a":1}]`, "q": "y"}),
	}
	for what, got := range changed {
		if got == key {
			t.Errorf("changing %s did NOT change the cache key — stale output would be served", what)
		}
	}
}

// sharingTestApp pins RootDB to a fresh mem store (appSpecStore resolves through
// it) and returns a CustomApps whose T.DB — the app-wide store holding the
// shared/public indexes — is its own fresh mem store. Restores RootDB on cleanup.
func sharingTestApp(t *testing.T) *CustomApps {
	t.Helper()
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	T := &CustomApps{}
	T.DB = &DBase{Store: kvlite.MemStore()}
	return T
}

// TestResolveSpecSharing checks the own-first resolution contract: a requester's
// own app always wins; a non-owner reaches another user's app only when it's
// shared; ownerUser is reported correctly (it's the identity scripts run as).
func TestResolveSpecSharing(t *testing.T) {
	T := sharingTestApp(t)

	// alice owns a private app; bob owns a shared one, both slug "tool".
	SaveAppSpec(AppSpec{Slug: "priv", Name: "Alice Private", Owner: "alice"})
	SaveAppSpec(AppSpec{Slug: "tool", Name: "Alice Tool", Owner: "alice"})
	bobShared := SaveAppSpec(AppSpec{Slug: "shared", Name: "Bob Shared", Owner: "bob", Shared: true})
	SetSharedOwner(T.DB, sharedAppsIndex, "shared", "bob", true)
	// bob also has his OWN "tool" (unshared) — must shadow alice's for bob.
	SaveAppSpec(AppSpec{Slug: "tool", Name: "Bob Tool", Owner: "bob"})

	// Owner reaches their own app; ownerUser == requester.
	if s, owner, ok := T.resolveSpec("alice", "priv"); !ok || owner != "alice" || s.Name != "Alice Private" {
		t.Fatalf("owner-own resolve failed: %v %q ok=%v", s.Name, owner, ok)
	}
	// Non-owner reaches bob's shared app; ownerUser == bob (scripts run as bob).
	if s, owner, ok := T.resolveSpec("alice", "shared"); !ok || owner != "bob" || s.Name != bobShared.Name {
		t.Fatalf("shared resolve failed: %v %q ok=%v", s.Name, owner, ok)
	}
	// A non-shared app is invisible to a non-owner.
	if _, _, ok := T.resolveSpec("bob", "priv"); ok {
		t.Fatalf("bob resolved alice's PRIVATE app — leak")
	}
	// Own slug shadows a foreign one: bob's own "tool" wins over alice's "tool".
	if s, owner, ok := T.resolveSpec("bob", "tool"); !ok || owner != "bob" || s.Name != "Bob Tool" {
		t.Fatalf("own-app shadowing failed: got %q owner=%q", s.Name, owner)
	}
}

// The anonymous capability surface runs the OWNER'S sandboxed script on every
// data request, and the query params that key its output cache come from
// whoever holds the link. Unthrottled that was one subprocess per request and
// one cache entry per distinct param set, neither of which had a ceiling.
// fromAddr builds a request whose TCP peer is addr.
func fromAddr(target, addr string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	r.RemoteAddr = addr
	return r
}

func TestDataSourceCacheIsBounded(t *testing.T) {
	// The key includes the caller's query params, so on the public surface a
	// varied param both misses the cache and writes an entry. The sweep only
	// removes EXPIRED entries, so without a hard cap the map grows for as long
	// as requests keep arriving inside the TTL.
	dsCacheMu.Lock()
	dsCache = map[string]dsCacheEntry{}
	for i := 0; i < maxDSCacheEntries*3; i++ {
		dsCache[string(rune(i))+"-k"] = dsCacheEntry{out: "x", expires: time.Now().Add(time.Hour)}
	}
	over := len(dsCache)
	dsCacheMu.Unlock()

	if over <= maxDSCacheEntries {
		t.Skip("fixture did not exceed the cap")
	}
	// Drive one cached run so the eviction path executes.
	dsCacheMu.Lock()
	trimDSCache(time.Now())
	dsCacheMu.Unlock()

	dsCacheMu.Lock()
	size := len(dsCache)
	dsCacheMu.Unlock()
	if size > maxDSCacheEntries {
		t.Errorf("cache stayed above its cap: %d > %d", size, maxDSCacheEntries)
	}
}

// TestShareIsAPublishRequestForNonAdmins covers the publish gate. A non-admin
// owner's Share becomes a pending promotion request and the app stays
// unshared; the index row says so; an administrator approving the request
// (through the registered "app" approver) is what shares it; the owner can
// still un-share directly; and an admin owner never has to ask.
func TestShareIsAPublishRequestForNonAdmins(t *testing.T) {
	T := sharingTestApp(t)
	auth := &DBase{Store: kvlite.MemStore()}
	savedAuth, savedAdmin := AuthDB, requestIsAdmin
	AuthDB = func() Database { return auth }
	requestIsAdmin = func(*http.Request) bool { return false }
	t.Cleanup(func() { AuthDB, requestIsAdmin = savedAuth, savedAdmin })
	promotion.RegisterApprover("app", T.approvePublish)

	SaveAppSpec(AppSpec{Slug: "tally", Name: "Tally", Owner: "alice"})

	share := func(on string, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/apps/_app/share?slug=tally&on="+on, strings.NewReader(body))
		T.handleShareApp(w, r, "alice")
		return w
	}

	// Share from a non-admin = a request, not a share.
	w := share("true", `{"note":"the team wants it"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"requested":true`) {
		t.Fatalf("non-admin share: %d %s", w.Code, w.Body.String())
	}
	if s, _ := loadSpec("alice", "tally"); s.Shared {
		t.Fatal("the app must not be shared until an admin approves")
	}
	if _, shared := LookupSharedOwner(T.DB, sharedAppsIndex, "tally"); shared {
		t.Fatal("the shared-slug index must not list a merely requested app")
	}
	reqs := ListPromotionRequests(auth, true)
	if len(reqs) != 1 || reqs[0].Kind != "app" || reqs[0].Owner != "alice" || reqs[0].Name != "tally" || reqs[0].Note != "the team wants it" {
		t.Fatalf("pending queue = %+v", reqs)
	}

	// The owner's list carries the state so the modal can say who it waits on.
	w = httptest.NewRecorder()
	T.handleAppsList(w, httptest.NewRequest(http.MethodGet, "/apps/_apps", nil), "alice")
	if !strings.Contains(w.Body.String(), `"requested":"1"`) || !strings.Contains(w.Body.String(), `"status":"publish requested"`) {
		t.Fatalf("row must show the request: %s", w.Body.String())
	}

	// A conflicting slug refuses the approval and leaves the request pending.
	SaveAppSpec(AppSpec{Slug: "tally", Name: "Bob Tally", Owner: "bob", Shared: true})
	SetSharedOwner(T.DB, sharedAppsIndex, "tally", "bob", true)
	id := PromotionRequestKey("app", "alice", "tally")
	if err := promotion.Approve(auth, id, "root"); err == nil {
		t.Fatal("approval must refuse a slug another user already shares")
	}
	if !PendingPromotion(auth, "alice", "app", "tally") {
		t.Fatal("a refused approval must leave the request pending")
	}
	SetSharedOwner(T.DB, sharedAppsIndex, "tally", "bob", false)

	// Admin approval is what shares it.
	if err := promotion.Approve(auth, id, "root"); err != nil {
		t.Fatal(err)
	}
	if s, _ := loadSpec("alice", "tally"); !s.Shared {
		t.Fatal("approval must share the app")
	}
	if owner, shared := LookupSharedOwner(T.DB, sharedAppsIndex, "tally"); !shared || owner != "alice" {
		t.Fatalf("index after approval = %q %v", owner, shared)
	}
	if PendingPromotion(auth, "alice", "app", "tally") {
		t.Fatal("an approved request is no longer pending")
	}

	// The owner un-shares directly — revoking your own share needs no one.
	if w = share("false", ""); w.Code != http.StatusOK {
		t.Fatalf("un-share: %d %s", w.Code, w.Body.String())
	}
	if s, _ := loadSpec("alice", "tally"); s.Shared {
		t.Fatal("un-share must be immediate")
	}

	// An admin owner shares directly, no request filed.
	requestIsAdmin = func(*http.Request) bool { return true }
	if w = share("true", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"shared":true`) {
		t.Fatalf("admin share: %d %s", w.Code, w.Body.String())
	}
	if n := len(ListPromotionRequests(auth, true)); n != 0 {
		t.Fatalf("an admin's share must not queue a request; pending = %d", n)
	}
}
