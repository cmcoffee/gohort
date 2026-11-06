package account

import (
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A key's scope keeps only what the editor offers its owner: features the
// admin permits them and targets from their own grantable list. Anything else
// posted is dropped, not stored.
func TestAKeyScopeKeepsOnlyWhatTheEditorOffers(t *testing.T) {
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prevRoot })
	SetFeatureAllowedUsers(RootDB, "desktop", []string{"someone-else"})

	T := &Account{}
	T.DB = RootDB.(*DBase).Bucket("account")
	// The raw tiers are Model access, closed until granted.
	if got := T.grantableScope("alice", &TokenScope{Targets: []string{"worker", "lead"}}); len(got.Targets) != 0 {
		t.Errorf("tiers kept without Model access: %v", got.Targets)
	}
	SetFeatureAllowedUsers(RootDB, ModelAccessFeature, []string{"alice"})
	got := T.grantableScope("alice", &TokenScope{
		Features: []string{"desktop", "made-up"},
		Targets:  []string{"worker", "agent:bobs-agent", "lead"},
	})
	if len(got.Features) != 0 {
		t.Errorf("features the admin withheld or that do not exist were kept: %v", got.Features)
	}
	if len(got.Targets) != 2 || got.Targets[0] != "worker" || got.Targets[1] != "lead" {
		t.Errorf("targets: %v", got.Targets)
	}
}

// A key is saved as one kind: its door is set from the kind, and of what was
// posted only what that kind reaches is kept. A scope that reads as several
// kinds, or a kind the user has not been granted, is refused.
func TestAKeyIsSavedAsOneKind(t *testing.T) {
	prevRoot, prevFn := RootDB, ListExternalTargetsFn
	RootDB = &DBase{Store: kvlite.MemStore()}
	ListExternalTargetsFn = func(_ Database, user string) []ExternalTarget {
		return []ExternalTarget{
			{Value: "agent:a1", Group: "Agents"},
			{Value: "channel:c1", Group: "Channels"},
		}
	}
	t.Cleanup(func() { RootDB, ListExternalTargetsFn = prevRoot, prevFn })
	RegisterShareableFeature(ShareableFeature{Key: "openai", Label: "Model API"})
	RegisterShareableFeature(ShareableFeature{Key: "mcp", Label: "MCP"})
	for _, f := range []string{"openai", "mcp", "desktop", ModelAccessFeature} {
		SetFeatureAllowedUsers(RootDB, f, []string{"alice"})
	}
	T := &Account{}
	T.DB = RootDB.(*DBase).Bucket("account")
	tools := []string{"ask_agent"}
	everything := &TokenScope{
		Features: []string{"openai", "mcp", "desktop", "models"},
		Targets:  []string{"agent:a1", "channel:c1", "worker", "lead"},
		Tools:    &tools,
	}
	for _, c := range []struct {
		kind     string
		features string
		targets  string
		tools    bool
		outside  bool
	}{
		{KeyKindAgents, "openai", "agent:a1,channel:c1", false, false},
		{KeyKindMCP, "mcp", "agent:a1", true, false},
		{KeyKindModels, "models,openai", "worker,lead", false, false},
		{KeyKindDesktop, "desktop", "", false, false},
		{KeyKindOutside, "openai", "agent:a1", false, true},
	} {
		got, err := T.kindScope("alice", c.kind, everything)
		if err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		if f := strings.Join(got.Features, ","); f != c.features {
			t.Errorf("%s: features %q, want %q", c.kind, f, c.features)
		}
		if tg := strings.Join(got.Targets, ","); tg != c.targets {
			t.Errorf("%s: targets %q, want %q", c.kind, tg, c.targets)
		}
		if (got.Tools != nil) != c.tools || got.Outside != c.outside {
			t.Errorf("%s: tools %v outside %v", c.kind, got.Tools, got.Outside)
		}
		if k := got.Kind(); k != c.kind {
			t.Errorf("%s: the saved key reads as %q", c.kind, k)
		}
	}
	// No kind named: the one the scope reads as.
	if got, err := T.kindScope("alice", "", &TokenScope{Features: []string{"mcp"}, Targets: []string{"agent:a1"}}); err != nil || got.Kind() != KeyKindMCP {
		t.Errorf("an MCP scope with no kind named: %+v %v", got, err)
	}
	if _, err := T.kindScope("alice", "", everything); err == nil {
		t.Error("a scope of several kinds was saved")
	}
	if got, err := T.kindScope("alice", "", &TokenScope{}); err != nil || got.Kind() != "" {
		t.Errorf("an empty scope stays the key that opens nothing: %+v %v", got, err)
	}
	if _, err := T.kindScope("alice", "made-up", nil); err == nil {
		t.Error("a kind that does not exist was saved")
	}
	// Bob has been granted nothing.
	if _, err := T.kindScope("bob", KeyKindMCP, nil); err == nil {
		t.Error("a kind the user was not granted was saved")
	}
	if k := keyKinds("bob"); len(k) != 0 {
		t.Errorf("bob is offered kinds he was not granted: %v", k)
	}
	// The desktop app makes its own key when signed in to, so a new key is
	// never of that kind; one made before still saves as itself.
	if k := strings.Join(keyKinds("alice"), ","); k != "agents,mcp,models,outside" {
		t.Errorf("alice's kinds: %s", k)
	}
	c := &jsonapi.Call{User: "alice"}
	if _, err := T.handleTokenMint(c, &tokenMintBody{Name: "laptop", Kind: KeyKindDesktop}); err == nil {
		t.Error("a new desktop key was minted")
	}
	if _, err := T.handleTokenMint(c, &tokenMintBody{Name: "laptop", Scope: &TokenScope{Features: []string{"desktop"}}}); err == nil {
		t.Error("a new desktop key was minted from its scope alone")
	}
	if _, err := T.handleTokenMint(c, &tokenMintBody{Name: "claude", Kind: KeyKindMCP}); err != nil {
		t.Errorf("an MCP key: %v", err)
	}
}

// A kind no longer made stays editable on a key that already is one, and no
// other key can be rescoped into it: rescoping was the way round minting's
// refusal.
func TestAKeyCannotBeRescopedIntoTheDesktopKind(t *testing.T) {
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prevRoot })
	RegisterShareableFeature(ShareableFeature{Key: "mcp", Label: "MCP"})
	for _, f := range []string{"mcp", "desktop"} {
		SetFeatureAllowedUsers(RootDB, f, []string{"alice"})
	}
	T := &Account{}
	T.DB = RootDB.(*DBase).Bucket("account")
	mcp := MintAccountTokenScoped("alice", "claude", &TokenScope{Features: []string{"mcp"}})
	desk := MintAccountTokenScoped("alice", "laptop", &TokenScope{Features: []string{"desktop"}})
	rescope := func(id string) error {
		c := &jsonapi.Call{User: "alice", R: httptest.NewRequest("POST", "/api/tokens?action=scope&id="+id, nil)}
		_, err := T.handleTokenScope(c, struct {
			Kind  string     `json:"kind"`
			Scope TokenScope `json:"scope"`
		}{Kind: KeyKindDesktop})
		return err
	}
	if err := rescope(mcp.ID); err == nil {
		t.Error("an MCP key was rescoped into a desktop key")
	}
	if err := rescope(desk.ID); err != nil {
		t.Errorf("an existing desktop key could not be saved as itself: %v", err)
	}
}
