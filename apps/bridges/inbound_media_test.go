package bridges

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/messaging"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A photo-only iMessage reads as a picture post, not as a caption made of the
// mark iMessage leaves where the attachment sits. Observed 2026-10-23: judged
// as "[image with caption: ￼]", it was blocked as a captioned image with
// nothing to say.
func TestAPhotoOnlyMessageHasNoCaption(t *testing.T) {
	if got := withoutAttachmentMarks("￼", true); got != "" {
		t.Errorf("a photo-only message kept %q as its text", got)
	}
	if got := withoutAttachmentMarks("￼￼ look at these", true); got != "look at these" {
		t.Errorf("a caption should survive with the marks taken out: %q", got)
	}
	// Nothing came with it: the mark is the only sign an attachment was there.
	if got := withoutAttachmentMarks("￼", false); got != "￼" {
		t.Errorf("a message with no media should keep its text: %q", got)
	}
}

// A blocked message's pictures wait for the sender's next message that is let
// through, in that conversation only. Observed: a photo blocked, then "there
// ya go, can you make it now?" arrived with nothing attached.
func TestABlockedPictureRidesTheNextMessage(t *testing.T) {
	const owner, chat = "owner@test", "any;+;chat-held"
	holdBlockedMedia(owner, chat, "", []string{"cGhvdG8x"}, nil)
	holdBlockedMedia(owner, chat, "", []string{"cGhvdG8y"}, nil)
	if imgs, _ := takeHeldMedia(owner, "any;+;chat-other", ""); len(imgs) != 0 {
		t.Error("another conversation must not get them")
	}
	if imgs, _ := takeHeldMedia(owner, chat, "+15550199"); len(imgs) != 0 {
		t.Error("another sender in the same conversation must not get them")
	}
	imgs, _ := takeHeldMedia(owner, chat, "")
	if len(imgs) != 2 || imgs[0] != "cGhvdG8x" || imgs[1] != "cGhvdG8y" {
		t.Fatalf("held = %q, want both, oldest first", imgs)
	}
	if again, _ := takeHeldMedia(owner, chat, ""); len(again) != 0 {
		t.Error("taken once, they are gone")
	}
	holdBlockedMedia(owner, chat, "", nil, nil)
	if imgs, vids := takeHeldMedia(owner, chat, ""); len(imgs)+len(vids) != 0 {
		t.Error("a message with no media holds nothing")
	}
}

// Texting your own number writes two rows, and each reached the agent: every
// message answered twice in a thread where both sides are you. The received
// copy is recorded and not routed; the owner's own message never is.
func TestTheSecondCopyOfAMessageToYourselfIsNotAnswered(t *testing.T) {
	T := &Bridges{AppCore{DB: OpenCache()}}
	T.setConfig(bridgesConfig{Enabled: true, SelfHandle: "+1 (555) 010-0001"})
	self := "any;-;+15550100001"
	if T.isSelfCopy(self, "", "Hi") {
		t.Fatal("the message the owner sent is not the copy")
	}
	if !T.isSelfCopy(self, "+15550100001", "hi") {
		t.Error("the received copy of it is")
	}
	if T.isSelfCopy(self, "+15550100001", "hi") {
		t.Error("a later message with the same text is a new message")
	}
	// The received copy first: in the owner's own thread the sent one is then
	// the copy.
	if T.isSelfCopy(self, "+15550100001", "again") || !T.isSelfCopy(self, "", "again") {
		t.Error("in the owner's own thread either order pairs")
	}

	// Someone else's thread, with the owner's number set: never.
	bob := "any;-;+15550100002"
	if T.isSelfCopy(bob, "", "ok") || T.isSelfCopy(bob, "+15550100002", "ok") {
		t.Error("a reply in someone else's thread is theirs")
	}
	// Groups: never.
	if T.isSelfCopy("any;+;chat-group", "", "ok") || T.isSelfCopy("any;+;chat-group", "+15550100002", "ok") {
		t.Error("a group has no second copy")
	}

	// No number set: the pairing alone decides, and only the received copy
	// is ever taken for the copy.
	U := &Bridges{AppCore{DB: OpenCache()}}
	U.setConfig(bridgesConfig{Enabled: true})
	if U.isSelfCopy(self, "+15550100001", "first") || U.isSelfCopy(self, "", "first") {
		t.Error("unset, the owner's own message must never be dropped")
	}
	if U.isSelfCopy(self, "", "second") || !U.isSelfCopy(self, "+15550100001", "second") {
		t.Error("unset, the received copy after the owner's message is the copy")
	}
}

