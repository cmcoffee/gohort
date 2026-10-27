package ui

import (
	"encoding/json"
	"strings"
	"testing"
)

// The code editor's chat is the host's own panel, serialized beside the
// editor's config like the workbench's: the panel the chat runtime mounts.
func TestTheCodeEditorCarriesItsHostsChat(t *testing.T) {
	b, err := json.Marshal(CodeEditorPanel{ListURL: "api/snippets", Chat: AgentLoopPanel{SendURL: "chat/send", CancelURL: "chat/cancel"}})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Type string         `json:"type"`
		Chat map[string]any `json:"chat"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "code_editor_panel" || got.Chat["type"] != "agent_loop_panel" || got.Chat["send_url"] != "chat/send" {
		t.Errorf("the panel serialized as %s", b)
	}
	if b, _ := json.Marshal(CodeEditorPanel{ListURL: "api/snippets"}); strings.Contains(string(b), `"chat"`) {
		t.Errorf("an editor with no chat serialized one: %s", b)
	}
}

// The chat fills the editor's chat column. Sized as a page of its own, its
// input row and Send sat below the column's clipped bottom edge, and the chat
// could not be typed into.
func TestTheCodeEditorsChatFillsItsColumn(t *testing.T) {
	for _, want := range []string{
		".ui-cw-chat > .ui-agent { height: 100%",
		".ui-cw-chat .ui-agent { height: 100%; min-height: 0; }",
		".ui-cw-chat .ui-agent-grid { height: 100%; }",
	} {
		if !strings.Contains(runtimeCSS, want) {
			t.Errorf("runtime.css is missing %q", want)
		}
	}
}
