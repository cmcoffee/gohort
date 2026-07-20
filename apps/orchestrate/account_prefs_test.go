package orchestrate

import (
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestResolveDefaultAgent(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	prevAuthDB := AuthDB
	AuthDB = func() Database { return adb }
	defer func() { AuthDB = prevAuthDB }()

	opts := []ui.SelectOption{
		{Value: ""}, {Value: "seed-chat"}, {Value: "ag-1"},
	}
	cases := []struct {
		name   string
		pref   string
		cookie string
		want   string
	}{
		{"unset pref without cookie falls back", "", "", "seed-chat"},
		{"unset pref reads last-accessed cookie", "", "ag-1", "ag-1"},
		{"unset pref with unknown cookie falls back", "", "ag-gone", "seed-chat"},
		{"specific visible agent wins", "ag-1", "", "ag-1"},
		{"specific agent beats the cookie", "seed-chat", "ag-1", "seed-chat"},
		{"specific unknown agent falls back", "ag-gone", "", "seed-chat"},
		{"legacy last sentinel reads the cookie", lastAccessedSentinel, "ag-1", "ag-1"},
		{"legacy last sentinel without cookie falls back", lastAccessedSentinel, "", "seed-chat"},
	}
	for _, c := range cases {
		adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice", DefaultAgent: c.pref})
		r := httptest.NewRequest("GET", "/orchestrate/", nil)
		if c.cookie != "" {
			r.AddCookie(&http.Cookie{Name: lastAgentCookie, Value: c.cookie})
		}
		if got := resolveDefaultAgent(r, "alice", "seed-chat", opts); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}

	// Unknown user (no auth record) must behave like an unset pref.
	r := httptest.NewRequest("GET", "/orchestrate/", nil)
	if got := resolveDefaultAgent(r, "nobody", "seed-chat", opts); got != "seed-chat" {
		t.Errorf("unknown user: got %q, want seed-chat", got)
	}
}

func TestAuthDefaultAgentRoundtrip(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	if got := AuthGetDefaultAgent(adb, "bob"); got != "" {
		t.Errorf("fresh user: got %q, want empty", got)
	}
	AuthSetDefaultAgent(adb, "bob", "  ag-9  ")
	if got := AuthGetDefaultAgent(adb, "bob"); got != "ag-9" {
		t.Errorf("after set: got %q, want ag-9 (trimmed)", got)
	}
	AuthSetDefaultAgent(adb, "bob", "")
	if got := AuthGetDefaultAgent(adb, "bob"); got != "" {
		t.Errorf("after clear: got %q, want empty", got)
	}
	// Unknown user: set is a no-op, get returns empty.
	AuthSetDefaultAgent(adb, "ghost", "ag-1")
	if got := AuthGetDefaultAgent(adb, "ghost"); got != "" {
		t.Errorf("unknown user: got %q, want empty", got)
	}
}
