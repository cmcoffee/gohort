package governance

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func withAuth(t *testing.T) {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(db, "boss", "pw", true)
	AuthSetUser(db, "dana", "pw", false)
	prev := AuthDB
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })
}

// Until the administrator sets one, each kind keeps what it did before:
// people added their own API credentials, and a server waited for approval.
// A value nothing recognises reads closed.
func TestAPolicyDefaultsToWhatWasAndReadsClosed(t *testing.T) {
	withAuth(t)
	if Of(Credentials) != Allowed || Of(MCPServers) != NeedsApproval {
		t.Errorf("defaults: credentials %q, mcp %q", Of(Credentials), Of(MCPServers))
	}
	AuthDB().Set(WebTable, key(Credentials), "alowed")
	if Of(Credentials) != AdminOnly {
		t.Errorf("a typo should read as admin only, got %q", Of(Credentials))
	}
	if err := Set(Credentials, "sometimes"); err == nil {
		t.Error("a value that is not a policy was stored")
	}
	if err := Set(MCPServers, string(Allowed)); err != nil || Of(MCPServers) != Allowed {
		t.Errorf("set: %v %q", err, Of(MCPServers))
	}
}

func TestPermitFollowsThePolicyAndNeverHoldsAnAdmin(t *testing.T) {
	withAuth(t)
	for _, c := range []struct {
		p        Policy
		hold, ok bool
	}{{Allowed, false, true}, {NeedsApproval, true, true}, {AdminOnly, false, false}} {
		if err := Set(MCPServers, string(c.p)); err != nil {
			t.Fatal(err)
		}
		hold, err := Permit(MCPServers, "dana")
		if hold != c.hold || (err == nil) != c.ok {
			t.Errorf("%s: hold=%v err=%v", c.p, hold, err)
		}
		if err != nil && !strings.Contains(err.Error(), "ask them") {
			t.Errorf("%s: the refusal should say what to do: %v", c.p, err)
		}
		if hold, err := Permit(MCPServers, "boss"); hold || err != nil {
			t.Errorf("%s: an administrator was held: hold=%v err=%v", c.p, hold, err)
		}
	}
}
