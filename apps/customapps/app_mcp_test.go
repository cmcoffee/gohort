package customapps

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
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

// Settings has the app's MCP address with what it offers and what it leaves
// out, for its owner alone; an app that offers nothing says so.
func TestSettingsSaysWhereAnMCPClientFindsTheApp(t *testing.T) {
	T, spec := mcpApp(t)
	r := httptest.NewRequest("GET", "https://box.example/apps/books/_settings", nil)
	sec := T.mcpSection(r, spec, "u", "u", "/apps/books")
	if sec == nil {
		t.Fatal("no section for the owner")
	}
	body, _ := json.Marshal(sec)
	for _, want := range []string{"https://box.example/mcp/app/books/", "books_add", "Left out", "books_wipe", "Make a key for this app", appMCPKeyAction, "_settings/mcp/keys"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the section does not say %q: %s", want, body)
		}
	}
	card := sec.Body.(ui.Stack).Children[0].(ui.Card).HTML
	if !strings.Contains(card, "<b>Offers</b> books_add<") || !strings.Contains(card, "<b>Left out</b> books_wipe:") {
		t.Errorf("the card says what is offered and what is left out: %s", card)
	}
	if T.mcpSection(r, spec, "someone", "u", "/apps/books") != nil {
		t.Error("someone other than the owner sees the section")
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
	for _, path := range []string{"/apps/books/_settings/mcp/keys", "/apps/books/_settings/mcp/mint"} {
		method := "GET"
		if strings.HasSuffix(path, "mint") {
			method = "POST"
		}
		w = appAs(t, T, spec, "someone", httptest.NewRequest(method, path, strings.NewReader(`{}`)))
		if w.Code != 403 {
			t.Errorf("someone other than the owner: %s %s answered %d", method, path, w.Code)
		}
	}
	if w := appAs(t, T, spec, "someone", httptest.NewRequest("DELETE", "/apps/books/_settings/mcp/keys/"+tok.ID, nil)); w.Code != 403 {
		t.Errorf("someone other than the owner revoked a key: %d", w.Code)
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
