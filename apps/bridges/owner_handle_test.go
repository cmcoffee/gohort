package bridges

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// An empty sender handle is the owner only on iMessage, where the daemon
// clears it on the owner's own messages; on any other service it is a sender
// the transport could not name (an app's post, a bot, a webhook).
func TestAnEmptyHandleIsTheOwnerOnlyOnIMessage(t *testing.T) {
	T := &Bridges{AppCore{DB: OpenCache()}}
	T.setConfig(bridgesConfig{Enabled: true, SelfHandle: "+15550100"})
	for _, c := range []struct {
		service, handle string
		want            bool
	}{
		{"imessage", "", true},
		{"teams", "", false},
		{"slack", "", false},
		{"mattermost", "", false},
		{"slack", "+15550100", true},
		{"imessage", "+15550199", false},
	} {
		if got := T.isOwnerHandleFor(c.service, c.handle); got != c.want {
			t.Errorf("%s %q: owner=%v, want %v", c.service, c.handle, got, c.want)
		}
	}
	if (messagingLinkImpl{T: T}).IsOwnerHandle("", "") {
		t.Error("the shared check, with no service to read, counts no empty handle as the owner")
	}
}
