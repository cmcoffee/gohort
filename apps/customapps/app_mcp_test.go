package customapps

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/gohort/core/userprofile"
	"github.com/cmcoffee/snugforge/kvlite"
)

// mcpApp is an app of u's that offers an action and one that asks first,
// with MCP open to u.
func mcpApp(t *testing.T) (*CustomApps, AppSpec) {
	t.Helper()
	T := sharingTestApp(t)
	RegisterShareableFeature(ShareableFeature{Key: "mcp", Label: "MCP"})
	SetFeatureAllowedUsers(RootDB, "mcp", []string{"u"})
	spec := SaveAppSpec(AppSpec{Slug: "books", Name: "Reading List", Desc: "Books to read.", Owner: "u",
		Actions: []AppAction{
			{Name: "add", Script: "x", Expose: true},
			{Name: "wipe", Script: "x", Expose: true, Confirm: "Delete every book?"},
		}})
	return T, spec
}

// An app's offering is what the MCP server reads: its name, description and
// tools, the one that asks first among them for the server to leave out.
func TestAnAppsOfferingIsItsNameAndTools(t *testing.T) {
	T, _ := mcpApp(t)
	SaveAppSpec(AppSpec{Slug: "plain", Name: "Plain", Owner: "u", Actions: []AppAction{{Name: "go", Script: "x"}}})
	apps := T.offeringApps("u")
	if len(apps) != 1 || apps[0].Slug != "books" || apps[0].Name != "Reading List" || apps[0].Desc != "Books to read." || len(apps[0].Tools) != 2 {
		t.Fatalf("u's offering apps: %+v", apps)
	}
	if got := (*CustomApps)(nil).offeringApps("u"); got != nil {
		t.Errorf("no apps host offers nothing: %v", got)
	}
}

// Settings has the app's MCP address with what it offers and what asks
// first, with asking in the client offered on the keys, for its owner alone;
// an app that offers nothing says so.
func TestSettingsSaysWhereAnMCPClientFindsTheApp(t *testing.T) {
	T, spec := mcpApp(t)
	r := httptest.NewRequest("GET", "https://box.example/apps/books/_settings", nil)
	sec := T.mcpSection(r, spec, "u", "u", "/apps/books")
	if sec == nil {
		t.Fatal("no section for the owner")
	}
	body, _ := json.Marshal(sec)
	for _, want := range []string{"https://box.example/mcp/app/books/", "books_add", "Asks first", "books_wipe", "Make a key for this app", appMCPKeyAction, "_settings/mcp/keys", "Ask in the client", "/keys/{id}/ask", "/keys/{id}/forget", "Runs unasked"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the section does not say %q: %s", want, body)
		}
	}
	card := sec.Body.(ui.Stack).Children[0].(ui.Card).HTML
	if !strings.Contains(card, "<b>Offers</b> books_add<") || !strings.Contains(card, "<b>Asks first</b> books_wipe:") {
		t.Errorf("the card says what is offered and what asks first: %s", card)
	}
	if T.mcpSection(r, spec, "someone", "u", "/apps/books") != nil {
		t.Error("someone other than the owner sees the section")
	}
	if b, _ := json.Marshal(sec); strings.Contains(string(b), "_settings/mcp/offer") {
		t.Errorf("an app shared with nobody offers to share over MCP: %s", b)
	}
	plain := SaveAppSpec(AppSpec{Slug: "plain", Name: "Plain", Owner: "u"})
	if sec := T.mcpSection(r, plain, "u", "u", "/apps/plain"); sec == nil || sec.Body != nil || !strings.Contains(sec.Subtitle, "offers nothing") {
		t.Errorf("an app that offers nothing: %+v", sec)
	}
	SetFeatureAllowedUsers(RootDB, "mcp", []string{"someone-else"})
	if sec := T.mcpSection(r, spec, "u", "u", "/apps/books"); sec == nil || sec.Body != nil || !strings.Contains(sec.Subtitle, "administrator") {
		t.Errorf("MCP not open to the owner: %+v", sec)
	}
}

