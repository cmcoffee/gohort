package orchestrate

// The Sources picker. Its job is to make an attachment visible and
// editable without going through Builder, so the tests here are about
// the two ways that fails quietly: a button wired to nothing, and a save
// that round-trips into a different shape than the one the agent reads.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

// newTestOrchestrate stands up an app with a real logged-in user, since
// every route here goes through RequireUser.
func newTestOrchestrate(t *testing.T) (*OrchestrateApp, Database, string) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
	token = AuthCreateSession(adb, "alice")
	return &OrchestrateApp{AppCore: AppCore{DB: root}}, UserDB(root, "alice"), "alice"
}

// token is the session cookie newTestOrchestrate minted, carried to
// asUser so requests arrive authenticated.
var token string

func asUser(r *http.Request, user string) *http.Request {
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
	return r
}

// fakeRefSource stands in for a producer app (a file store, servitor).
// It implements ReferenceToolProvider because the tools are the point:
// an attachment that mints no named tools is an attachment nobody can
// ask the agent to use.
type fakeRefSource struct{ kind, label string }

func (f fakeRefSource) Kind() string  { return f.kind }
func (f fakeRefSource) Label() string { return f.label }
func (f fakeRefSource) List(user string) []ReferenceItem {
	return []ReferenceItem{{ID: "prod", Name: "Prod logs", Desc: "Nightly capture."}}
}
func (f fakeRefSource) Fetch(ctx context.Context, user, itemID, query string) string { return "" }
func (f fakeRefSource) ItemTools(user, itemID string) []AgentToolDef {
	return []AgentToolDef{
		{Tool: Tool{Name: "search_prod_logs"}},
		{Tool: Tool{Name: "read_prod_file"}},
	}
}

func TestSourcesModal_ToolbarEntryAndHandlerAgree(t *testing.T) {
	page, err := os.ReadFile("page_chat.go")
	if err != nil {
		t.Fatalf("read page: %v", err)
	}
	assets, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatalf("read assets: %v", err)
	}
	const action = "orchestrate_sources_modal"
	if !strings.Contains(string(page), `URL: "`+action+`"`) {
		t.Errorf("no toolbar entry points at %s — the modal is unreachable", action)
	}
	if !strings.Contains(string(assets), `uiRegisterClientAction('`+action+`'`) {
		t.Errorf("%s is named by the toolbar but never registered — the button would do nothing", action)
	}
	// It must post to its own endpoint, not the agent record: a picker
	// showing one field that POSTs a whole record clobbers what it never
	// loaded.
	if !strings.Contains(string(assets), "post_to: 'api/reference-sources?'") {
		t.Error("the picker should save through /api/reference-sources")
	}
}

// The handle the picker stores has to be the same handle the tool
// documents. Two spellings for one concept is how an attachment made in
// the UI reads as absent to the agent.
func TestReferenceRefRoundTrips(t *testing.T) {
	sel := ReferenceSelection{Kind: "files", ItemID: "prod"}
	back, ok := ParseReferenceRef(sel.Ref())
	if !ok || back != sel {
		t.Fatalf("%q did not round-trip: %+v ok=%v", sel.Ref(), back, ok)
	}
	// Guides' older double-colon form still parses, and an item id
	// containing a colon survives the single-colon form.
	if got, ok := ParseReferenceRef("system::abc"); !ok || got.Kind != "system" || got.ItemID != "abc" {
		t.Errorf("double-colon form broke: %+v ok=%v", got, ok)
	}
	if got, ok := ParseReferenceRef("mcp:server:tool"); !ok || got.Kind != "mcp" || got.ItemID != "server:tool" {
		t.Errorf("split should take the FIRST colon: %+v ok=%v", got, ok)
	}
	for _, bad := range []string{"", "files", ":prod", "files:", "  "} {
		if _, ok := ParseReferenceRef(bad); ok {
			t.Errorf("%q should not parse", bad)
		}
	}
}

