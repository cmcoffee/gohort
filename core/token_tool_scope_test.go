package core

// Per-key MCP tool scope. The interesting cases are all about the difference
// between "not narrowed" and "narrowed to nothing" — a distinction that has to
// survive the wire, or existing keys lose every tool the day the field ships.

import (
	"encoding/json"
	"testing"
)

func TestToolScopeDistinguishesUnsetFromEmpty(t *testing.T) {
	// A key written before tool scoping: scope exists, Tools does not.
	legacy := &AccountToken{Scope: &TokenScope{Features: []string{"mcp"}}}
	if !legacy.AllowsTool("ask_agent") {
		t.Error("a scoped key with no tools list must NOT be narrowed — it predates the field")
	}
	// A key narrowed to nothing: explicit, and means nothing.
	none := &AccountToken{Scope: &TokenScope{Tools: &[]string{}}}
	if none.AllowsTool("ask_agent") {
		t.Error("an explicitly empty tools list must deny; otherwise unticking everything silently means 'all'")
	}
	// Narrowed to one.
	one := &AccountToken{Scope: &TokenScope{Tools: &[]string{"ask_agent"}}}
	if !one.AllowsTool("ask_agent") || one.AllowsTool("list_agents") {
		t.Error("a narrowed key must allow exactly what it lists")
	}
	// The pre-scoping grandfather still wins over everything.
	if !(&AccountToken{}).AllowsTool("anything") {
		t.Error("a nil scope is legacy-unrestricted")
	}
	if (*AccountToken)(nil).AllowsTool("ask_agent") {
		t.Error("a nil token allows nothing")
	}
}

// The two states must be distinguishable after a round trip, or the whole
// scheme collapses into "empty means all" on the next save.
func TestToolScopeSurvivesJSON(t *testing.T) {
	empty := &[]string{}
	for _, c := range []struct {
		name  string
		scope TokenScope
		allow bool
	}{
		{"unset", TokenScope{}, true},
		{"empty", TokenScope{Tools: empty}, false},
		{"listed", TokenScope{Tools: &[]string{"ask_agent"}}, true},
	} {
		raw, err := json.Marshal(AccountToken{Scope: &c.scope})
		if err != nil {
			t.Fatal(err)
		}
		var back AccountToken
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		if got := back.AllowsTool("ask_agent"); got != c.allow {
			t.Errorf("%s: after round trip AllowsTool = %v, want %v (json: %s)", c.name, got, c.allow, raw)
		}
	}
}