// A key made here reaches this app alone: it is an MCP key naming the app,
// shown once with the address. It is listed here and revoked here; another
// app's key is not, and only the owner makes, sees or revokes them.
func TestAKeyMadeForAnAppIsListedAndRevokedThere(t *testing.T) {
	T, spec := mcpApp(t)
	w := appAs(t, T, spec, "u", httptest.NewRequest("POST", "/apps/books/_settings/mcp/mint", strings.NewReader(`{"client":"Claude Desktop"}`)))
	var made struct{ Key, Name, Server, Address string }
	json.Unmarshal(w.Body.Bytes(), &made)
	if w.Code != 200 || !strings.HasPrefix(made.Key, "ght_") || made.Name != "Claude Desktop for Reading List" || made.Server != "books" || !strings.HasSuffix(made.Address, "/mcp/app/books/") {
		t.Fatalf("made: %d %s", w.Code, w.Body.String())
	}
	var tok AccountToken
	for _, k := range ListAccountTokens("u") {
		if k.Name == made.Name {
			tok = k
		}
	}
	if tok.Scope == nil || tok.Scope.Kind() != KeyKindMCP || strings.Join(tok.Scope.Targets, ",") != AppToolsTarget+"books" {
		t.Fatalf("the key reaches this app alone: %+v", tok.Scope)
	}
	other := MintAccountTokenScoped("u", "general", &TokenScope{Features: []string{"mcp"}, Targets: []string{AppToolsTarget + "pantry"}})

	w = appAs(t, T, spec, "u", httptest.NewRequest("GET", "/apps/books/_settings/mcp/keys", nil))
	if !strings.Contains(w.Body.String(), made.Name) || strings.Contains(w.Body.String(), "general") || strings.Contains(w.Body.String(), made.Key) {
		t.Errorf("the app's keys: %s", w.Body.String())
	}
	w = appAs(t, T, spec, "u", httptest.NewRequest("DELETE", "/apps/books/_settings/mcp/keys/"+other.ID, nil))
	if w.Code != 404 {
		t.Errorf("revoked another app's key from here: %d", w.Code)
	}
	// Someone the app is shared with, and not offered to, makes no key and
	// sees and revokes none of the owner's.
	if w := appAs(t, T, spec, "someone", httptest.NewRequest("POST", "/apps/books/_settings/mcp/mint", strings.NewReader(`{}`))); w.Code != 403 {
		t.Errorf("someone the app is not offered to made a key: %d", w.Code)
	}
	if w := appAs(t, T, spec, "someone", httptest.NewRequest("GET", "/apps/books/_settings/mcp/keys", nil)); strings.Contains(w.Body.String(), made.Name) {
		t.Errorf("someone else sees the owner's keys: %s", w.Body.String())
	}
	if w := appAs(t, T, spec, "someone", httptest.NewRequest("DELETE", "/apps/books/_settings/mcp/keys/"+tok.ID, nil)); w.Code != 404 {
		t.Errorf("someone else revoked the owner's key: %d", w.Code)
	}
	w = appAs(t, T, spec, "u", httptest.NewRequest("DELETE", "/apps/books/_settings/mcp/keys/"+tok.ID, nil))
	if w.Code != 200 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}
	for _, k := range ListAccountTokens("u") {
		if k.ID == tok.ID {
			t.Error("the key is still there")
		}
	}

	plain := SaveAppSpec(AppSpec{Slug: "plain", Name: "Plain", Owner: "u"})
	if w := appAs(t, T, plain, "u", httptest.NewRequest("POST", "/apps/plain/_settings/mcp/mint", strings.NewReader(`{}`))); w.Code != 400 {
		t.Errorf("a key for an app that offers nothing: %d", w.Code)
	}
}

