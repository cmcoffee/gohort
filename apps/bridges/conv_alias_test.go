package bridges

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
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