// The record has always stored objects while the agent tool has always
// documented strings. Nothing posted the field over HTTP until this
// picker, so the mismatch was invisible; a recipe or a Builder-authored
// body using the documented form must not 400 the whole save.
func TestAttachedSourcesAcceptsEitherShape(t *testing.T) {
	var rec struct {
		AttachedSources ReferenceSelections `json:"attached_sources"`
	}
	if err := json.Unmarshal([]byte(`{"attached_sources":["files:prod","system::abc"]}`), &rec); err != nil {
		t.Fatalf("string form rejected: %v", err)
	}
	if len(rec.AttachedSources) != 2 || rec.AttachedSources[0].ItemID != "prod" {
		t.Fatalf("string form decoded wrong: %+v", rec.AttachedSources)
	}
	rec.AttachedSources = nil
	if err := json.Unmarshal([]byte(`{"attached_sources":[{"kind":"files","item_id":"prod"}]}`), &rec); err != nil {
		t.Fatalf("object form rejected: %v", err)
	}
	if len(rec.AttachedSources) != 1 || rec.AttachedSources[0].Kind != "files" {
		t.Fatalf("object form decoded wrong: %+v", rec.AttachedSources)
	}
	// One malformed entry drops itself rather than the whole save, and
	// duplicates collapse — a picker can submit the same handle twice if
	// two option rows resolve to one item.
	rec.AttachedSources = nil
	if err := json.Unmarshal([]byte(`{"attached_sources":["files:prod","garbage","files:prod"]}`), &rec); err != nil {
		t.Fatalf("mixed list rejected: %v", err)
	}
	if len(rec.AttachedSources) != 1 {
		t.Fatalf("expected one surviving selection, got %+v", rec.AttachedSources)
	}
}

// The option rows must name the tools each attachment mints. This is the
// answer to "I attached it and the agent has no idea what I'm talking
// about": the tool is called search_prod_logs and nothing else in the UI
// ever says so.
func TestSourceRowNamesTheToolsItAdds(t *testing.T) {
	RegisterReferenceSource(fakeRefSource{kind: "testfiles", label: "Test files"})
	desc := refPickerDesc("u", "testfiles", ReferenceItem{ID: "prod", Desc: "Nightly capture."})
	if !strings.Contains(desc, "search_prod_logs") || !strings.Contains(desc, "read_prod_file") {
		t.Errorf("row does not name the minted tools: %q", desc)
	}
	if !strings.Contains(desc, "Nightly capture.") {
		t.Errorf("row dropped the item's own description: %q", desc)
	}
}

