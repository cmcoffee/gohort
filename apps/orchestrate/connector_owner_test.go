package orchestrate

// An approved connector is every user's tool, so the connector tool may only
// change or remove one on behalf of its drafter or an admin. Before this gate
// any user's Builder could repoint a live connector at a host of its choosing.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

type stubConnectorKind struct{ torn int }

func (s *stubConnectorKind) Validate(Connector) error    { return nil }
func (s *stubConnectorKind) Materialize(Connector) error { return nil }
func (s *stubConnectorKind) Teardown(Connector) error    { s.torn++; return nil }
func (s *stubConnectorKind) Summary(Connector) string    { return "stub" }

func connectorOwnerFixture(t *testing.T) {
	t.Helper()
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return adb }
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { AuthDB = prevAuth; RootDB = prevRoot })
	RegisterConnectorKind("test_stub_owner", &stubConnectorKind{})
	for _, c := range []Connector{
		{Name: "alices", Kind: "test_stub_owner", Owner: "alice"},
		{Name: "unowned", Kind: "test_stub_owner"},
	} {
		if err := SaveConnector(RootDB, c); err != nil {
			t.Fatalf("seed %s: %v", c.Name, err)
		}
		if err := ApproveConnector(RootDB, c.Name); err != nil {
			t.Fatalf("approve %s: %v", c.Name, err)
		}
	}
}

func TestOnlyTheDrafterOrAnAdminManagesAConnector(t *testing.T) {
	connectorOwnerFixture(t)
	alices, _ := GetConnector(RootDB, "alices")
	unowned, _ := GetConnector(RootDB, "unowned")
	cases := []struct {
		user string
		c    Connector
		want bool
	}{
		{"alice", alices, true},
		{"bob", alices, false},
		{"root", alices, true},
		{"alice", unowned, false}, // no recorded drafter = admin-only
		{"root", unowned, true},
		{"", alices, false},
	}
	for _, tc := range cases {
		if got := mayManageConnector(&ToolSession{Username: tc.user}, tc.c); got != tc.want {
			t.Errorf("user %q on %q: got %v want %v", tc.user, tc.c.Name, got, tc.want)
		}
	}
}

func TestAnotherUserCannotDeleteOrUpdateAConnector(t *testing.T) {
	connectorOwnerFixture(t)
	bob := &ToolSession{Username: "bob"}
	if _, err := connectorDelete(map[string]any{"name": "alices"}, bob); err == nil || !strings.Contains(err.Error(), "another user") {
		t.Fatalf("bob deleting alice's connector should be refused, got %v", err)
	}
	if _, ok := GetConnector(RootDB, "alices"); !ok {
		t.Fatal("the connector was deleted anyway")
	}
	// The ownership check runs before any kind-specific patching.
	if _, err := connectorUpdate(map[string]any{"name": "alices", "url": "https://elsewhere.example"}, bob); err == nil || !strings.Contains(err.Error(), "another user") {
		t.Fatalf("bob updating alice's connector should be refused, got %v", err)
	}
	if _, err := connectorDelete(map[string]any{"name": "alices"}, &ToolSession{Username: "alice"}); err != nil {
		t.Fatalf("alice deleting her own connector: %v", err)
	}
}
