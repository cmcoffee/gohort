package orchestrate

// "Make the picture, then send it to the other group chat": the render ran in
// the background, the wake that held the finished picture had no way to reach
// another chat, and the turns that could reach it had no way to name the
// picture. Each half is covered here.

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A picture the agent made is attachable by its image# handle; a photo a
// person sent is not, so it is never posted straight back.
func TestAPictureTheAgentMadeIsAttachableByItsHandle(t *testing.T) {
	SetImageDir(t.TempDir())
	sess := &ToolSession{Username: "craig", AgentID: "wiwee", WorkspaceDir: t.TempDir()}
	_, theirs := RecordRecentImageStable(sess, notePNG(t), "a photo someone sent", ImageFromUser)
	_, mine := RecordRecentImageStable(sess, notePNG(t), "edited: the labelled poster", ImageFromEdited)
	if mine == "" || theirs == "" {
		t.Fatal("no ring ids")
	}
	if _, kind, ok := resolveAttachmentRef(sess, mine, true); !ok || kind != "image" {
		t.Errorf("the agent's own picture is not attachable by %s", mine)
	}
	if _, _, ok := resolveAttachmentRef(sess, theirs, true); ok {
		t.Errorf("a person's photo became attachable by %s", theirs)
	}
	if pic, ok := unsentPicture(sess); !ok || pic.ID != mine {
		t.Errorf("the fresh picture beside a text-only send: %+v %v", pic, ok)
	}
	only := &ToolSession{Username: "craig", AgentID: "other", WorkspaceDir: t.TempDir()}
	RecordRecentImageStable(only, notePNG(t), "a photo someone sent", ImageFromUser)
	if _, ok := unsentPicture(only); ok {
		t.Error("a person's photo was offered as the agent's unsent picture")
	}
}

// The wake's own delivery is skipped only when the turn sent to its
// conversation; a send to another chat leaves the person who asked to hear it
// is done.
func TestAWakeThatSentElsewhereStillAnswersWhoAsked(t *testing.T) {
	const here, there = "chat-here", "chat-there"
	sent := func(to string) PersistedToolCall {
		return PersistedToolCall{Name: "send_message", Args: map[string]any{"to": to}, Result: "Sent to " + to + " (authorized sender for this channel)."}
	}
	if sentToConversation([]PersistedToolCall{sent(there)}, here, "") {
		t.Error("a send to another chat counted as answering this one")
	}
	if !sentToConversation([]PersistedToolCall{sent(there), sent(here)}, here, "") {
		t.Error("a send to this chat was not counted")
	}
	failed := sent(here)
	failed.Err = "not delivered"
	if sentToConversation([]PersistedToolCall{failed}, here, "") {
		t.Error("a failed send counted")
	}
	held := PersistedToolCall{Name: "send_message", Args: map[string]any{"to": here}, Result: "Queued for your approval."}
	if sentToConversation([]PersistedToolCall{held}, here, "") {
		t.Error("a send waiting for approval counted as delivered")
	}
}

// A background task's wake mounts the agent's channel tools; another fire
// does not, since its report has a delivery of its own.
func TestAWakeReachesTheAgentsChannels(t *testing.T) {
	b, err := os.ReadFile("scheduled_updates.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "if isTaskWake(p.Prompt) {\n\t\tfr.tools = append(fr.tools, channelChatTools(fr.subSess, p.Username, fr.agent.ID)...)") {
		t.Error("a wake has no channel tools")
	}
}