// No key is made while MCP is closed to the owner, and the Settings page has
// the section.
func TestAnAppsMCPSectionIsOnItsSettings(t *testing.T) {
	T, spec := mcpApp(t)
	w := appAs(t, T, spec, "u", httptest.NewRequest("GET", "/apps/books/_settings", nil))
	if !strings.Contains(w.Body.String(), "Use from an MCP client") || !strings.Contains(w.Body.String(), appMCPKeyAction) {
		t.Errorf("the Settings page has no MCP section: %d %s", w.Code, w.Body.String())
	}
	SetFeatureAllowedUsers(RootDB, "mcp", []string{"someone-else"})
	if w := appAs(t, T, spec, "u", httptest.NewRequest("POST", "/apps/books/_settings/mcp/mint", strings.NewReader(`{}`))); w.Code != 403 {
		t.Errorf("a key made while MCP is closed to the owner: %d", w.Code)
	}
}

// sharedMCPApp is mcpApp shared with pal, with accounts: u granted apps, pal
// granted this app alone, MCP open to both.
func sharedMCPApp(t *testing.T) (*CustomApps, AppSpec) {
	t.Helper()
	T, spec := mcpApp(t)
	prev := AuthDB
	db := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(db, "u", "pw-u-123", false)
	AuthSetUser(db, "pal", "pw-p-123", false)
	AuthSetUserApps(db, "u", []string{"/apps"})
	AuthSetUserApps(db, "pal", []string{"/apps/books"})
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })
	SetFeatureAllowedUsers(RootDB, "mcp", []string{"u", "pal"})
	spec.SharedWith = []string{"pal"}
	spec = SaveAppSpec(spec)
	SetSharedOwner(T.DB, sharedAppsIndex, spec.Slug, spec.Owner, true)
	return T, spec
}

func offerMCP(t *testing.T, T *CustomApps, spec AppSpec, user string, on bool) int {
	t.Helper()
	body := `{"offer_to_shared":false}`
	if on {
		body = `{"offer_to_shared":true}`
	}
	return appAs(t, T, spec, user, httptest.NewRequest("POST", "/apps/books/_settings/mcp/offer", strings.NewReader(body))).Code
}

// An app shared with someone is theirs over MCP only once its owner offers it
// to the people it is shared with: then it is an offering app of theirs,
// pinned to its owner, listed for their keys, never among their agents'
// tools. Only the owner offers it, and it stops with the offer, with the
// share, and with their access to it; an app of their own at the slug comes
// first.
func TestASharedAppIsOfferedOverMCPWhenItsOwnerSaysSo(t *testing.T) {
	T, spec := sharedMCPApp(t)
	if got := T.offeringApps("pal"); len(got) != 0 {
		t.Fatalf("offered before its owner says so: %+v", got)
	}
	if code := offerMCP(t, T, spec, "pal", true); code != 403 {
		t.Errorf("someone else offered the app: %d", code)
	}
	if code := offerMCP(t, T, spec, "u", true); code != 200 {
		t.Fatalf("the owner's offer: %d", code)
	}
	if w := appAs(t, T, spec, "u", httptest.NewRequest("GET", "/apps/books/_settings/mcp/offer", nil)); !strings.Contains(w.Body.String(), `"offer_to_shared":true`) {
		t.Errorf("the offer reads back as: %s", w.Body.String())
	}
	if w := appAs(t, T, spec, "pal", httptest.NewRequest("GET", "/apps/books/_settings/mcp/offer", nil)); w.Code != 403 {
		t.Errorf("someone else reads the offer: %d", w.Code)
	}
	got := T.offeringApps("pal")
	if len(got) != 1 || !got[0].Shared || got[0].Owner != "u" || got[0].Target() != AppToolsTarget+"u/books" {
		t.Fatalf("pal's offering apps: %+v", got)
	}
	if tg := MCPTargetsFor(T, "pal"); len(tg) != 1 || tg[0].Value != AppToolsTarget+"u/books" || !strings.Contains(tg[0].Label, "shared by u") {
		t.Errorf("pal's key targets: %+v", tg)
	}
	if tools := T.exposedTools(nil, "pal"); len(tools) != 0 {
		t.Errorf("a shared app's tools reached pal's agents: %d", len(tools))
	}
	if own := T.offeringApps("u"); len(own) != 1 || own[0].Shared || own[0].Target() != AppToolsTarget+"books" {
		t.Errorf("the owner's own offering: %+v", own)
	}

	// Access to the app: pal granted another app only, unless an
	// administrator.
	AuthSetUserApps(AuthDB(), "pal", []string{"/apps/other"})
	if got := T.offeringApps("pal"); len(got) != 0 {
		t.Errorf("offered without access to the app: %+v", got)
	}
	AuthSetUser(AuthDB(), "pal", "pw-p-123", true)
	if got := T.offeringApps("pal"); len(got) != 1 {
		t.Errorf("an administrator it is shared with: %+v", got)
	}
	AuthSetUser(AuthDB(), "pal", "pw-p-123", false)
	AuthSetUserApps(AuthDB(), "pal", []string{"/apps/books"})

	// An app of pal's own at the same slug comes first.
	SaveAppSpec(AppSpec{Slug: "books", Name: "Pal's Books", Owner: "pal", Actions: []AppAction{{Name: "add", Script: "x", Expose: true}}})
	AuthSetUserApps(AuthDB(), "pal", []string{"/apps"})
	if got := T.offeringApps("pal"); len(got) != 1 || got[0].Shared || got[0].Owner != "pal" {
		t.Errorf("pal's own app does not come first: %+v", got)
	}
	DeleteAppSpec("pal", "books")
	AuthSetUserApps(AuthDB(), "pal", []string{"/apps/books"})

	if code := offerMCP(t, T, spec, "u", false); code != 200 {
		t.Fatalf("the owner's offer off: %d", code)
	}
	if got := T.offeringApps("pal"); len(got) != 0 {
		t.Errorf("still offered with the offer off: %+v", got)
	}
	offerMCP(t, T, spec, "u", true)
	unshared, _ := loadSpec("u", "books")
	unshared.SharedWith = nil
	SaveAppSpec(unshared)
	if got := T.offeringApps("pal"); len(got) != 0 {
		t.Errorf("still offered once unshared: %+v", got)
	}
}

