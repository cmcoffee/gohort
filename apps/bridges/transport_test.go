package bridges

import (
	"testing"
	"time"

	. "github.com/cmcoffee/oddjob/core"
)

// A bridge key stands in for its owner beyond the bridge hook only when it is
// the desktop's iMessage bridge. A key minted for another connector opens that
// connector and nothing else.
func TestOnlyTheDesktopsBridgeKeyIsAnAccountKey(t *testing.T) {
	T := keyFixture(t)
	T.saveBridgeKey(BridgeKey{ID: "k4", Key: "secret-poller", Owner: "craig", Service: "telegram", Enabled: true})
	if owner, ok := T.bridgeKeyOwner("secret-poller"); ok {
		t.Errorf("a telegram connector's key opened the account as %q", owner)
	}
	if _, ok := T.validateBridgeKey("secret-poller"); !ok {
		t.Error("the telegram key no longer opens its own hook")
	}
	if owner, ok := T.bridgeKeyOwner("secret-craig"); !ok || owner != "craig" {
		t.Error("the desktop's imessage key stopped standing in for its owner")
	}
}

// Any account's desktop negotiates a key; it becomes an iMessage bridge only
// for an account the deployment lets use Bridges.
func TestADesktopKeyBridgesOnlyForABridgesUser(t *testing.T) {
	f := newOwnershipFixture(t)
	f.adb.Set(AuthTable, "user:carol", AuthUser{Username: "carol", Apps: []string{"/bridges"}})
	bobKey, _ := MintDesktopKey("bob")
	carolKey, _ := MintDesktopKey("carol")
	if _, ok := f.T.validateBridgeKey(bobKey); ok {
		t.Error("bob's desktop, with no Bridges access, became an iMessage bridge")
	}
	if k, ok := f.T.validateBridgeKey(carolKey); !ok || k.Owner != "carol" || k.Service != "imessage" {
		t.Errorf("carol's desktop: %+v ok=%v", k, ok)
	}
}

// A group's message is matched by the group, never by its sender's handle,
// which is the address of the sender's one-to-one channel.
func TestAGroupMessageIsNotItsSendersOneToOne(t *testing.T) {
	f := newOwnershipFixture(t)
	for _, id := range f.T.inboundIdentities("alice", "imessage", "any;+;chat123", "+15550100") {
		if id == "+15550100" {
			t.Error("a group message carried its sender's handle as a candidate")
		}
	}
	found := false
	for _, id := range f.T.inboundIdentities("alice", "imessage", "any;-;+15550100", "+15550100") {
		found = found || id == "+15550100"
	}
	if !found {
		t.Error("a one-to-one message lost its sender's handle")
	}
}

// Past a sender's or a connector's wake budget a message wakes nothing; a
// refused message spends neither budget, and the owner is never limited.
func TestTheWakeBudgetStopsAFlood(t *testing.T) {
	b := &wakeBudget{wakes: map[string][]time.Time{}}
	now := time.Now()
	for i := 0; i < 3; i++ {
		if _, ok := b.take(now, []string{"s", "c"}, []int{3, 5}); !ok {
			t.Fatalf("wake %d refused under budget", i+1)
		}
	}
	if who, ok := b.take(now, []string{"s", "c"}, []int{3, 5}); ok || who != "s" {
		t.Errorf("the fourth wake from one sender: who=%q ok=%v", who, ok)
	}
	if len(b.wakes["c"]) != 3 {
		t.Errorf("a refused wake spent the connector's budget: %d", len(b.wakes["c"]))
	}
	for i := 0; i < 2; i++ {
		b.take(now, []string{"s2", "c"}, []int{3, 5})
	}
	if who, ok := b.take(now, []string{"s3", "c"}, []int{3, 5}); ok || who != "c" {
		t.Errorf("the connector's sixth wake: who=%q ok=%v", who, ok)
	}
	if _, ok := b.take(now.Add(wakeWindow), []string{"s", "c"}, []int{3, 5}); !ok {
		t.Error("the budget did not roll over with the window")
	}
	T := &Bridges{}
	for i := 0; i < senderWakesFor()+5; i++ {
		if ok, _ := T.mayWake(BridgeKey{ID: "k"}, "alice", "sms", "chat", "", true); !ok {
			t.Fatal("the owner was limited")
		}
	}
}

// A chat id is written into a send URL as one component, never as structure.
func TestAChatIDCannotRewriteTheSendURL(t *testing.T) {
	for in, want := range map[string]string{
		"-100123":       "-100123",
		"../admin?x=1&": "..%2Fadmin%3Fx%3D1%26",
		"a b@c.example": "a%20b%40c.example",
	} {
		if got := urlComponent(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}
