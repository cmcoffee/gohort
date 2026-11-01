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
	ApproveConnector(RootDB, "finder")
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