// End to end: a photo-only message the gatekeeper blocks, then the same
// person's next message, let through, reaches the agent with the photo.
func TestABlockedPhotoReachesTheAgentWithTheNextMessage(t *testing.T) {
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prevRoot })
	T := &Bridges{AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	T.setConfig(bridgesConfig{Enabled: true})
	const chat = "any;+;chat-held-e2e"
	SaveChannel(RootDB, Channel{ID: "ch-held", Owner: "alice", Name: "Group", Service: "imessage", Address: chat, AgentID: "agent-1", AutoReply: true})

	var judged []string
	var mu sync.Mutex
	RegisterChannelGatekeeper(func(_ context.Context, in ChannelInbound) bool {
		mu.Lock()
		judged = append(judged, in.Text)
		mu.Unlock()
		return strings.TrimSpace(in.Text) != "" // a picture with nothing said is not for the agent
	})
	got := make(chan ChannelInbound, 2)
	RegisterChannelAgentRunner(func(_ context.Context, in ChannelInbound) (ChannelReply, error) {
		got <- in
		return ChannelReply{}, nil
	})
	t.Cleanup(func() { RegisterChannelGatekeeper(nil); RegisterChannelAgentRunner(nil) })

	key := BridgeKey{Owner: "alice", Service: "imessage", Enabled: true}
	T.ingestInbound(key, hookRequest{ChatID: chat, Text: "￼", Images: []string{"cGhvdG8="}, RowID: 101})
	deadline := time.Now().Add(5 * time.Second)
	for {
		heldMedia.Lock()
		_, held := heldMedia.m[heldMediaKey("alice", chat, "")]
		heldMedia.Unlock()
		if held {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the blocked photo was not held")
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	if len(judged) != 1 || judged[0] != "" {
		t.Errorf("the gatekeeper should have judged a picture with no text, got %q", judged)
	}
	mu.Unlock()

	T.ingestInbound(key, hookRequest{ChatID: chat, Text: "there ya go, can you make it now?", RowID: 102})
	select {
	case in := <-got:
		if len(in.Images) != 1 || in.Images[0] != "cGhvdG8=" || !strings.Contains(in.Text, "there ya go") {
			t.Errorf("the agent got %q with %d picture(s)", in.Text, len(in.Images))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the allowed message never reached the agent")
	}
}

// A contact's message reaches the inbound watchers whether or not a channel
// wakes for it: here there is no channel at all, so it is only recorded. The
// owner's own messages are not handed on.
func TestAContactsMessageIsWatchedWithNoChannelToWake(t *testing.T) {
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prevRoot })
	T := &Bridges{AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	T.setConfig(bridgesConfig{Enabled: true})
	RegisterChannelAgentRunner(func(_ context.Context, in ChannelInbound) (ChannelReply, error) {
		t.Errorf("no channel is bound, yet an agent ran for %q", in.Text)
		return ChannelReply{}, nil
	})
	t.Cleanup(func() { RegisterChannelAgentRunner(nil) })
	var seen []messaging.InboundEvent
	var mu sync.Mutex
	messaging.RegisterInboundWatcher(func(ev messaging.InboundEvent) {
		if ev.Owner == "watcher-owner" {
			mu.Lock()
			seen = append(seen, ev)
			mu.Unlock()
		}
	})
	key := BridgeKey{Owner: "watcher-owner", Service: "imessage", Enabled: true}
	const chat = "any;-;+15550177"
	T.ingestInbound(key, hookRequest{ChatID: chat, Handle: "+15550177", Text: "yes, logging in now", RowID: 201})
	T.ingestInbound(key, hookRequest{ChatID: chat, Text: "my own note", RowID: 202})
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0].Text != "yes, logging in now" || seen[0].Handle != "+15550177" || seen[0].ChatID != chat {
		t.Fatalf("watched %+v", seen)
	}
	found := false
	for _, a := range seen[0].Addresses {
		found = found || a == "+15550177"
	}
	if !found {
		t.Errorf("the sender's handle is not among the addresses: %v", seen[0].Addresses)
	}
}
