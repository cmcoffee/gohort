package service

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// keyedSearch is a search connection that takes an address, a size and a
// key, as a template's does.
func keyedSearch() map[string]any {
	return map[string]any{
		"config": []any{
			map[string]any{"name": "base_url", "label": "Address", "type": "url", "required": true},
			map[string]any{"name": "size", "label": "Results", "type": "number"},
			map[string]any{"name": "api_key", "label": "API key", "type": "secret", "required": true, "send": "bearer"},
		},
		"provider": map[string]any{"transport": "http", "base_url": "{config.base_url}", "auth": map[string]any{"secret": "api_key"},
			"operations": map[string]any{"find": map[string]any{"url": "/find?q={query}"}}},
		"provides": []any{map[string]any{"capability": "web.search", "steps": []any{map[string]any{"call": "find"}},
			"output": map[string]any{"results": map[string]any{"path": "hits", "fields": map[string]any{"title": "name", "url": "href"}}}}},
	}
}

// What is wrong with a saved connection is named, and only what would go
// wrong: one nothing uses has no key and that is fine; in use, a missing
// key, a key given for an old address, a bad value, an unreadable key and
// a spec that no longer reads each are. Put right, nothing is named.
func TestASavedConnectionIsChecked(t *testing.T) {
	world(t)
	raw, _ := json.Marshal(keyedSearch())
	if err := SaveConnector(RootDB, Connector{Name: "finder", Kind: Kind, Spec: raw}); err != nil {
		t.Fatal(err)
	}
	c, _ := GetConnector(RootDB, "finder")
	check := func() string { c, _ = GetConnector(RootDB, "finder"); return strings.Join(SelfCheck(RootDB, c), " | ") }
	if got := check(); got != "" {
		t.Errorf("unused and unset: %s", got)
	}
	RootDB.Set(SearchTable, "provider", "finder")
	if got := check(); !strings.Contains(got, "Address") {
		t.Errorf("in use with no address: %s", got)
	}
	SetConnectionConfig(RootDB, c, map[string]string{"base_url": "https://find.example", "size": "ten"})
	if got := check(); !strings.Contains(got, `its Results setting is wrong: "ten" is not a number`) || !strings.Contains(got, "in use with no API key") {
		t.Errorf("a bad value and no key: %s", got)
	}
	SetConnectionConfig(RootDB, c, map[string]string{"base_url": "https://find.example", "size": "10"})
	SetConnectionSecret(RootDB, c, "api_key", "k1", []string{"https://find.example"})
	if got := check(); got != "" {
		t.Errorf("put right: %s", got)
	}
	SetConnectionConfig(RootDB, c, map[string]string{"base_url": "https://moved.example"})
	if got := check(); !strings.Contains(got, "was given for https://find.example, and its calls now go to https://moved.example") {
		t.Errorf("an address moved from under its key: %s", got)
	}
	SetConnectionConfig(RootDB, c, map[string]string{"base_url": "https://find.example"})
	RootDB.Sub("connection:"+c.ID).Set("secret", "api_key", "not a secret record")
	if got := check(); !strings.Contains(got, "its API key is kept but cannot be read") || strings.Contains(got, "no API key") {
		t.Errorf("an unreadable key: %s", got)
	}
	c.Spec = json.RawMessage(`{"provider":`)
	RootDB.Set("connectors", "finder", c)
	if got := check(); !strings.Contains(got, "its spec no longer reads") {
		t.Errorf("a broken spec: %s", got)
	}
}

// A connection of another kind is held to its own type's check as things
// stand now: a credential it names gone, or switched off while it is in
// use, and a type this build does not know, each are named.
func TestAnotherKindOfConnectionIsChecked(t *testing.T) {
	world(t)
	if err := Secure().Save(SecureCredential{Name: "mcp_key", Type: SecureCredBearer, BaseURL: "https://mcp.example"}, "tok"); err != nil {
		t.Fatal(err)
	}
	spec, _ := json.Marshal(map[string]any{"url": "https://mcp.example/mcp", "auth_mode": "secure_api", "secure_cred": "mcp_key"})
	if err := SaveConnector(RootDB, Connector{Name: "tracker", Kind: RemoteMCPConnectorKind, Spec: spec}); err != nil {
		t.Fatal(err)
	}
	c, _ := GetConnector(RootDB, "tracker")
	// In use, without bringing it up: that would reach for the server.
	c.Approved = true
	RootDB.Set("connectors", "tracker", c)
	check := func() string {
		c, _ = GetConnector(RootDB, "tracker")
		return strings.Join(SelfCheck(RootDB, c), " | ")
	}
	if got := check(); got != "" {
		t.Errorf("as saved: %s", got)
	}
	Secure().SetDisabled("mcp_key", true)
	if got := check(); !strings.Contains(got, "its credential mcp_key is switched off") {
		t.Errorf("its credential switched off: %s", got)
	}
	c.Approved = false
	RootDB.Set("connectors", "tracker", c)
	if got := check(); got != "" {
		t.Errorf("switched off, and nothing uses it: %s", got)
	}
	Secure().Delete("mcp_key")
	if got := check(); !strings.Contains(got, `it no longer passes its own check: no credential named "mcp_key"`) {
		t.Errorf("its credential gone: %s", got)
	}
	c.Kind = "retired_kind"
	RootDB.Set("connectors", "tracker", c)
	if got := check(); !strings.Contains(got, `its type "retired_kind" is not one this build knows`) {
		t.Errorf("an unknown type: %s", got)
	}
}

// A connection saved under a name gohort keeps for something built in, from
// before names were reserved, says so: no page can choose it.
func TestAConnectionUnderAReservedNameSaysSo(t *testing.T) {
	world(t)
	searchConnector(t, "finder", "")
	c, _ := GetConnector(RootDB, "finder")
	c.Name = "duckduckgo"
	RootDB.Set("connectors", "duckduckgo", c)
	if got := strings.Join(SelfCheck(RootDB, c), " | "); !strings.Contains(got, "keeps for something built in") {
		t.Errorf("self-check: %s", got)
	}
	c, _ = GetConnector(RootDB, "finder")
	if got := SelfCheck(RootDB, c); len(got) != 0 {
		t.Errorf("an ordinary name: %v", got)
	}
}
