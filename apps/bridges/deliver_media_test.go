package bridges

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
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
	items := T.drainOutbox("imessage")
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
	items := T.drainOutbox("imessage")
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
