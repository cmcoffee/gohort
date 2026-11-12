package orchestrate

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A photo in a message the agent was not woken for reaches the agent's image
// space and leaves a line in its thread, caption or none; a voice memo the
// connector put among the pictures is not taken for one.
func TestANotWokenForPhotoReachesTheAgent(t *testing.T) {
	df := newDispatchFixture(t)
	savedDir, savedCaption := ImageDir(), CaptionOnRecord
	SetImageDir(t.TempDir())
	CaptionOnRecord = false // described in the background; core's tests cover that
	t.Cleanup(func() { SetImageDir(savedDir); CaptionOnRecord = savedCaption })

	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	pic := base64.StdEncoding.EncodeToString(buf.Bytes())
	memo := base64.StdEncoding.EncodeToString([]byte("not a picture at all, an audio memo"))
	df.app.recordChannelSilent(ChannelInbound{Owner: "u", AgentID: df.agent.ID, SessionID: "room-photo", ChatID: "room-photo",
		SenderName: "Alex", Handle: "+15550100", Images: []string{pic, memo}})

	sess, ok := loadChatSession(df.udb, df.agent.ID, df.app.effectiveChannelSession("u", df.agent.ID, "room-photo"))
	if !ok || len(sess.Messages) != 1 || !strings.Contains(sess.Messages[0].Content, "[1 image(s) attached from Alex]") {
		t.Fatalf("a photo with no text left no line in the thread: %+v", sess.Messages)
	}
	got := RecentImages(&ToolSession{Username: "u", AgentID: df.agent.ID})
	if len(got) != 1 || !strings.HasPrefix(got[0].Note, "received from Alex") {
		t.Errorf("the agent's image space holds %+v, want the one picture from Alex", got)
	}
}
