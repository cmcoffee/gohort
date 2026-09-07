package bridges

import (
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
	"strings"
	"testing"
)

// A CONVERSATION ALIAS handle is an alternate id for the chat (a folded-in
// duplicate reachable via another phone/email), not a person — so it must never
// appear in the participant roster, even when it's ALSO a real sender in the
// thread (the email case: an Apple ID email genuinely sends messages, so
// derive-on-read would otherwise harvest it as a bogus participant on reload).
func TestSyncMembersExcludesConversationAliases(t *testing.T) {
	const chatID = "acct;-;+15551234567"
	const email = "alice@icloud.com"

	t.Run("aliased sender is not harvested", func(t *testing.T) {
		T := &Bridges{AppCore{DB: OpenCache()}}
		// A message DID arrive from the email in this thread.
		T.storeMessage(StoredMessage{ID: "m1", ChatID: chatID, Role: "user", Handle: email, DisplayName: "Alice", Text: "hi"})
		// The user marks that email as a conversation alias.
		T.saveConvo(Convo{ChatID: chatID, AliasHandles: []string{email}})

		c := T.syncMembersFromHistory(chatID)
		for _, m := range c.Members {
			if strings.EqualFold(m.Handle, email) {
				t.Fatalf("conversation alias %q must not be harvested as a participant", email)
			}
		}
		// A genuine, non-alias sender is still harvested.
		T.storeMessage(StoredMessage{ID: "m2", ChatID: chatID, Role: "user", Handle: "+15559999999", DisplayName: "Bob", Text: "yo"})
		c = T.syncMembersFromHistory(chatID)
		found := false
		for _, m := range c.Members {
			if m.Handle == "+15559999999" {
				found = true
			}
		}
		if !found {
			t.Errorf("a non-alias sender should still be harvested as a participant")
		}
	})

	t.Run("member already harvested before aliasing is pruned", func(t *testing.T) {
		T := &Bridges{AppCore{DB: OpenCache()}}
		T.saveConvo(Convo{
			ChatID:       chatID,
			Members:      []ConvMember{{Handle: email, Name: "Alice"}},
			AliasHandles: []string{email},
		})
		c := T.syncMembersFromHistory(chatID)
		for _, m := range c.Members {
			if strings.EqualFold(m.Handle, email) {
				t.Fatalf("a member that is now a conversation alias should be pruned, still present: %q", m.Handle)
			}
		}
	})
}

// Bridge keys authenticate the iMessage hook and the desktop WS bridge, and
// they were resolved with ==, over a scan that returned on the first match.
// Both halves leak: the comparison tells you how much of a guess was right,
// and the early return tells you where in the table it landed. core/peer_key.go
// writes down why peer keys do neither; these are the same kind of credential.
func keyFixture(t *testing.T) *Bridges {
	t.Helper()
	T := &Bridges{}
	T.DB = &DBase{Store: kvlite.MemStore()}
	T.saveBridgeKey(BridgeKey{ID: "k1", Key: "secret-craig", Owner: "craig", Service: "imessage", Enabled: true})
	T.saveBridgeKey(BridgeKey{ID: "k2", Key: "secret-dana", Owner: "dana", Service: "imessage", Enabled: true})
	// An ownerless record, which bridgeKeyOwner must never resolve.
	T.saveBridgeKey(BridgeKey{ID: "k3", Key: "secret-orphan", Service: "imessage"})
	return T
}

func TestBridgeKeyResolvesExactly(t *testing.T) {
	T := keyFixture(t)

	if owner, ok := T.bridgeKeyOwner("secret-craig"); !ok || owner != "craig" {
		t.Errorf("exact key did not resolve: %q ok=%v", owner, ok)
	}
	if owner, ok := T.bridgeKeyOwner("secret-dana"); !ok || owner != "dana" {
		t.Errorf("second key did not resolve: %q ok=%v", owner, ok)
	}
	// A prefix, an extension, a case change and a blank must all miss —
	// checking that the constant-time rewrite did not turn into a loose match.
	for _, bad := range []string{"", " ", "secret-crai", "secret-craigg", "SECRET-CRAIG", "nope"} {
		if owner, ok := T.bridgeKeyOwner(bad); ok {
			t.Errorf("key %q was accepted as %q", bad, owner)
		}
	}
}

func TestBridgeKeyWithNoOwnerNeverResolves(t *testing.T) {
	T := keyFixture(t)
	if owner, ok := T.bridgeKeyOwner("secret-orphan"); ok {
		t.Errorf("an ownerless key resolved to %q", owner)
	}
}

func TestValidateBridgeKeyMatchesAndStampsLastSeen(t *testing.T) {
	T := keyFixture(t)

	k, ok := T.validateBridgeKey("secret-dana")
	if !ok || k.ID != "k2" {
		t.Fatalf("validate did not find the right record: %+v ok=%v", k, ok)
	}
	if k.LastSeen == "" {
		t.Error("a successful match should stamp LastSeen")
	}
	// The stamp must have been persisted, not just returned.
	var stored BridgeKey
	if !T.DB.Get(bridgeKeysTable, "k2", &stored) || stored.LastSeen == "" {
		t.Error("LastSeen was not written back to the store")
	}
	for _, bad := range []string{"", "secret-dan", "secret-danaa"} {
		if _, ok := T.validateBridgeKey(bad); ok {
			t.Errorf("key %q was accepted", bad)
		}
	}
}