// Someone it is offered to makes a key of their own, pinned to the owner's
// app, and sees and revokes it on their Settings; the owner sees who has
// connected and when, and offers it, where someone else sees neither.
func TestSomeoneItIsOfferedToMakesTheirOwnKey(t *testing.T) {
	T, spec := sharedMCPApp(t)
	offerMCP(t, T, spec, "u", true)

	r := httptest.NewRequest("GET", "https://box.example/apps/books/_settings", nil)
	sec := T.mcpSection(r, spec, "pal", "u", "/apps/books")
	if sec == nil || !strings.Contains(sec.Subtitle, "u offers this app") {
		t.Fatalf("pal's section: %+v", sec)
	}
	if b, _ := json.Marshal(sec); strings.Contains(string(b), "_settings/mcp/offer") || strings.Contains(string(b), "_settings/mcp/people") {
		t.Errorf("pal is offered the owner's controls: %s", b)
	}
	if b, _ := json.Marshal(T.mcpSection(r, spec, "u", "u", "/apps/books")); !strings.Contains(string(b), "_settings/mcp/offer") || !strings.Contains(string(b), "_settings/mcp/people") {
		t.Errorf("the owner's section lacks the offer or who has connected: %s", b)
	}

	w := appAs(t, T, spec, "pal", httptest.NewRequest("POST", "/apps/books/_settings/mcp/mint", strings.NewReader(`{"client":"laptop"}`)))
	var made struct{ Key, Name string }
	json.Unmarshal(w.Body.Bytes(), &made)
	if w.Code != 200 || made.Key == "" {
		t.Fatalf("pal's key: %d %s", w.Code, w.Body.String())
	}
	var tok AccountToken
	for _, k := range ListAccountTokens("pal") {
		if k.Name == made.Name {
			tok = k
		}
	}
	if tok.Scope == nil || strings.Join(tok.Scope.Targets, ",") != AppToolsTarget+"u/books" {
		t.Fatalf("pal's key is pinned to the owner's app: %+v", tok.Scope)
	}
	if w := appAs(t, T, spec, "pal", httptest.NewRequest("GET", "/apps/books/_settings/mcp/keys", nil)); !strings.Contains(w.Body.String(), made.Name) {
		t.Errorf("pal's keys: %s", w.Body.String())
	}
	if w := appAs(t, T, spec, "u", httptest.NewRequest("GET", "/apps/books/_settings/mcp/keys", nil)); strings.Contains(w.Body.String(), made.Name) {
		t.Errorf("the owner's own keys list pal's: %s", w.Body.String())
	}
	MintAccountTokenScoped("pal", "pal-general", &TokenScope{Features: []string{"mcp"}})
	MintAccountTokenScoped("u", "u-own", &TokenScope{Features: []string{"mcp"}, Targets: []string{AppToolsTarget + "books"}})
	w = appAs(t, T, spec, "u", httptest.NewRequest("GET", "/apps/books/_settings/mcp/people", nil))
	if !strings.Contains(w.Body.String(), `"person":"pal"`) || !strings.Contains(w.Body.String(), made.Name) || strings.Contains(w.Body.String(), made.Key) {
		t.Errorf("who has connected: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "pal-general") || strings.Contains(w.Body.String(), "u-own") {
		t.Errorf("who has connected lists keys that do not name the shared app: %s", w.Body.String())
	}
	if w := appAs(t, T, spec, "pal", httptest.NewRequest("GET", "/apps/books/_settings/mcp/people", nil)); w.Code != 403 {
		t.Errorf("pal sees who has connected: %d", w.Code)
	}
	if w := appAs(t, T, spec, "pal", httptest.NewRequest("DELETE", "/apps/books/_settings/mcp/keys/"+tok.ID, nil)); w.Code != 200 {
		t.Errorf("pal revokes their key: %d", w.Code)
	}

	offerMCP(t, T, spec, "u", false)
	if T.mcpSection(r, spec, "pal", "u", "/apps/books") != nil {
		t.Error("pal still has the section with the offer off")
	}
	if w := appAs(t, T, spec, "pal", httptest.NewRequest("POST", "/apps/books/_settings/mcp/mint", strings.NewReader(`{}`))); w.Code != 403 {
		t.Errorf("pal made a key with the offer off: %d", w.Code)
	}
}

// MCPTargetsFor is MCPTargets on T rather than the running host.
func MCPTargetsFor(T *CustomApps, user string) []ExternalTarget {
	prev := hostApp
	hostApp = T
	defer func() { hostApp = prev }()
	return MCPTargets(user)
}

// A key asks in the client only when its owner says so, at minting or on its
// row; a yes it keeps is for that key and action alone, and the row can make
// it ask again. Off, or revoked, forgets its yeses. Nobody turns on asking
// for a key that is not theirs or does not name the app.
func TestAKeyAsksInTheClientWhenItsOwnerSaysSo(t *testing.T) {
	T, spec := mcpApp(t)
	mint := func(body string) AccountToken {
		t.Helper()
		w := appAs(t, T, spec, "u", httptest.NewRequest("POST", "/apps/books/_settings/mcp/mint", strings.NewReader(body)))
		var made struct{ Name string }
		json.Unmarshal(w.Body.Bytes(), &made)
		for _, k := range ListAccountTokens("u") {
			if k.Name == made.Name {
				return k
			}
		}
		t.Fatalf("no key made: %d %s", w.Code, w.Body.String())
		return AccountToken{}
	}
	plain := mint(`{"client":"script"}`)
	asking := mint(`{"client":"Claude Code","asks":true}`)
	if MCPKeyAsks(plain.ID) || !MCPKeyAsks(asking.ID) {
		t.Fatalf("asking at minting: plain %v, asking %v", MCPKeyAsks(plain.ID), MCPKeyAsks(asking.ID))
	}

	scope := "app-action:u/books/wipe"
	RememberMCPYes(plain.ID, scope)
	if MCPRunsUnasked(plain.ID, scope) {
		t.Error("a key that does not ask kept a yes")
	}
	RememberMCPYes(asking.ID, scope)
	RememberMCPYes(asking.ID, scope)
	if !MCPRunsUnasked(asking.ID, scope) || MCPRunsUnasked(asking.ID, "app-action:u/books/add") || MCPRunsUnasked(plain.ID, scope) {
		t.Error("a yes is for its key and its action alone")
	}
	if got := loadMCPKeyAsk(asking.ID).Unasked; len(got) != 1 {
		t.Errorf("a yes kept twice is kept once: %v", got)
	}
	w := appAs(t, T, spec, "u", httptest.NewRequest("GET", "/apps/books/_settings/mcp/keys", nil))
	var rows []map[string]any
	json.Unmarshal(w.Body.Bytes(), &rows)
	for _, row := range rows {
		want := row["id"] == asking.ID
		if row["asks"] != want || (row["unasked"] == "wipe") != want {
			t.Errorf("the key's row says how it asks: %v", row)
		}
	}

	path := "/apps/books/_settings/mcp/keys/" + asking.ID
	if w := appAs(t, T, spec, "u", httptest.NewRequest("POST", path+"/forget", strings.NewReader(`{}`))); w.Code != 200 || MCPRunsUnasked(asking.ID, scope) || !MCPKeyAsks(asking.ID) {
		t.Errorf("ask again: %d, unasked %v, asks %v", w.Code, MCPRunsUnasked(asking.ID, scope), MCPKeyAsks(asking.ID))
	}
	RememberMCPYes(asking.ID, scope)
	if w := appAs(t, T, spec, "u", httptest.NewRequest("POST", path+"/ask", strings.NewReader(`{"asks":false}`))); w.Code != 200 || MCPKeyAsks(asking.ID) {
		t.Fatalf("turned off: %d %s", w.Code, w.Body.String())
	}
	appAs(t, T, spec, "u", httptest.NewRequest("POST", path+"/ask", strings.NewReader(`{"asks":true}`)))
	if !MCPKeyAsks(asking.ID) || MCPRunsUnasked(asking.ID, scope) {
		t.Error("turning asking off did not forget the key's yeses")
	}

	other := MintAccountTokenScoped("u", "pantry key", &TokenScope{Features: []string{"mcp"}, Targets: []string{AppToolsTarget + "pantry"}})
	if w := appAs(t, T, spec, "u", httptest.NewRequest("POST", "/apps/books/_settings/mcp/keys/"+other.ID+"/ask", strings.NewReader(`{"asks":true}`))); w.Code != 404 || MCPKeyAsks(other.ID) {
		t.Errorf("asking turned on for a key that names another app: %d", w.Code)
	}
	if w := appAs(t, T, spec, "someone", httptest.NewRequest("POST", "/apps/books/_settings/mcp/keys/"+plain.ID+"/ask", strings.NewReader(`{"asks":true}`))); w.Code == 200 || MCPKeyAsks(plain.ID) {
		t.Errorf("someone else turned on asking for the owner's key: %d", w.Code)
	}
	RememberMCPYes(asking.ID, scope)
	appAs(t, T, spec, "u", httptest.NewRequest("DELETE", path, nil))
	if MCPKeyAsks(asking.ID) || MCPRunsUnasked(asking.ID, scope) {
		t.Error("a revoked key's asking was kept")
	}
}

// A key for an app is a personal API key: a user profile that takes those
// away refuses it here too, rather than making one that every use refuses.
func TestAProfileThatTakesKeysAwayMakesNoKeyForAnApp(t *testing.T) {
	T, spec := mcpApp(t)
	userprofile.SetStore(RootDB)
	t.Cleanup(func() { userprofile.SetStore(nil) })
	if err := userprofile.Save(userprofile.Profile{ID: "p", Name: "Focused", Users: []string{"u"}, Removes: []string{userprofile.RemoveAPIKeys}}); err != nil {
		t.Fatal(err)
	}
	w := appAs(t, T, spec, "u", httptest.NewRequest("POST", "/apps/books/_settings/mcp/mint", strings.NewReader(`{"client":"laptop"}`)))
	if w.Code != 403 || !strings.Contains(w.Body.String(), "your profile does not include this") || len(ListAccountTokens("u")) != 0 {
		t.Errorf("a key made for someone whose profile takes keys away: %d %s", w.Code, w.Body.String())
	}
}
