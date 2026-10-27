package bridges

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A finished background job could not send the file it had just produced. The
// outbox has carried a Videos field all along; the seam in front of it took
// images only, so nothing could reach it from the wake path. The reply went
// out, the video did not, and the one trace was a log line saying so.
//
// Telling the agent it forgot is what made it work, because a follow-up is an
// ordinary channel reply and THAT path already carried videos. Same agent, same
// conversation, different exit.
func TestDeliverMediaPutsVideosOnTheOutbox(t *testing.T) {
	T := &Bridges{AppCore{DB: OpenCache()}}
	c := channelThreadsImpl{T: T}

	if err := c.DeliverMedia("craig", "imessage", "chat1", "", "here it is", "Wren",
		[]string{"img-b64"}, []string{"vid-b64"}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	items := T.drainOutbox("imessage", "craig")
	if len(items) != 1 {
		t.Fatalf("expected one queued item, got %d", len(items))
	}
	if got := items[0].Videos; len(got) != 1 || got[0] != "vid-b64" {
		t.Errorf("video did not reach the outbox: %+v", got)
	}
	if got := items[0].Images; len(got) != 1 || got[0] != "img-b64" {
		t.Errorf("images regressed while adding video: %+v", got)
	}
}

// Deliver is now the no-video call of the same body, so the images-only callers
// must be unchanged: nine of them send text or images and never a video.
func TestDeliverStillWorksWithoutVideos(t *testing.T) {
	T := &Bridges{AppCore{DB: OpenCache()}}
	c := channelThreadsImpl{T: T}

	if err := c.Deliver("craig", "imessage", "chat1", "", "text only", "Wren", nil); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	items := T.drainOutbox("imessage", "craig")
	// The name tag is prefixed at the outbox chokepoint, so the text is
	// "[Wren] text only" by design; assert the payload, not the wire form.
	if len(items) != 1 || !strings.Contains(items[0].Text, "text only") {
		t.Fatalf("plain deliver broke: %+v", items)
	}
	if len(items[0].Videos) != 0 {
		t.Errorf("a text-only send invented a video: %+v", items[0].Videos)
	}
}

// The transport must satisfy the optional capability, or the wake path type
// assertion silently falls back to images-only and the bug returns with the
// same symptom and no error anywhere.
func TestBridgesSatisfiesTheMediaDeliverer(t *testing.T) {
	var _ ChannelMediaDeliverer = channelThreadsImpl{}
}

// A reply the agent sent as several messages goes out as several, in order,
// the pictures and the spoken reply on the first only, and the connector's
// poll carries the spoken reply under the name it reads.
func TestAReplyOfSeveralMessagesGoesOutAsSeveral(t *testing.T) {
	T := &Bridges{AppCore{DB: OpenCache()}}
	T.enqueueReply(OutboxItem{ChatID: "chat1", Service: "imessage", Owner: "craig", Text: "one", Images: []string{"img"}, Audios: []string{"aud"}, Type: "reply"},
		[]string{"two", "three"})
	items := T.drainOutbox("imessage", "craig")
	if len(items) != 3 {
		t.Fatalf("want 3 messages, got %d: %+v", len(items), items)
	}
	for i, want := range []string{"one", "two", "three"} {
		if !strings.Contains(items[i].Text, want) || items[i].ChatID != "chat1" {
			t.Errorf("message %d = %+v, want %q to chat1", i, items[i], want)
		}
	}
	if len(items[0].Images) != 1 || len(items[1].Images)+len(items[2].Images) != 0 {
		t.Errorf("the picture should ride the first message only: %+v", items)
	}
	if len(items[0].Audios) != 1 || len(items[1].Audios)+len(items[2].Audios) != 0 {
		t.Errorf("the spoken reply should ride the first message only: %+v", items)
	}
	if wire, _ := json.Marshal(items[0]); !strings.Contains(string(wire), `"audios":["aud"]`) {
		t.Errorf("the poll should carry it as audios: %s", wire)
	}
	if items[1].ID == items[0].ID || items[2].Seq <= items[1].Seq {
		t.Errorf("each message needs its own id, in order: %+v", items)
	}
}

// End to end: a voice note in, and the agent's spoken reply reaches the
// connector's poll on its own: the words once, as a voice note.
func TestASpokenReplyReachesTheConnector(t *testing.T) {
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prevRoot })
	T := &Bridges{AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	T.setConfig(bridgesConfig{Enabled: true})
	const chat = "any;-;+15550100"
	SaveChannel(RootDB, Channel{ID: "ch-voice", Owner: "alice", Name: "Voice", Service: "imessage", Address: chat, AgentID: "agent-1", AutoReply: true})
	RegisterChannelAgentRunner(func(_ context.Context, in ChannelInbound) (ChannelReply, error) {
		return ChannelReply{Text: "at noon", Audios: []string{"c3Bva2Vu"}}, nil
	})
	t.Cleanup(func() { RegisterChannelAgentRunner(nil) })

	T.ingestInbound(BridgeKey{Owner: "alice", Service: "imessage", Enabled: true},
		hookRequest{ChatID: chat, Handle: "+15550100", Audios: []string{"dm9pY2U="}, RowID: 201})
	deadline := time.Now().Add(10 * time.Second)
	for {
		if items := T.drainOutbox("imessage", "alice"); len(items) > 0 {
			if len(items) != 1 || items[0].Text != "" || len(items[0].Audios) != 1 || items[0].Audios[0] != "c3Bva2Vu" {
				t.Errorf("the reply should go out as its voice note alone: %+v", items)
			}
			if len(items) == 1 && items[0].AudioName != "at noon" {
				t.Errorf("the voice note should be named for what it says: %q", items[0].AudioName)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the reply never reached the outbox")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Only iMessage delivers audio, so only there does a voice note stand in for
// the text; a reply with no voice note always keeps its text.
func TestVoiceOnlyWhereAudioIsDelivered(t *testing.T) {
	spoken := ChannelReply{Text: "at noon", Audios: []string{"aud"}}
	cases := []struct {
		svc   string
		reply ChannelReply
		want  bool
	}{
		{"imessage", spoken, true},
		{"telegram", spoken, false},
		{"imessage", ChannelReply{Text: "at noon"}, false},
	}
	for _, c := range cases {
		if got := voiceOnly(c.svc, c.reply); got != c.want {
			t.Errorf("voiceOnly(%q, audios=%d) = %v, want %v", c.svc, len(c.reply.Audios), got, c.want)
		}
	}
}

// A spoken reply's clip is named like a subject line, by its first words, and
// by its agent only when the agent signs its messages.
func TestVoiceNoteName(t *testing.T) {
	cases := []struct{ name, agent, text, want string }{
		{"first words", "", "Sure, I can call you at noon tomorrow if that works.", "Sure, I can call you at"},
		{"signed agent", "Wren", "Sure, at noon.", "Wren - Sure, at noon."},
		{"markdown and links left out", "", "**Done.** See [the notes](https://example.com/x)", "Done. See the notes"},
		{"trailing punctuation trimmed", "", "Here's the plan: one, two, three, four, five", "Here's the plan- one, two, three"},
		{"nothing to say", "", "```\nls\n```", "Voice reply"},
		{"no path in a name", "a/b", "c:d", "a-b - c-d"},
	}
	for _, c := range cases {
		if got := voiceNoteName(c.agent, c.text); got != c.want {
			t.Errorf("%s: voiceNoteName(%q, %q) = %q, want %q", c.name, c.agent, c.text, got, c.want)
		}
	}
	if got := voiceNoteName("", strings.Repeat("abcdefghij", 8)); len([]rune(got)) > 48 {
		t.Errorf("a long first word should be cut: %q", got)
	}
}
