package core

import (
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

func TestChannelAllowsSender(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })

	// A parent-bound group channel that grants "sub" send access.
	SaveChannel(RootDB, Channel{
		ID: "c1", Owner: "u", Name: "grp", Service: "imessage",
		Address: "chat123", AgentID: "parent", AuthorizedSenders: []string{"sub"},
	})

	cases := []struct {
		name          string
		chatID, agent string
		want          bool
	}{
		{"bound agent", "chat123", "parent", true},
		{"granted sub-agent", "chat123", "sub", true},
		{"unrelated agent", "chat123", "other", false},
		{"wrong conversation", "chatXXX", "sub", false}, // per-address channel scoping
		{"empty agent", "chat123", "", false},
	}
	for _, c := range cases {
		if got := ChannelAllowsSender(RootDB, "u", c.chatID, "", c.agent); got != c.want {
			t.Errorf("%s: ChannelAllowsSender=%v want %v", c.name, got, c.want)
		}
	}

	// A whole-service channel (Address=="") matches any conversation.
	SaveChannel(RootDB, Channel{ID: "c2", Owner: "u2", Service: "slack", AgentID: "a", AuthorizedSenders: []string{"helper"}})
	if !ChannelAllowsSender(RootDB, "u2", "anychat", "", "helper") {
		t.Error("whole-service grant should match any conversation")
	}
}