func TestReferenceSourcesEndpointReadsAndWrites(t *testing.T) {
	RegisterReferenceSource(fakeRefSource{kind: "testfiles", label: "Test files"})
	app, udb, user := newTestOrchestrate(t)
	rec := AgentRecord{ID: "a1", Name: "Investigator", OrchestratorPrompt: "You investigate."}
	saveAgent(udb, rec)

	get := httptest.NewRequest("GET", "/api/reference-sources?agent=a1", nil)
	w := httptest.NewRecorder()
	app.handleReferenceSources(w, asUser(get, user))
	if w.Code != 200 {
		t.Fatalf("GET: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Items []struct {
			ID, Name, Desc, Group string
		} `json:"items"`
		Attached []string `json:"attached"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var found bool
	for _, it := range out.Items {
		if it.ID == "testfiles:prod" {
			found = true
			if it.Group != "Test files" {
				t.Errorf("row should carry its group header, got %q", it.Group)
			}
			if !strings.Contains(it.Desc, "search_prod_logs") {
				t.Errorf("row should name its tools, got %q", it.Desc)
			}
		}
	}
	if !found {
		t.Fatalf("source missing from the picker: %+v", out.Items)
	}
	if len(out.Attached) != 0 {
		t.Errorf("nothing is attached yet, got %v", out.Attached)
	}

	post := httptest.NewRequest("POST", "/api/reference-sources?agent=a1",
		strings.NewReader(`{"references":["testfiles:prod","junk"]}`))
	w = httptest.NewRecorder()
	app.handleReferenceSources(w, asUser(post, user))
	if w.Code != 200 {
		t.Fatalf("POST: %d %s", w.Code, w.Body.String())
	}
	saved, ok := loadAgent(udb, "a1")
	if !ok {
		t.Fatal("agent vanished")
	}
	if len(saved.AttachedSources) != 1 || saved.AttachedSources[0].Kind != "testfiles" || saved.AttachedSources[0].ItemID != "prod" {
		t.Fatalf("attachment did not land as a selection: %+v", saved.AttachedSources)
	}

	// And it reads back as attached — a picker that saves but reopens
	// empty is worse than one that refuses, because it looks like the
	// attachment did not take and invites a second one.
	w = httptest.NewRecorder()
	app.handleReferenceSources(w, asUser(httptest.NewRequest("GET", "/api/reference-sources?agent=a1", nil), user))
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if len(out.Attached) != 1 || out.Attached[0] != "testfiles:prod" {
		t.Fatalf("attached set did not survive the round trip: %v", out.Attached)
	}
}

func TestReferenceSourcesRefusesWithoutAnAgent(t *testing.T) {
	app, _, user := newTestOrchestrate(t)
	w := httptest.NewRecorder()
	app.handleReferenceSources(w, asUser(httptest.NewRequest("GET", "/api/reference-sources", nil), user))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 without an agent, got %d", w.Code)
	}
	w = httptest.NewRecorder()
	app.handleReferenceSources(w, asUser(httptest.NewRequest("GET", "/api/reference-sources?agent=nope", nil), user))
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for an unknown agent, got %d", w.Code)
	}
}

// The scope plane: the same question the picker answers, asked from the
// source's side. It exists because checking four agents one at a time is
// how you come to believe an attachment took when it did not.
func TestSourceScopeLinksAndUnlinks(t *testing.T) {
	RegisterReferenceSource(fakeRefSource{kind: "testfiles", label: "Test files"})
	root := &DBase{Store: kvlite.MemStore()}
	prevRoot := RootDB
	RootDB = root
	t.Cleanup(func() { RootDB = prevRoot })
	udb := agentUserDB(root, "alice")
	saveAgent(udb, AgentRecord{ID: "a1", Name: "Investigator", OrchestratorPrompt: "x"})
	saveAgent(udb, AgentRecord{ID: "a2", Name: "Chatter", OrchestratorPrompt: "x"})

	prov, ok := ScopeProviderFor("source")
	if !ok {
		t.Fatal("no source scope provider — the pill has no backend")
	}
	st, ok := prov.State(root, "alice", "testfiles:prod")
	if !ok {
		t.Fatal("state not found for a reachable source")
	}
	if st.Global {
		t.Error("a source must never report a global scope")
	}
	if len(st.Agents) != 2 {
		t.Fatalf("expected both agents as targets, got %+v", st.Agents)
	}
	for _, a := range st.Agents {
		if a.On {
			t.Errorf("%s should start unlinked", a.Name)
		}
	}

	if err := prov.Set(root, "alice", "testfiles:prod", "a1", true); err != nil {
		t.Fatalf("link: %v", err)
	}
	got, _ := loadAgent(udb, "a1")
	if len(got.AttachedSources) != 1 || got.AttachedSources[0].ItemID != "prod" {
		t.Fatalf("link did not land: %+v", got.AttachedSources)
	}
	// Linking twice must not duplicate — the pill can be double-clicked,
	// and a duplicate attachment mints the same tool name twice.
	if err := prov.Set(root, "alice", "testfiles:prod", "a1", true); err != nil {
		t.Fatalf("re-link: %v", err)
	}
	got, _ = loadAgent(udb, "a1")
	if len(got.AttachedSources) != 1 {
		t.Fatalf("re-link duplicated: %+v", got.AttachedSources)
	}

	if err := prov.Set(root, "alice", "testfiles:prod", "a1", false); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	got, _ = loadAgent(udb, "a1")
	if len(got.AttachedSources) != 0 {
		t.Fatalf("unlink did not take: %+v", got.AttachedSources)
	}
}

// No global scope. "Every agent I own can read this folder" is a grant
// nobody should be able to make by accident, and a silently-ignored
// toggle is worse than a refusal because the pill would show it as on.
func TestSourceScopeRefusesGlobal(t *testing.T) {
	RegisterReferenceSource(fakeRefSource{kind: "testfiles", label: "Test files"})
	root := &DBase{Store: kvlite.MemStore()}
	prov, _ := ScopeProviderFor("source")
	if err := prov.Set(root, "alice", "testfiles:prod", "global", true); err == nil {
		t.Fatal("global should be refused for sources")
	}
}

// An admin can un-assign a store after it was attached. This surface
// must not become the way to keep managing one you can no longer reach.
func TestSourceScopeRefusesAnUnreachableSource(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	prov, _ := ScopeProviderFor("source")
	if _, ok := prov.State(root, "alice", "nosuchkind:item"); ok {
		t.Error("state should report not-found for a source the user cannot reach")
	}
	if err := prov.Set(root, "alice", "nosuchkind:item", "a1", true); err == nil {
		t.Error("linking an unreachable source should be refused")
	}
}

// A Configure entry that opens onto nothing reads as a broken feature.
func TestSourcesEntryHidesWhenThereIsNothingToAttach(t *testing.T) {
	actions := []ui.ToolbarAction{
		{Group: "Configure", Label: "Tools", Method: "client", URL: "orchestrate_tools_modal"},
		{Group: "Configure", Label: "Sources", Method: "client", URL: "orchestrate_sources_modal"},
		{Group: "Configure", Label: "Rules", Method: "client", URL: "orchestrate_rules_modal"},
	}
	kept := pruneToolbar(map[string]bool{"orchestrate_sources_modal": true}, actions)
	if len(kept) != 2 {
		t.Fatalf("expected the Sources entry dropped, got %d entries", len(kept))
	}
	// Order of what remains must not change: the Configure menu should
	// not reshuffle depending on what you happen to have configured.
	if kept[0].Label != "Tools" || kept[1].Label != "Rules" {
		t.Errorf("pruning reordered the menu: %+v", kept)
	}
	if got := pruneToolbar(nil, actions); len(got) != 3 {
		t.Errorf("with nothing hidden every entry stays, got %d", len(got))
	}
}
